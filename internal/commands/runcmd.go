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
	"github.com/lionelchamorro/orquestalite/internal/loops"
	"github.com/lionelchamorro/orquestalite/internal/memory"
	"github.com/lionelchamorro/orquestalite/internal/preflight"
	"github.com/lionelchamorro/orquestalite/internal/prompts"
	"github.com/lionelchamorro/orquestalite/internal/providers"
	"github.com/lionelchamorro/orquestalite/internal/results"
	"github.com/lionelchamorro/orquestalite/internal/runner"
	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

// agentHealthThreshold is the default number of consecutive non-rate-limit
// failures after which an agent is auto-skipped for the rest of the run.
// Set to 2 because a single failure may be a transient network blip, but two
// in a row almost always means a stale model, wrong auth, or missing CLI.
const agentHealthThreshold = 2

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
	runStaticAgentPreflight(cfg, tracker, logger)

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

	return loops.RunReviewLoop(ctx, tl, loops.ReviewConfig{MaxCycles: cfg.Limits.MaxReviewCycles}, deps)
}

// runStaticAgentPreflight verifies that each agent referenced by any role
// either declares a provider that orq-lite knows about (claude, codex) and
// whose CLI is on PATH, OR a cmd whose first token is on PATH. Agents that
// fail the check are marked skipped in the tracker so the fallback caller
// can move past them without burning a real invocation. Never blocks the run
// — the goal is to surface obvious misconfiguration cheaply.
func runStaticAgentPreflight(cfg *config.Config, tracker *agenthealth.Tracker, log *eventlog.Logger) {
	used := map[string]bool{}
	for _, role := range cfg.Roles {
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
}

// RunFix sets up the current task and runs the fix loop.
func (d *liveDeps) RunFix(ctx context.Context, taskID string) (*loops.FixResult, error) {
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
	return loops.RunFix(ctx, loops.FixConfig{
		MaxIterations:    d.cfg.Limits.MaxFixIterations,
		EscalationLadder: escalationLadder,
	}, rr)
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
	role := d.cfg.Roles["parser"]
	tmpl, err := prompts.Load(filepath.Join(d.dir, role.Prompt))
	if err != nil {
		return nil, err
	}
	mem, _ := memory.ReadAll(d.memPath)

	prompt := prompts.Interpolate(tmpl, map[string]string{
		"MEMORY": mem,
		"PLAN":   plan,
	})
	if err := d.callRole(ctx, "parser", prompt, role); err != nil {
		return nil, err
	}
	return results.ParseParser(filepath.Join(d.dir, role.ResultPath))
}

// RunReviewer invokes the reviewer role and returns its parsed result.
func (d *liveDeps) RunReviewer(ctx context.Context, cycle int) (results.ReviewerResult, error) {
	d.currentCycle = cycle
	role := d.cfg.Roles["reviewer"]
	tmpl, err := prompts.Load(filepath.Join(d.dir, role.Prompt))
	if err != nil {
		return results.ReviewerResult{}, err
	}
	mem, _ := memory.ReadAll(d.memPath)
	tasksRaw, _ := os.ReadFile(d.tasksPath)

	gitLog := ""
	if _, headErr := gitx.HeadSHA(d.dir); headErr == nil {
		gitLog, _ = gitx.LogStat(d.dir, "HEAD~5")
	}

	prompt := prompts.Interpolate(tmpl, map[string]string{
		"REVIEW_CYCLE": fmt.Sprintf("%d", cycle),
		"MEMORY":       mem,
		"TASKS_JSON":   string(tasksRaw),
		"GIT_LOG":      gitLog,
	})
	if err := d.callRole(ctx, "reviewer", prompt, role); err != nil {
		return results.ReviewerResult{}, err
	}
	r, err := results.ParseReviewer(filepath.Join(d.dir, role.ResultPath))
	if err != nil {
		return results.ReviewerResult{}, err
	}
	if r.NotesForMemory != nil {
		_ = memory.Append(d.memPath, memory.Entry{
			Cycle:  cycle,
			TaskID: "-",
			Role:   "reviewer",
			Body:   *r.NotesForMemory,
		})
	}
	return *r, nil
}

// callRole drives a single role invocation through the fallback chain.
func (d *liveDeps) callRole(ctx context.Context, roleName, prompt string, role config.Role) error {
	var lastResult *runner.Result
	var lastErr error
	// triedAgents tracks agent names in attempt order for error reporting.
	var triedAgents []string

	// Skip agents that previous calls (or the static preflight) marked unhealthy.
	chain := role.Agents
	if d.health != nil {
		chain = d.health.Filter(role.Agents)
		if len(chain) == 0 {
			// All declared agents are skipped: surface the skipped set in the
			// error so the operator can fix team.json without grepping logs.
			skipped := d.health.SkippedAgents()
			return fmt.Errorf("all agents for role %q are marked skipped: %v", roleName, skipped)
		}
	}

	_, _, err := d.fc.Call(ctx, chain, func(ctx context.Context, agentName string) (fallback.Outcome, error) {
		ag := d.cfg.Agents[agentName]
		pattern := ag.RateLimitPattern
		if pattern == "" {
			pattern = d.cfg.RateLimitBackoff.DefaultPattern
		}
		// Canonical Phase-3 template vars: RESULT_PATH and ROLE.
		// Additional vars can be added here as new codex flags are introduced.
		spec := runner.Spec{
			Cmd:                        ag.Cmd,
			Provider:                   ag.Provider,
			Model:                      ag.Model,
			Effort:                     ag.Effort,
			DangerouslySkipPermissions: ag.DangerouslySkipPermissions,
			Prompt:                     prompt,
			ResultPath:                 filepath.Join(d.dir, role.ResultPath),
			Timeout:                    time.Duration(role.TimeoutSeconds) * time.Second,
			RateLimitPattern:           pattern,
			TemplateVars: map[string]string{
				"RESULT_PATH": filepath.Join(d.dir, role.ResultPath),
				"ROLE":        roleName,
			},
		}
		r, err := runner.RunAgent(ctx, spec)
		if err != nil {
			return fallback.Outcome{}, err
		}
		lastResult = r
		triedAgents = append(triedAgents, agentName)

		// Build cmd_line with {{PROMPT}} replaced by <elided> and all other
		// TemplateVars substituted so the logged cmd reflects the actual invocation.
		cmdLine := ""
		if len(ag.Cmd) > 0 {
			parts := make([]string, len(ag.Cmd))
			for i, tok := range ag.Cmd {
				s := strings.ReplaceAll(tok, "{{PROMPT}}", "<elided>")
				for k, v := range spec.TemplateVars {
					s = strings.ReplaceAll(s, "{{"+k+"}}", v)
				}
				parts[i] = s
			}
			cmdLine = strings.Join(parts, " ")
		}

		// Determine fallback disposition.
		// Precedence: rate_limit → timed_out (agent_crashed) → result_missing → success.
		// TimedOut must be checked before !ResultExists because a timed-out agent
		// always has ResultExists=false; checking !ResultExists first would shadow
		// the more-specific "agent_crashed" reason.
		var shouldFallback bool
		var fallbackReason string
		switch {
		case r.RateLimited:
			shouldFallback = true
			fallbackReason = "rate_limit"
		case r.TimedOut:
			shouldFallback = true
			fallbackReason = "agent_crashed"
		case !r.ResultExists:
			shouldFallback = true
			fallbackReason = "result_missing"
		// "invalid_contract" is reserved for Phase 3 contract validation.
		default:
			shouldFallback = false
		}

		// Record last non-success error for reporting.
		if shouldFallback {
			lastErr = fmt.Errorf("agent %q (role %q) did not write %s: exit=%d; detail: %s",
				agentName, roleName, role.ResultPath, r.ExitCode, errorDetail(r))
		}

		// Update agent health. Rate-limit failures already have a backoff path
		// in the fallback caller, so they are excluded from the failure count
		// — being slow is not the same as being broken.
		if d.health != nil {
			switch {
			case !shouldFallback:
				d.health.MarkSuccess(agentName)
			case fallbackReason != "rate_limit":
				if d.health.MarkFailure(agentName, agenthealth.ReasonResultMissing) {
					d.log.Log(eventlog.Event{Type: "agent_marked_skipped", Fields: map[string]any{
						"agent":                agentName,
						"role":                 roleName,
						"reason":               "consecutive_failures",
						"threshold":            agentHealthThreshold,
						"last_fallback_reason": fallbackReason,
					}})
				}
			}
		}

		// Record the agent that actually produced the coder result on the current
		// task. This shows up in `orq-lite status` so the operator can see which
		// model worked without grepping run.log.
		if !shouldFallback && roleName == "coder" && d.currentTask != nil {
			d.currentTask.LastAgent = agentName
		}

		fields := map[string]any{
			"role":             roleName,
			"agent":            agentName,
			"provider":         ag.Provider,
			"model":            ag.Model,
			"duration_s":       int(r.Duration.Seconds()),
			"timed_out":        r.TimedOut,
			"rate_limited":     r.RateLimited,
			"result_exists":    r.ResultExists,
			"exit_code":        r.ExitCode,
			"session_id":       r.SessionID,
			"final_text_tail":  runner.TailString(r.FinalText, 1000),
			"tool_calls_count": toolCallsCount(r),
			"stderr_tail":      r.StderrTail(2048),
			"stdout_tail":      r.StdoutTail(2048),
			"fallback_reason":  fallbackReason,
		}
		if cmdLine != "" {
			fields["cmd_line"] = cmdLine
		}
		if r.CodexHeader != nil {
			fields["codex_header"] = r.CodexHeader
		}
		d.log.Log(eventlog.Event{Type: "agent_run", Fields: fields})

		return fallback.Outcome{
			RateLimited:    r.RateLimited,
			ResultExists:   r.ResultExists,
			TimedOut:       r.TimedOut,
			ShouldFallback: shouldFallback,
			FallbackReason: fallbackReason,
		}, nil
	})

	if errors.Is(err, fallback.ErrAllAgentsFailed) {
		tried := strings.Join(triedAgents, ", ")
		lastErrStr := ""
		if lastErr != nil {
			lastErrStr = lastErr.Error()
		} else if lastResult != nil {
			lastErrStr = fmt.Sprintf("exit=%d; detail: %s", lastResult.ExitCode, errorDetail(lastResult))
		}
		return fmt.Errorf("all agents failed for role %q: tried [%s]; last error: %s",
			roleName, tried, lastErrStr)
	}
	if errors.Is(err, fallback.ErrRateLimitExhausted) {
		return err
	}
	if err != nil {
		return err
	}
	return nil
}

func errorDetail(res *runner.Result) string {
	if strings.TrimSpace(res.Stderr) != "" {
		return res.StderrTail(2048)
	}
	if strings.TrimSpace(res.FinalText) != "" {
		return runner.TailString(res.FinalText, 2048)
	}
	return lastNonEmptyLines(res.Stdout, 20)
}

func lastNonEmptyLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	kept := make([]string, 0, n)
	for i := len(lines) - 1; i >= 0 && len(kept) < n; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		kept = append(kept, line)
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	return strings.Join(kept, "\n")
}

func toolCallsCount(res *runner.Result) int {
	count := 0
	for _, ev := range res.Events {
		if ev.Type == providers.EventToolCall {
			count++
		}
	}
	return count
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

// Decompose invokes the parser in decomposition mode to break a failed task into subtasks.
// Returns ErrNoDecomposer if the parser role has no decompose_prompt configured.
// Returns (nil, ErrNoDecomposer) if the result contains 0 or >5 tasks.
func (d *liveDeps) Decompose(ctx context.Context, t *tasks.Task, fx *loops.FixResult, files []string) ([]tasks.Task, error) {
	parserRole := d.cfg.Roles["parser"]
	if parserRole.DecomposePrompt == "" {
		return nil, loops.ErrNoDecomposer
	}

	tmpl, err := prompts.Load(filepath.Join(d.dir, parserRole.DecomposePrompt))
	if err != nil {
		return nil, loops.ErrNoDecomposer
	}

	mem, _ := memory.ReadAll(d.memPath)

	// Use LastFeedback as both tester and critic feedback — they are not separately
	// tracked in FixResult but the most recent failure context is sufficient.
	feedback := ""
	if fx != nil {
		feedback = fx.LastFeedback
	}

	prompt := prompts.Interpolate(tmpl, map[string]string{
		"MEMORY":                   mem,
		"TASK_ID":                  t.ID,
		"TASK_TITLE":               t.Title,
		"TASK_DESCRIPTION":         t.Description,
		"PREVIOUS_ATTEMPT_SUMMARY": feedback,
		"FILES_CHANGED_SO_FAR":     strings.Join(files, "\n"),
		"TESTER_FEEDBACK":          feedback,
		"CRITIC_FEEDBACK":          feedback,
	})

	// Invoke the parser role agents but write to a separate result file so that
	// any in-flight parser.json is not overwritten.
	decomposeResultPath := filepath.Join(d.dir, ".orquestalite", "results", "parser-decompose.json")
	decomposeRole := parserRole
	decomposeRole.ResultPath = ".orquestalite/results/parser-decompose.json"

	if err := d.callRole(ctx, "parser", prompt, decomposeRole); err != nil {
		return nil, fmt.Errorf("decompose callRole: %w", err)
	}

	pr, err := results.ParseParser(decomposeResultPath)
	if err != nil {
		return nil, fmt.Errorf("decompose parse result: %w", err)
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
func (rr *liveRoleRunner) RunCoder(ctx context.Context, attempt int, fb loops.CoderFeedback) (loops.CoderOutcome, error) {
	d := rr.deps
	role := d.cfg.Roles["coder"]

	// When AgentOverride is set, route through a single-agent chain instead of
	// the default chain so the escalated agent is used exclusively.
	effectiveAgents := role.Agents
	if fb.AgentOverride != "" {
		effectiveAgents = []string{fb.AgentOverride}
	}
	effectiveRole := role
	effectiveRole.Agents = effectiveAgents

	tmpl, err := prompts.Load(filepath.Join(d.dir, role.Prompt))
	if err != nil {
		return loops.CoderOutcome{}, err
	}
	mem, _ := memory.ReadAll(d.memPath)

	taskID := ""
	taskTitle := ""
	taskDesc := ""
	if d.currentTask != nil {
		taskID = d.currentTask.ID
		taskTitle = d.currentTask.Title
		taskDesc = d.currentTask.Description
	}

	prompt := prompts.Interpolate(tmpl, map[string]string{
		"MEMORY":                   mem,
		"TASK_ID":                  taskID,
		"TASK_TITLE":               taskTitle,
		"TASK_DESCRIPTION":         taskDesc,
		"ATTEMPT_NUMBER":           strconv.Itoa(attempt),
		"TESTER_FEEDBACK":          fb.TesterFeedback,
		"CRITIC_FEEDBACK":          fb.CriticFeedback,
		"PREVIOUS_ATTEMPT_SUMMARY": fb.PreviousAttemptSummary,
		"FILES_CHANGED_SO_FAR":     strings.Join(fb.FilesChangedSoFar, "\n"),
	})

	if err := d.callRole(ctx, "coder", prompt, effectiveRole); err != nil {
		return loops.CoderOutcome{}, err
	}

	r, err := results.ParseCoder(filepath.Join(d.dir, role.ResultPath))
	if err != nil {
		return loops.CoderOutcome{}, err
	}

	if r.NotesForMemory != nil {
		_ = memory.Append(d.memPath, memory.Entry{
			Cycle:  d.currentCycle,
			TaskID: taskID,
			Role:   "coder",
			Body:   *r.NotesForMemory,
		})
	}

	// Store files changed so tester and critic can reference them.
	rr.filesChanged = r.FilesChanged

	return loops.CoderOutcome{Status: r.Status, Summary: r.Summary, FilesChanged: r.FilesChanged}, nil
}

// RunTester invokes the tester role after a coder attempt.
func (rr *liveRoleRunner) RunTester(ctx context.Context, attempt int) (loops.TesterOutcome, error) {
	d := rr.deps
	role := d.cfg.Roles["tester"]
	tmpl, err := prompts.Load(filepath.Join(d.dir, role.Prompt))
	if err != nil {
		return loops.TesterOutcome{}, err
	}
	mem, _ := memory.ReadAll(d.memPath)

	taskTitle := ""
	taskDesc := ""
	taskID := ""
	if d.currentTask != nil {
		taskID = d.currentTask.ID
		taskTitle = d.currentTask.Title
		taskDesc = d.currentTask.Description
	}

	filesChanged := strings.Join(rr.filesChanged, "\n")

	prompt := prompts.Interpolate(tmpl, map[string]string{
		"MEMORY":           mem,
		"TASK_ID":          taskID,
		"TASK_TITLE":       taskTitle,
		"TASK_DESCRIPTION": taskDesc,
		"ATTEMPT_NUMBER":   strconv.Itoa(attempt),
		"FILES_CHANGED":    filesChanged,
	})

	if err := d.callRole(ctx, "tester", prompt, role); err != nil {
		return loops.TesterOutcome{}, err
	}

	r, err := results.ParseTester(filepath.Join(d.dir, role.ResultPath))
	if err != nil {
		return loops.TesterOutcome{}, err
	}

	if r.NotesForMemory != nil {
		_ = memory.Append(d.memPath, memory.Entry{
			Cycle:  d.currentCycle,
			TaskID: taskID,
			Role:   "tester",
			Body:   *r.NotesForMemory,
		})
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
func (rr *liveRoleRunner) RunCritic(ctx context.Context, attempt int) (loops.CriticOutcome, error) {
	d := rr.deps
	role := d.cfg.Roles["critic"]
	tmpl, err := prompts.Load(filepath.Join(d.dir, role.Prompt))
	if err != nil {
		return loops.CriticOutcome{}, err
	}
	mem, _ := memory.ReadAll(d.memPath)

	taskTitle := ""
	taskDesc := ""
	taskID := ""
	if d.currentTask != nil {
		taskID = d.currentTask.ID
		taskTitle = d.currentTask.Title
		taskDesc = d.currentTask.Description
	}

	filesChanged := strings.Join(rr.filesChanged, "\n")

	prompt := prompts.Interpolate(tmpl, map[string]string{
		"MEMORY":           mem,
		"TASK_ID":          taskID,
		"TASK_TITLE":       taskTitle,
		"TASK_DESCRIPTION": taskDesc,
		"ATTEMPT_NUMBER":   strconv.Itoa(attempt),
		"FILES_CHANGED":    filesChanged,
	})

	if err := d.callRole(ctx, "critic", prompt, role); err != nil {
		return loops.CriticOutcome{}, err
	}

	r, err := results.ParseCritic(filepath.Join(d.dir, role.ResultPath))
	if err != nil {
		return loops.CriticOutcome{}, err
	}

	if r.NotesForMemory != nil {
		_ = memory.Append(d.memPath, memory.Entry{
			Cycle:  d.currentCycle,
			TaskID: taskID,
			Role:   "critic",
			Body:   *r.NotesForMemory,
		})
	}

	var fb strings.Builder
	for _, c := range r.Concerns {
		fb.WriteString(fmt.Sprintf("- [%s] %s: %s (suggestion: %s)\n", c.Severity, c.Where, c.Issue, c.Suggestion))
	}

	return loops.CriticOutcome{Status: r.Status, Feedback: fb.String()}, nil
}
