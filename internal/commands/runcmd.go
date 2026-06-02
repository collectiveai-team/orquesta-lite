package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

// agentHealthThreshold is the default number of consecutive non-rate-limit
// failures after which an agent is auto-skipped for the rest of the run.
// Set to 2 because a single failure may be a transient network blip, but two
// in a row almost always means a stale model, wrong auth, or missing CLI.
const agentHealthThreshold = 2

var staticRunPreflightRoles = []string{"parser", "coder", "tester", "critic", "reviewer"}

// RunOptions holds the parameters for the run command.
type RunOptions struct {
	ProjectDir string
	TeamPath   string
	// LogFormat controls the stdout pretty format. One of eventlog.FormatVerbose
	// (default; one line per event with all fields) or eventlog.FormatHuman
	// (compact, one summary per agent transition). The JSONL file is unaffected.
	LogFormat eventlog.Format
}

// Run loads the config and tasks, wires up all components, and drives the
// review loop until it completes or the context is cancelled.
func Run(ctx context.Context, opts RunOptions) error {
	cfg, err := config.Load(opts.TeamPath)
	if err != nil {
		return err
	}
	tasksPath := filepath.Join(opts.ProjectDir, ".orquestalite", "tasks.json")
	tl, err := tasks.Load(tasksPath)
	if err != nil {
		return err
	}

	logPath := filepath.Join(opts.ProjectDir, ".orquestalite", "run.log")
	logger, err := eventlog.OpenWithFormat(logPath, os.Stdout, opts.LogFormat)
	if err != nil {
		return err
	}
	defer logger.Close()

	memPath := filepath.Join(opts.ProjectDir, ".orquestalite", "memory.md")

	fc := fallback.NewCaller(fallback.Config{
		InitialBackoff: time.Duration(cfg.RateLimitBackoff.InitialSeconds) * time.Second,
		Factor:         cfg.RateLimitBackoff.Factor,
		MaxBackoff:     time.Duration(cfg.RateLimitBackoff.MaxSeconds) * time.Second,
	})

	tracker := agenthealth.New(agentHealthThreshold)
	runStaticAgentPreflight(cfg, tracker, logger, staticRunPreflightRoles)

	deps := &liveDeps{
		cfg:          cfg,
		dir:          opts.ProjectDir,
		fc:           fc,
		log:          logger,
		tl:           tl,
		memPath:      memPath,
		tasksPath:    tasksPath,
		currentCycle: 0,
		health:       tracker,
	}
	deps.inv, err = newLiveRoleInvoker(cfg, opts.ProjectDir, memPath, fc, logger, tracker, func(role, agent string) {
		if role == "coder" && deps.currentTask != nil {
			deps.currentTask.LastAgent = agent
		}
	})
	if err != nil {
		return err
	}

	return loops.RunReviewLoop(ctx, tl, loops.ReviewConfig{MaxCycles: cfg.Limits.MaxReviewCycles}, deps)
}

func newLiveRoleInvoker(
	cfg *config.Config,
	dir string,
	memPath string,
	fc *fallback.Caller,
	log *eventlog.Logger,
	health *agenthealth.Tracker,
	onAgentSuccess func(role, agent string),
) (*invoke.RoleInvoker, error) {
	specs, err := cfg.Resolve()
	if err != nil {
		return nil, err
	}
	return &invoke.RoleInvoker{
		Specs:                   specs,
		Dir:                     dir,
		Fallback:                fc,
		Log:                     log,
		Health:                  health,
		MemPath:                 memPath,
		Runner:                  invoke.ExecRunner{},
		DefaultRateLimitPattern: cfg.RateLimitBackoff.DefaultPattern,
		AgentHealthThreshold:    agentHealthThreshold,
		OnAgentSuccess:          onAgentSuccess,
	}, nil
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
		case ag.Provider == "claude":
			binary = "claude"
		case ag.Provider == "codex":
			binary = "codex"
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
}

// RunFix sets up the current task and runs the fix loop.
func (d *liveDeps) RunFix(ctx context.Context, taskID string, rc invoke.RunContext) (*loops.FixResult, error) {
	for i, t := range d.tl.Tasks {
		if t.ID == taskID {
			d.currentTask = &d.tl.Tasks[i]
			break
		}
	}
	escalationLadder := []string{}
	if coderRole, ok := d.cfg.Roles["coder"]; ok {
		escalationLadder = coderRole.EscalationLadder
	}
	rr := &liveRoleRunner{deps: d}
	d.currentCycle = rc.Cycle
	return loops.RunFix(ctx, loops.FixConfig{
		MaxIterations:    d.cfg.Limits.MaxFixIterations,
		EscalationLadder: escalationLadder,
	}, rr, rc)
}

// FullSuite runs the full test command specified in team.json.
func (d *liveDeps) FullSuite(ctx context.Context) error {
	parts := strings.Fields(d.cfg.FullTestCommand)
	if len(parts) == 0 {
		return nil
	}
	c := exec.CommandContext(ctx, parts[0], parts[1:]...)
	c.Dir = d.dir
	out, err := c.CombinedOutput()
	if err != nil {
		d.log.Log(eventlog.Event{Type: "full_suite_failed", Fields: map[string]any{
			"output_tail": runner.TailString(string(out), 1024),
		}})
		return loops.ErrFullSuiteFailed
	}
	return nil
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

// Rollback discards uncommitted changes via git checkout + clean.
func (d *liveDeps) Rollback(ctx context.Context) error {
	return gitx.CheckoutAll(d.dir)
}

// SaveTasks persists the task list to disk.
func (d *liveDeps) SaveTasks(ctx context.Context, tl *tasks.TaskList) error {
	return tasks.Save(d.tasksPath, tl)
}

// RunParser invokes the parser role with the given plan text and returns its result.
// The parser prompt uses {{MEMORY}} and {{PLAN}} interpolation variables.
func (d *liveDeps) RunParser(ctx context.Context, plan string) (*results.ParserResult, error) {
	return invoke.Role(ctx, d.inv, "parser", map[string]string{"PLAN": plan}, invoke.RunContext{TaskID: "_plan", Attempt: 1}, results.ParseParser)
}

// RunReviewer invokes the reviewer role and returns its parsed result.
func (d *liveDeps) RunReviewer(ctx context.Context, rc invoke.RunContext) (results.ReviewerResult, error) {
	d.currentCycle = rc.Cycle
	tasksRaw, _ := os.ReadFile(d.tasksPath)

	gitLog := ""
	if _, headErr := gitx.HeadSHA(d.dir); headErr == nil {
		gitLog, _ = gitx.LogStat(d.dir, "HEAD~5")
	}

	r, err := invoke.Role(ctx, d.inv, "reviewer", map[string]string{
		"REVIEW_CYCLE": fmt.Sprintf("%d", rc.Cycle),
		"TASKS_JSON":   string(tasksRaw),
		"GIT_LOG":      gitLog,
	}, rc, results.ParseReviewer)
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

	pr, err := invoke.Role(ctx, d.inv, "parser", map[string]string{
		invoke.VarArchiveRole:      "parser-decompose",
		invoke.VarPromptPath:       parserRole.DecomposePrompt,
		invoke.VarResultPath:       ".orquestalite/results/parser-decompose.json",
		"TASK_ID":                  t.ID,
		"TASK_TITLE":               t.Title,
		"TASK_DESCRIPTION":         t.Description,
		"PREVIOUS_ATTEMPT_SUMMARY": feedback,
		"FILES_CHANGED_SO_FAR":     strings.Join(files, "\n"),
		"TESTER_FEEDBACK":          feedback,
		"CRITIC_FEEDBACK":          feedback,
	}, rc, results.ParseParser)
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
			Title:       pt.Title,
			Description: pt.Description,
			Priority:    pt.Priority,
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

// liveRoleRunner implements loops.RoleRunner using real subprocess agents.
type liveRoleRunner struct {
	deps         *liveDeps
	filesChanged []string // set by RunCoder, consumed by RunTester/RunCritic
}

// RunCoder invokes the coder role for a single fix attempt.
func (rr *liveRoleRunner) RunCoder(ctx context.Context, rc invoke.RunContext, fb loops.CoderFeedback) (loops.CoderOutcome, error) {
	d := rr.deps
	r, err := invoke.Role(ctx, d.inv, "coder", map[string]string{
		invoke.VarAgentOverride:    fb.AgentOverride,
		"TASK_ID":                  rc.TaskID,
		"TASK_TITLE":               d.currentTaskTitle(),
		"TASK_DESCRIPTION":         d.currentTaskDescription(),
		"ATTEMPT_NUMBER":           strconv.Itoa(rc.Attempt),
		"TESTER_FEEDBACK":          fb.TesterFeedback,
		"CRITIC_FEEDBACK":          fb.CriticFeedback,
		"PREVIOUS_ATTEMPT_SUMMARY": fb.PreviousAttemptSummary,
		"FILES_CHANGED_SO_FAR":     strings.Join(fb.FilesChangedSoFar, "\n"),
	}, rc, results.ParseCoder)
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
	r, err := invoke.Role(ctx, d.inv, "tester", map[string]string{
		"TASK_ID":          rc.TaskID,
		"TASK_TITLE":       d.currentTaskTitle(),
		"TASK_DESCRIPTION": d.currentTaskDescription(),
		"ATTEMPT_NUMBER":   strconv.Itoa(rc.Attempt),
		"FILES_CHANGED":    strings.Join(rr.filesChanged, "\n"),
	}, rc, results.ParseTester)
	if err != nil {
		return loops.TesterOutcome{}, err
	}

	var fb strings.Builder
	for _, f := range r.Failures {
		fb.WriteString(fmt.Sprintf("- %s: %s (hint: %s)\n", f.Test, f.Message, f.Hint))
	}

	return loops.TesterOutcome{
		Status:       r.Status,
		Feedback:     fb.String(),
		FailuresHash: sha256OfFailures(r.Failures),
	}, nil
}

// RunCritic invokes the critic role after the tester passes.
func (rr *liveRoleRunner) RunCritic(ctx context.Context, rc invoke.RunContext) (loops.CriticOutcome, error) {
	d := rr.deps
	r, err := invoke.Role(ctx, d.inv, "critic", map[string]string{
		"TASK_ID":          rc.TaskID,
		"TASK_TITLE":       d.currentTaskTitle(),
		"TASK_DESCRIPTION": d.currentTaskDescription(),
		"ATTEMPT_NUMBER":   strconv.Itoa(rc.Attempt),
		"FILES_CHANGED":    strings.Join(rr.filesChanged, "\n"),
	}, rc, results.ParseCritic)
	if err != nil {
		return loops.CriticOutcome{}, err
	}

	var fb strings.Builder
	for _, c := range r.Concerns {
		fb.WriteString(fmt.Sprintf("- [%s] %s: %s (suggestion: %s)\n", c.Severity, c.Where, c.Issue, c.Suggestion))
	}

	return loops.CriticOutcome{Status: r.Status, Feedback: fb.String()}, nil
}
