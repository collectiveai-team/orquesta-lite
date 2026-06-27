package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/agenthealth"
	"github.com/lionelchamorro/orquestalite/internal/config"
	"github.com/lionelchamorro/orquestalite/internal/eventlog"
	"github.com/lionelchamorro/orquestalite/internal/fallback"
	"github.com/lionelchamorro/orquestalite/internal/gitx"
	"github.com/lionelchamorro/orquestalite/internal/handoff"
	"github.com/lionelchamorro/orquestalite/internal/invoke"
	"github.com/lionelchamorro/orquestalite/internal/loops"
	"github.com/lionelchamorro/orquestalite/internal/preflight"
	"github.com/lionelchamorro/orquestalite/internal/results"
	"github.com/lionelchamorro/orquestalite/internal/runner"
	"github.com/lionelchamorro/orquestalite/internal/sessions"
	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

// agentHealthThreshold is the default number of consecutive non-rate-limit
// failures after which an agent is auto-skipped for the rest of the run.
// Set to 2 because a single failure may be a transient network blip, but two
// in a row almost always means a stale model, wrong auth, or missing CLI.
const agentHealthThreshold = 2

var staticRunPreflightRoles = []string{"parser", "coder", "tester", "critic", "reviewer", "verifier"}

// RunOptions holds the parameters for the run command.
type RunOptions struct {
	ProjectDir string
	TeamPath   string
	// LogFormat controls the stdout pretty format. One of eventlog.FormatVerbose
	// (default; one line per event with all fields) or eventlog.FormatHuman
	// (compact, one summary per agent transition). The JSONL file is unaffected.
	LogFormat eventlog.Format
	// FeatureID, when set (factory mode), namespaces provider session keys so
	// identical per-feature task IDs do not resume each other's sessions.
	FeatureID string
}

// Run loads the config and tasks, wires up all components, and drives the
// review loop until it completes or the context is cancelled.
func Run(ctx context.Context, opts RunOptions) error {
	deps, cleanup, err := newLiveDeps(liveDepsOptions{
		ProjectDir: opts.ProjectDir,
		TeamPath:   opts.TeamPath,
		LogFormat:  opts.LogFormat,
		Roles:      staticRunPreflightRoles,
		FeatureID:  opts.FeatureID,
	})
	if err != nil {
		return err
	}
	defer cleanup()

	return loops.RunReviewLoop(ctx, deps.tl, loops.ReviewConfig{MaxCycles: deps.cfg.Limits.MaxReviewCycles}, deps)
}

type liveDepsOptions struct {
	ProjectDir string
	TeamPath   string
	LogFormat  eventlog.Format
	Roles      []string
	FeatureID  string
}

// resolveLogFormat turns an explicit/auto format choice into a concrete one.
// "" and "auto" select human when stdout is an interactive terminal (so an
// operator gets readable output) and verbose otherwise (so pipes and CI keep
// the machine-parseable key=value stream).
func resolveLogFormat(f eventlog.Format, out *os.File) eventlog.Format {
	switch f {
	case eventlog.FormatHuman, eventlog.FormatVerbose:
		return f
	default: // "" or "auto"
		if isTerminal(out) {
			return eventlog.FormatHuman
		}
		return eventlog.FormatVerbose
	}
}

func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func newLiveDeps(opts liveDepsOptions) (*liveDeps, func() error, error) {
	cfg, err := config.Load(opts.TeamPath)
	if err != nil {
		return nil, nil, err
	}
	specs, err := cfg.Resolve()
	if err != nil {
		return nil, nil, err
	}

	tasksPath := filepath.Join(opts.ProjectDir, ".orquestalite", "tasks.json")
	logPath := filepath.Join(opts.ProjectDir, ".orquestalite", "run.log")
	logger, err := eventlog.OpenWithFormat(logPath, os.Stdout, resolveLogFormat(opts.LogFormat, os.Stdout))
	if err != nil {
		return nil, nil, err
	}
	cleanup := logger.Close
	ok := false
	defer func() {
		if !ok {
			_ = cleanup()
		}
	}()

	// If the verification commands are unset (e.g. init couldn't detect the
	// repo's language, or this is a fresh factory checkout) try to fill them
	// from the project layout so the gates aren't silently skipped. Empty stays
	// a no-op; detection is best-effort and never aborts the run.
	if cfg.FullTestCommand == "" {
		if cmd := detectTestCommand(opts.ProjectDir); cmd != "" {
			cfg.FullTestCommand = cmd
			persisted := persistConfigString(opts.TeamPath, "full_test_command", cmd) == nil
			logger.Log(eventlog.Event{Type: "test_command_detected", Fields: map[string]any{
				"command":   cmd,
				"persisted": persisted,
			}})
		}
	}
	if cfg.LintCommand == "" {
		if cmd := detectLintCommand(opts.ProjectDir); cmd != "" {
			cfg.LintCommand = cmd
			persisted := persistConfigString(opts.TeamPath, "lint_command", cmd) == nil
			logger.Log(eventlog.Event{Type: "lint_command_detected", Fields: map[string]any{
				"command":   cmd,
				"persisted": persisted,
			}})
		}
	}

	memPath := filepath.Join(opts.ProjectDir, ".orquestalite", "memory.md")

	fc := fallback.NewCaller(fallback.Config{
		InitialBackoff: time.Duration(cfg.RateLimitBackoff.InitialSeconds) * time.Second,
		Factor:         cfg.RateLimitBackoff.Factor,
		MaxBackoff:     time.Duration(cfg.RateLimitBackoff.MaxSeconds) * time.Second,
		// Surface the wait so a long sleep for a rate-limited agent is not
		// silent — the loop waits for the agent to recover rather than failing.
		OnWait: func(agent, reason string, until time.Time) {
			logger.Log(eventlog.Event{Type: "rate_limit_wait", Fields: map[string]any{
				"agent":   agent,
				"reason":  reason,
				"until":   until.Format("15:04:05"),
				"seconds": int(time.Until(until).Seconds()),
			}})
		},
	})

	tracker := agenthealth.New(agentHealthThreshold)
	runStaticAgentPreflight(cfg, tracker, logger, opts.Roles)

	var deps *liveDeps
	inv := &invoke.RoleInvoker{
		Specs:                   specs,
		Dir:                     opts.ProjectDir,
		Fallback:                fc,
		Log:                     logger,
		Health:                  tracker,
		MemPath:                 memPath,
		Runner:                  invoke.ExecRunner{},
		DefaultRateLimitPattern: cfg.RateLimitBackoff.DefaultPattern,
		AgentHealthThreshold:    agentHealthThreshold,
		ConventionsPath:         cfg.ConventionsFile,
		OnAgentSuccess: func(role, agent string) {
			if role == "coder" && deps.currentTask != nil {
				deps.currentTask.LastAgent = agent
			}
		},
	}
	inv.SessionNamespace = opts.FeatureID
	// Let the coder resume its provider session across fix-loop iterations (and
	// across a factory --resume) when the same agent runs again on the same task.
	if cfg.Limits.SessionResumeEnabled() {
		inv.Sessions = sessions.Load(opts.ProjectDir)
		inv.ResumeRoles = map[string]bool{"coder": true}
	}

	tl, err := tasks.Load(tasksPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
		tl = &tasks.TaskList{}
	}

	deps = &liveDeps{
		cfg:          cfg,
		dir:          opts.ProjectDir,
		fc:           fc,
		log:          logger,
		tl:           tl,
		memPath:      memPath,
		tasksPath:    tasksPath,
		currentCycle: 0,
		health:       tracker,
		inv:          inv,
	}

	ok = true
	return deps, cleanup, nil
}

// runStaticAgentPreflight verifies that each agent referenced by the chosen roles
// either declares a provider that orq-lite knows about (claude, codex) and
// whose CLI is on PATH, OR a cmd whose first token is on PATH. Agents that
// fail the check are marked skipped in the tracker so the fallback caller
// can move past them without burning a real invocation. Never blocks the run
// — the goal is to surface obvious misconfiguration cheaply.
func runStaticAgentPreflight(cfg *config.Config, tracker *agenthealth.Tracker, log *eventlog.Logger, roles []string) {
	used := map[string]bool{}
	for _, roleName := range roles {
		role, ok := cfg.Roles[roleName]
		if !ok {
			continue
		}
		for _, name := range role.Agents {
			used[name] = true
		}
		for _, name := range role.EscalationLadder {
			used[name] = true
		}
	}
	names := make([]string, 0, len(used))
	for n := range used {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		ag, ok := cfg.Agents[name]
		if !ok {
			continue
		}
		var binary string
		switch {
		case ag.Provider != "":
			// Provider name matches the CLI binary name (claude, codex, gemini).
			binary = ag.Provider
		case len(ag.Cmd) > 0:
			binary = ag.Cmd[0]
		default:
			continue
		}
		if _, err := exec.LookPath(binary); err != nil {
			tracker.Skip(name, agenthealth.ReasonUnreachable)
			log.Log(eventlog.Event{Type: "preflight_skipped_agent", Fields: map[string]any{
				"agent":  name,
				"binary": binary,
				"reason": "binary_not_in_path",
			}})
			continue
		}
		// Skip provider agents with no usable headless credential up front, so
		// the run never wastes an invocation discovering an interactive auth
		// prompt mid-task (e.g. an un-logged-in gemini CLI).
		if ag.Provider != "" && !providerHasUsableCredentials(ag.Provider) {
			tracker.Skip(name, agenthealth.ReasonNoCredentials)
			log.Log(eventlog.Event{Type: "preflight_skipped_agent", Fields: map[string]any{
				"agent":    name,
				"binary":   binary,
				"provider": ag.Provider,
				"reason":   "no_credentials",
			}})
		}
	}
}

// liveDeps implements loops.ReviewDeps using real subprocess agents.
type liveDeps struct {
	cfg          *config.Config
	dir          string
	fc           *fallback.Caller
	log          *eventlog.Logger
	tl           *tasks.TaskList
	memPath      string
	tasksPath    string
	currentCycle int
	currentTask  *tasks.Task
	health       *agenthealth.Tracker
	inv          *invoke.RoleInvoker

	// Rollback baseline captured at the start of each task (in RunFix). On
	// failure, Rollback resets tracked files to taskBaseSHA and removes only
	// untracked files NOT in taskUntracked — so the agent's work is undone
	// without deleting pre-existing user files. See gitx.RollbackTo.
	taskBaseSHA   string
	taskUntracked map[string]bool
}

// captureRollbackBase snapshots the work tree at the start of a task so a later
// Rollback can restore exactly this state. No-op outside a git repo.
func (d *liveDeps) captureRollbackBase() {
	d.taskBaseSHA = ""
	d.taskUntracked = nil
	if !gitx.IsRepo(d.dir) {
		return
	}
	sha, err := gitx.HeadSHA(d.dir)
	if err != nil {
		return
	}
	files, err := gitx.UntrackedFiles(d.dir)
	if err != nil {
		return
	}
	set := make(map[string]bool, len(files))
	for _, f := range files {
		set[f] = true
	}
	d.taskBaseSHA = sha
	d.taskUntracked = set
}

// RunFix sets up the current task and runs the fix loop.
func (d *liveDeps) RunFix(ctx context.Context, taskID string, rc invoke.RunContext) (*loops.FixResult, error) {
	for i, t := range d.tl.Tasks {
		if t.ID == taskID {
			d.currentTask = &d.tl.Tasks[i]
			break
		}
	}
	// Snapshot the tree before the agent touches anything; Rollback restores to
	// here. RunFix always runs before any Rollback in the same task iteration.
	d.captureRollbackBase()
	escalationLadder := []string{}
	if coderRole, ok := d.cfg.Roles["coder"]; ok {
		escalationLadder = coderRole.EscalationLadder
	}
	rr := &liveRoleRunner{deps: d}
	d.currentCycle = rc.Cycle
	return loops.RunFix(ctx, loops.FixConfig{
		MaxIterations:    d.cfg.Limits.MaxFixIterations,
		EscalationLadder: escalationLadder,
		VerifierEnabled:  d.cfg.VerifierPerTask(),
		LintGate:         d.lintGateOutcome,
	}, rr, rc)
}

// FullSuite runs the full test command specified in team.json after a task.
// (Lint runs inside the fix loop via lintGateOutcome, so a violation is fixed
// in-loop rather than only blocking here.)
func (d *liveDeps) FullSuite(ctx context.Context) error {
	parts := strings.Fields(d.cfg.FullTestCommand)
	if len(parts) == 0 {
		return nil
	}
	c := exec.CommandContext(ctx, parts[0], parts[1:]...)
	c.Dir = d.dir
	out, err := c.CombinedOutput()
	if err != nil {
		// pytest exits 5 when no tests were collected. Early scaffolding tasks
		// legitimately run before any test exists; treat that as a pass with a
		// warning instead of failing every task until the first test lands.
		if exitCode(err) == 5 && strings.Contains(d.cfg.FullTestCommand, "pytest") {
			d.log.Log(eventlog.Event{Type: "full_suite_empty", Fields: map[string]any{
				"command": d.cfg.FullTestCommand,
			}})
			return nil
		}
		d.log.Log(eventlog.Event{Type: "full_suite_failed", Fields: map[string]any{
			"output_tail": runner.TailString(string(out), 1024),
		}})
		return loops.ErrFullSuiteFailed
	}
	return nil
}

// lintGateOutcome runs the configured lint command as a deterministic quality
// gate inside the fix loop. It returns ok=true when there is no linter, the
// linter is clean, or the lint binary is missing (a missing/unconfigured
// linter must never block every task — that is logged as a skip). On a real
// violation it returns ok=false with the linter's output as coder feedback.
func (d *liveDeps) lintGateOutcome(ctx context.Context) (bool, string) {
	parts := strings.Fields(d.cfg.LintCommand)
	if len(parts) == 0 {
		return true, ""
	}
	c := exec.CommandContext(ctx, parts[0], parts[1:]...)
	c.Dir = d.dir
	out, err := c.CombinedOutput()
	if err == nil {
		return true, ""
	}
	if exitCode(err) < 0 {
		// Binary not found / failed to spawn: skip the gate, do not block.
		d.log.Log(eventlog.Event{Type: "lint_skipped", Fields: map[string]any{
			"command": d.cfg.LintCommand,
			"reason":  err.Error(),
		}})
		return true, ""
	}
	tail := runner.TailString(string(out), 1024)
	d.log.Log(eventlog.Event{Type: "lint_failed", Fields: map[string]any{
		"command":     d.cfg.LintCommand,
		"output_tail": tail,
	}})
	return false, fmt.Sprintf("Lint gate failed (`%s`). Fix these before re-running:\n%s", d.cfg.LintCommand, tail)
}

// exitCode extracts the process exit code from an exec error, or -1.
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// Commit stages all changes and commits them with the given message. If the
// working directory is not a git work tree, Commit returns ErrCommitSkipped
// so the task loop can mark the task done-with-verify=commit_skipped instead
// of treating it as a hard failure (see findings: test #1 marked 8/10 tasks
// failed solely because `orq-lite init` did not run `git init`).
func (d *liveDeps) Commit(ctx context.Context, msg string) (string, error) {
	if !gitx.IsRepo(d.dir) {
		taskID := ""
		if d.currentTask != nil {
			taskID = d.currentTask.ID
		}
		d.log.Log(eventlog.Event{Type: "task_done_no_commit", Fields: map[string]any{
			"task_id": taskID,
			"reason":  "not_a_git_repo",
		}})
		return "", loops.ErrCommitSkipped
	}
	sha, err := gitx.CommitAll(d.dir, msg)
	if errors.Is(err, gitx.ErrNothingToCommit) {
		// No net diff: an earlier task already produced this task's files. Map to
		// the loops sentinel so the task loop records a no-op (commit_empty) and
		// keeps the task done, instead of a commit_rejected hard failure.
		taskID := ""
		if d.currentTask != nil {
			taskID = d.currentTask.ID
		}
		d.log.Log(eventlog.Event{Type: "task_done_no_commit", Fields: map[string]any{
			"task_id": taskID,
			"reason":  "nothing_to_commit",
		}})
		return "", loops.ErrNothingToCommit
	}
	if err != nil {
		return "", err
	}
	taskID := ""
	if d.currentTask != nil {
		taskID = d.currentTask.ID
	}
	d.log.Log(eventlog.Event{Type: "task_done", Fields: map[string]any{
		"task_id":    taskID,
		"commit_sha": sha,
	}})
	return sha, nil
}

// Rollback restores the work tree to the state captured at the start of the
// task (captureRollbackBase): it reverts the agent's tracked changes and removes
// only the untracked files the agent created, leaving pre-existing user files
// intact. No-op outside a git repo.
func (d *liveDeps) Rollback(ctx context.Context) error {
	if !gitx.IsRepo(d.dir) {
		return nil
	}
	return gitx.RollbackTo(d.dir, d.taskBaseSHA, d.taskUntracked)
}

// SaveTasks persists the task list to disk.
func (d *liveDeps) SaveTasks(ctx context.Context, tl *tasks.TaskList) error {
	return tasks.Save(d.tasksPath, tl)
}

// CycleBaseSHA returns the commit at the start of a review cycle. Non-git
// workspaces are allowed; the reviewer prompt will receive an empty base.
func (d *liveDeps) CycleBaseSHA(_ context.Context) (string, error) {
	if !gitx.IsRepo(d.dir) {
		return "", nil
	}
	sha, err := gitx.HeadSHA(d.dir)
	if err != nil {
		return "", nil
	}
	return sha, nil
}

// RunParser invokes the parser role with the given plan text and returns its result.
// The parser prompt uses {{MEMORY}} and {{PLAN}} interpolation variables.
func (d *liveDeps) RunParser(ctx context.Context, plan string) (*results.ParserResult, error) {
	return invoke.Role(ctx, d.inv, "parser", invoke.RoleCall{Vars: map[string]string{"PLAN": plan}}, invoke.RunContext{TaskID: "_plan", Attempt: 1}, results.ParseParser)
}

// CycleVerification runs the verifier role once over the whole increment
// shipped this cycle (per-cycle mode). The report — passed and failed checks
// alike — is handed to the reviewer, which turns failures into next-cycle
// tasks. Verifier-agent failures are folded into the report instead of
// erroring so a flaky verifier cannot kill the run.
func (d *liveDeps) CycleVerification(ctx context.Context, rc invoke.RunContext) (string, error) {
	if !d.cfg.VerifierPerCycle() {
		return "", nil
	}

	gitLog := ""
	if rc.CycleBaseSHA != "" {
		gitLog, _ = gitx.LogStat(d.dir, rc.CycleBaseSHA)
	}
	var doneTitles []string
	for _, t := range d.tl.Tasks {
		if t.Status == tasks.StatusDone {
			doneTitles = append(doneTitles, fmt.Sprintf("- %s: %s", t.ID, t.Title))
		}
	}

	call := invoke.RoleCall{
		ArchiveRole: "verifier-cycle",
		Vars: map[string]string{
			"TASK_ID":          "_cycle",
			"TASK_TITLE":       fmt.Sprintf("End-of-cycle verification (cycle %d)", rc.Cycle),
			"TASK_DESCRIPTION": "Verify the increment shipped this cycle works end to end.\n\nTasks completed:\n" + strings.Join(doneTitles, "\n"),
			"ATTEMPT_NUMBER":   "1",
			"FILES_CHANGED":    gitLog,
			"REVIEW_CYCLE":     fmt.Sprintf("%d", rc.Cycle),
		},
	}
	if cp := d.inv.Specs["verifier"].CyclePrompt; cp != "" {
		call.PromptPath = cp
	}

	cycleRC := rc
	cycleRC.TaskID = "_cycle"
	r, err := invoke.Role(ctx, d.inv, "verifier", call, cycleRC, results.ParseVerifier)
	if err != nil {
		d.log.Log(eventlog.Event{Type: "cycle_verification_error", Fields: map[string]any{
			"cycle": rc.Cycle,
			"error": err.Error(),
		}})
		return fmt.Sprintf("(cycle verification could not run: %v)", err), nil
	}

	failed := 0
	var b strings.Builder
	fmt.Fprintf(&b, "Overall: %s\n", r.Status)
	for _, c := range r.Checks {
		mark := "PASS"
		if !c.Passed {
			mark = "FAIL"
			failed++
		}
		fmt.Fprintf(&b, "[%s] %s — %s; expected: %s; actual: %s\n", mark, c.Name, c.Action, c.Expected, c.Actual)
	}
	d.log.Log(eventlog.Event{Type: "cycle_verification", Fields: map[string]any{
		"cycle":         rc.Cycle,
		"status":        r.Status,
		"checks":        len(r.Checks),
		"failed_checks": failed,
	}})
	return b.String(), nil
}

// RunReviewer invokes the reviewer role and returns its parsed result.
func (d *liveDeps) RunReviewer(ctx context.Context, rc invoke.RunContext, verificationReport string) (results.ReviewerResult, error) {
	d.currentCycle = rc.Cycle
	tasksRaw, _ := os.ReadFile(d.tasksPath)

	gitLog := ""
	if rc.CycleBaseSHA != "" {
		gitLog, _ = gitx.LogStat(d.dir, rc.CycleBaseSHA)
	}
	if verificationReport == "" {
		verificationReport = "(no end-of-cycle verification configured)"
	}

	r, err := invoke.Role(ctx, d.inv, "reviewer", invoke.RoleCall{Vars: map[string]string{
		"REVIEW_CYCLE":        fmt.Sprintf("%d", rc.Cycle),
		"CYCLE_BASE_SHA":      rc.CycleBaseSHA,
		"TASKS_JSON":          string(tasksRaw),
		"GIT_LOG":             gitLog,
		"VERIFICATION_REPORT": verificationReport,
	}}, rc, results.ParseReviewer)
	if err != nil {
		return results.ReviewerResult{}, err
	}
	return *r, nil
}

// Handoff writes a markdown handoff file for the task and logs a handoff_written event.
func (d *liveDeps) Handoff(ctx context.Context, t *tasks.Task) (string, error) {
	path, err := handoff.Write(d.dir, t)
	if err != nil {
		return "", err
	}
	d.log.Log(eventlog.Event{Type: "handoff_written", Fields: map[string]any{
		"task_id": t.ID,
		"path":    path,
	}})
	return path, nil
}

// PreflightEnabled reports whether the opt-in pre-flight validator is active.
func (d *liveDeps) PreflightEnabled() bool {
	return d.cfg.Limits.PreflightEnabled
}

// HasRole reports whether the named role is configured in the team roster.
// Used by the generic-squad dispatcher to fall back to the full fix lane when
// no generalist role is present.
func (d *liveDeps) HasRole(role string) bool {
	_, ok := d.inv.Specs[role]
	return ok
}

// RunSingle invokes one role once (with one retry when the agent writes no
// result), and maps a written result file to done, an absent result after
// retries to failed. No tester / critic / verifier loop runs; this is the lean
// path for the setup and generic squads.
func (d *liveDeps) RunSingle(ctx context.Context, role string, rc invoke.RunContext) (loops.SingleOutcome, error) {
	if _, ok := d.inv.Specs[role]; !ok {
		return loops.SingleOutcome{Status: "failed", Summary: "role not configured: " + role}, nil
	}

	// Mirror RunFix: find the current task so prompt vars resolve correctly.
	for i, t := range d.tl.Tasks {
		if t.ID == rc.TaskID {
			d.currentTask = &d.tl.Tasks[i]
			break
		}
	}
	d.captureRollbackBase()
	d.currentCycle = rc.Cycle

	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		rc.Attempt = attempt
		err := d.inv.RunOnce(ctx, role, invoke.RoleCall{
			Vars: map[string]string{
				"TASK_ID":                  rc.TaskID,
				"TASK_TITLE":               d.currentTaskTitle(),
				"TASK_DESCRIPTION":         d.currentTaskDescription(),
				"ATTEMPT_NUMBER":           strconv.Itoa(attempt),
				"TESTER_FEEDBACK":          "",
				"CRITIC_FEEDBACK":          "",
				"VERIFIER_FEEDBACK":        "",
				"LINT_FEEDBACK":            "",
				"PREVIOUS_ATTEMPT_SUMMARY": "",
				"FILES_CHANGED_SO_FAR":     "",
			},
		}, rc)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return loops.SingleOutcome{}, err
			}
			lastErr = err
			continue
		}
		return loops.SingleOutcome{Status: "done"}, nil
	}
	return loops.SingleOutcome{Status: "failed", Summary: fmt.Sprintf("%v", lastErr)}, nil
}

// Preflight runs a lightweight validity check on the task.
func (d *liveDeps) Preflight(_ context.Context, t *tasks.Task) preflight.Verdict {
	return preflight.Check(d.dir, t)
}

func (d *liveDeps) currentTaskTitle() string {
	if d.currentTask == nil {
		return ""
	}
	return d.currentTask.Title
}

func (d *liveDeps) currentTaskDescription() string {
	if d.currentTask == nil {
		return ""
	}
	return d.currentTask.Description
}

// Decompose invokes the parser in decomposition mode to break a failed task into subtasks.
// Returns ErrNoDecomposer if the parser role has no decompose_prompt configured.
// Returns (nil, ErrNoDecomposer) if the result contains 0 or >5 tasks.
func (d *liveDeps) Decompose(ctx context.Context, t *tasks.Task, fx *loops.FixResult, files []string, rc invoke.RunContext) ([]tasks.Task, error) {
	parserRole := d.inv.Specs["parser"]
	if parserRole.DecomposePrompt == "" {
		return nil, loops.ErrNoDecomposer
	}

	// Use LastFeedback as both tester and critic feedback — they are not separately
	// tracked in FixResult but the most recent failure context is sufficient.
	feedback := ""
	if fx != nil {
		feedback = fx.LastFeedback
	}

	pr, err := invoke.Role(ctx, d.inv, "parser", invoke.RoleCall{
		ArchiveRole: "parser-decompose",
		PromptPath:  parserRole.DecomposePrompt,
		ResultPath:  ".orquestalite/results/parser-decompose.json",
		Vars: map[string]string{
			"TASK_ID":                  t.ID,
			"TASK_TITLE":               t.Title,
			"TASK_DESCRIPTION":         t.Description,
			"PREVIOUS_ATTEMPT_SUMMARY": feedback,
			"FILES_CHANGED_SO_FAR":     strings.Join(files, "\n"),
			"TESTER_FEEDBACK":          feedback,
			"CRITIC_FEEDBACK":          feedback,
		}}, rc, results.ParseParser)
	if err != nil {
		return nil, fmt.Errorf("decompose parser role: %w", err)
	}

	if len(pr.Tasks) == 0 || len(pr.Tasks) > 5 {
		d.log.Log(eventlog.Event{Type: "decompose_invalid_count", Fields: map[string]any{
			"task_id": t.ID,
			"count":   len(pr.Tasks),
		}})
		return nil, loops.ErrNoDecomposer
	}

	subtasks := make([]tasks.Task, 0, len(pr.Tasks))
	for _, pt := range pr.Tasks {
		subtasks = append(subtasks, tasks.Task{
			Title:              pt.Title,
			Description:        pt.Description,
			Priority:           pt.Priority,
			DecompositionDepth: t.DecompositionDepth + 1,
			Squad:              pt.Squad,
		})
	}
	return subtasks, nil
}

// sha256OfFailures hashes a slice of test failures for repeated-failure detection.
func sha256OfFailures(fs []results.TestFailure) string {
	raw, _ := json.Marshal(fs)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func sha256Of(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// runShell executes a shell command line in the project directory with a
// timeout, returning combined output. Used to independently verify the
// tester's reported command.
func (d *liveDeps) runShell(ctx context.Context, command string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := exec.CommandContext(cctx, "sh", "-c", command)
	c.Dir = d.dir
	out, err := c.CombinedOutput()
	return string(out), err
}

// maybeCompactMemory rewrites .orquestalite/memory.md via the compactor role
// when it has grown past the configured size, keeping durable facts and
// shrinking what gets injected into every prompt. It is a no-op when no
// "compactor" role is configured or memory is still under the threshold, and
// any failure is logged and swallowed (compaction is an optimization, never a
// gate on progress).
func (d *liveDeps) maybeCompactMemory(ctx context.Context) {
	if _, ok := d.inv.Specs["compactor"]; !ok {
		return
	}
	info, err := os.Stat(d.memPath)
	if err != nil {
		return
	}
	before := int(info.Size())
	if before <= d.cfg.Limits.MemoryCompactThreshold() {
		return
	}
	r, err := invoke.Role(ctx, d.inv, "compactor",
		invoke.RoleCall{Vars: map[string]string{}},
		invoke.RunContext{TaskID: "_memory", Attempt: 1},
		results.ParseCompactor)
	if err != nil {
		d.log.Log(eventlog.Event{Type: "memory_compact_failed", Fields: map[string]any{"error": err.Error()}})
		return
	}
	next, ok := compactedMemory(before, r.Memory)
	if !ok {
		d.log.Log(eventlog.Event{Type: "memory_compact_skipped", Fields: map[string]any{
			"before_chars": before, "reason": "result not smaller",
		}})
		return
	}
	if err := writeFileAtomic(d.memPath, []byte(next)); err != nil {
		d.log.Log(eventlog.Event{Type: "memory_compact_failed", Fields: map[string]any{"error": err.Error()}})
		return
	}
	d.log.Log(eventlog.Event{Type: "memory_compacted", Fields: map[string]any{
		"before_chars": before, "after_chars": len(next), "kept_notes": r.KeptNotes,
	}})
}

// compactedMemory normalizes a compactor result and reports whether it should
// replace the existing memory: non-empty and actually smaller than before.
func compactedMemory(before int, raw string) (string, bool) {
	s := strings.TrimRight(raw, "\n")
	if strings.TrimSpace(s) == "" {
		return "", false
	}
	s += "\n"
	if len(s) >= before {
		return "", false
	}
	return s, true
}

// writeFileAtomic writes data to path via a temp file + rename so a crash
// mid-write cannot leave a half-written memory file.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// liveRoleRunner implements loops.RoleRunner using real subprocess agents.
type liveRoleRunner struct {
	deps         *liveDeps
	filesChanged []string // set by RunCoder, consumed by RunTester/RunCritic
}

// RunCoder invokes the coder role for a single fix attempt.
func (rr *liveRoleRunner) RunCoder(ctx context.Context, rc invoke.RunContext, fb loops.CoderFeedback) (loops.CoderOutcome, error) {
	d := rr.deps
	// At the start of a task's coding, shrink memory if it has grown past the
	// threshold so the (full) memory injected into this and following prompts
	// stays bounded. Self-throttles: after a compaction it stays under until it
	// regrows. Skipped on fix retries to keep the memory stable within a task.
	if rc.Attempt <= 1 {
		d.maybeCompactMemory(ctx)
	}
	r, err := invoke.Role(ctx, d.inv, "coder", invoke.RoleCall{
		AgentOverride: fb.AgentOverride,
		Vars: map[string]string{
			"TASK_ID":                  rc.TaskID,
			"TASK_TITLE":               d.currentTaskTitle(),
			"TASK_DESCRIPTION":         d.currentTaskDescription(),
			"ATTEMPT_NUMBER":           strconv.Itoa(rc.Attempt),
			"TESTER_FEEDBACK":          fb.TesterFeedback,
			"CRITIC_FEEDBACK":          fb.CriticFeedback,
			"VERIFIER_FEEDBACK":        fb.VerifierFeedback,
			"LINT_FEEDBACK":            fb.LintFeedback,
			"PREVIOUS_ATTEMPT_SUMMARY": fb.PreviousAttemptSummary,
			"FILES_CHANGED_SO_FAR":     strings.Join(fb.FilesChangedSoFar, "\n"),
		}}, rc, results.ParseCoder)
	if err != nil {
		return loops.CoderOutcome{}, err
	}

	// Store files changed so tester and critic can reference them.
	rr.filesChanged = r.FilesChanged

	return loops.CoderOutcome{Status: r.Status, Summary: r.Summary, FilesChanged: r.FilesChanged}, nil
}

// RunTester invokes the tester role after a coder attempt.
func (rr *liveRoleRunner) RunTester(ctx context.Context, rc invoke.RunContext) (loops.TesterOutcome, error) {
	d := rr.deps
	r, err := invoke.Role(ctx, d.inv, "tester", invoke.RoleCall{Vars: map[string]string{
		"TASK_ID":          rc.TaskID,
		"TASK_TITLE":       d.currentTaskTitle(),
		"TASK_DESCRIPTION": d.currentTaskDescription(),
		"ATTEMPT_NUMBER":   strconv.Itoa(rc.Attempt),
		"FILES_CHANGED":    strings.Join(rr.filesChanged, "\n"),
	}}, rc, results.ParseTester)
	if err != nil {
		return loops.TesterOutcome{}, err
	}

	var fb strings.Builder
	for _, f := range r.Failures {
		fb.WriteString(fmt.Sprintf("- %s: %s (hint: %s)\n", f.Test, f.Message, f.Hint))
	}

	// Trust-but-verify: a tester claiming "pass" on a command that actually
	// fails is the root cause of "tests pass but manual testing fails" runs.
	// Re-run the reported command ourselves; a non-zero exit overrides the pass.
	if r.Status == "pass" && d.cfg.Limits.TesterVerificationEnabled() {
		if out, err := d.runShell(ctx, r.CommandRun, d.inv.Specs["tester"].Timeout); err != nil {
			tail := runner.TailString(out, 1024)
			d.log.Log(eventlog.Event{Type: "tester_verification_failed", Fields: map[string]any{
				"task_id":     rc.TaskID,
				"command":     r.CommandRun,
				"output_tail": tail,
			}})
			feedback := fmt.Sprintf(
				"Tester reported pass, but the orchestrator re-ran %q and it failed:\n%s\n", r.CommandRun, tail)
			return loops.TesterOutcome{
				Status:       "fail",
				Feedback:     feedback,
				FailuresHash: sha256Of("verify:" + tail),
			}, nil
		}
	}

	return loops.TesterOutcome{
		Status:       r.Status,
		Feedback:     fb.String(),
		FailuresHash: sha256OfFailures(r.Failures),
	}, nil
}

// RunVerifier invokes the optional verifier role after the critic approves.
func (rr *liveRoleRunner) RunVerifier(ctx context.Context, rc invoke.RunContext) (loops.VerifierOutcome, error) {
	d := rr.deps
	r, err := invoke.Role(ctx, d.inv, "verifier", invoke.RoleCall{Vars: map[string]string{
		"TASK_ID":          rc.TaskID,
		"TASK_TITLE":       d.currentTaskTitle(),
		"TASK_DESCRIPTION": d.currentTaskDescription(),
		"ATTEMPT_NUMBER":   strconv.Itoa(rc.Attempt),
		"FILES_CHANGED":    strings.Join(rr.filesChanged, "\n"),
	}}, rc, results.ParseVerifier)
	if err != nil {
		return loops.VerifierOutcome{}, err
	}

	var fb strings.Builder
	for _, c := range r.Checks {
		if c.Passed {
			continue
		}
		fb.WriteString(fmt.Sprintf("- %s: did %s; expected %s, got %s\n", c.Name, c.Action, c.Expected, c.Actual))
	}

	return loops.VerifierOutcome{Status: r.Status, Feedback: fb.String()}, nil
}

// RunCritic invokes the critic role after the tester passes.
func (rr *liveRoleRunner) RunCritic(ctx context.Context, rc invoke.RunContext) (loops.CriticOutcome, error) {
	d := rr.deps
	r, err := invoke.Role(ctx, d.inv, "critic", invoke.RoleCall{Vars: map[string]string{
		"TASK_ID":          rc.TaskID,
		"TASK_TITLE":       d.currentTaskTitle(),
		"TASK_DESCRIPTION": d.currentTaskDescription(),
		"ATTEMPT_NUMBER":   strconv.Itoa(rc.Attempt),
		"FILES_CHANGED":    strings.Join(rr.filesChanged, "\n"),
	}}, rc, results.ParseCritic)
	if err != nil {
		return loops.CriticOutcome{}, err
	}

	var fb strings.Builder
	for _, c := range r.Concerns {
		fb.WriteString(fmt.Sprintf("- [%s] %s: %s (suggestion: %s)\n", c.Severity, c.Where, c.Issue, c.Suggestion))
	}

	return loops.CriticOutcome{Status: r.Status, Feedback: fb.String()}, nil
}
