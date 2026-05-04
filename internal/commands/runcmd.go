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
	"strconv"
	"strings"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/config"
	"github.com/lionelchamorro/orquestalite/internal/eventlog"
	"github.com/lionelchamorro/orquestalite/internal/fallback"
	"github.com/lionelchamorro/orquestalite/internal/gitx"
	"github.com/lionelchamorro/orquestalite/internal/loops"
	"github.com/lionelchamorro/orquestalite/internal/memory"
	"github.com/lionelchamorro/orquestalite/internal/prompts"
	"github.com/lionelchamorro/orquestalite/internal/results"
	"github.com/lionelchamorro/orquestalite/internal/runner"
	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

// RunOptions holds the parameters for the run command.
type RunOptions struct {
	ProjectDir string
	TeamPath   string
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
	logger, err := eventlog.Open(logPath, os.Stdout)
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

	deps := &liveDeps{
		cfg:          cfg,
		dir:          opts.ProjectDir,
		fc:           fc,
		log:          logger,
		tl:           tl,
		memPath:      memPath,
		tasksPath:    tasksPath,
		currentCycle: 0,
	}

	return loops.RunReviewLoop(ctx, tl, loops.ReviewConfig{MaxCycles: cfg.Limits.MaxReviewCycles}, deps)
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
}

// RunFix sets up the current task and runs the fix loop.
func (d *liveDeps) RunFix(ctx context.Context, taskID string) (*loops.FixResult, error) {
	for i, t := range d.tl.Tasks {
		if t.ID == taskID {
			d.currentTask = &d.tl.Tasks[i]
			break
		}
	}
	rr := &liveRoleRunner{deps: d}
	return loops.RunFix(ctx, loops.FixConfig{MaxIterations: d.cfg.Limits.MaxFixIterations}, rr)
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
			"output_tail": tailString(string(out), 1024),
		}})
		return loops.ErrFullSuiteFailed
	}
	return nil
}

// Commit stages all changes and commits them with the given message.
func (d *liveDeps) Commit(ctx context.Context, msg string) (string, error) {
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
	res, _, err := d.fc.Call(ctx, role.Agents, func(ctx context.Context, agentName string) (fallback.Outcome, error) {
		ag := d.cfg.Agents[agentName]
		pattern := ag.RateLimitPattern
		if pattern == "" {
			pattern = d.cfg.RateLimitBackoff.DefaultPattern
		}
		spec := runner.Spec{
			Cmd:              ag.Cmd,
			Prompt:           prompt,
			ResultPath:       filepath.Join(d.dir, role.ResultPath),
			Timeout:          time.Duration(role.TimeoutSeconds) * time.Second,
			RateLimitPattern: pattern,
		}
		r, err := runner.RunAgent(ctx, spec)
		if err != nil {
			return fallback.Outcome{}, err
		}
		d.log.Log(eventlog.Event{Type: "agent_run", Fields: map[string]any{
			"role":          roleName,
			"agent":         agentName,
			"duration_s":    int(r.Duration.Seconds()),
			"timed_out":     r.TimedOut,
			"rate_limited":  r.RateLimited,
			"result_exists": r.ResultExists,
		}})
		return fallback.Outcome{
			RateLimited:  r.RateLimited,
			ResultExists: r.ResultExists,
			TimedOut:     r.TimedOut,
		}, nil
	})
	if errors.Is(err, fallback.ErrRateLimitExhausted) {
		return err
	}
	if err != nil {
		return err
	}
	if !res.ResultExists {
		return fmt.Errorf("agent %q did not write result file for role %q", roleName, roleName)
	}
	return nil
}

// tailString returns the last n bytes of s.
func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
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
func (rr *liveRoleRunner) RunCoder(ctx context.Context, attempt int, testerFB, criticFB string) (loops.CoderOutcome, error) {
	d := rr.deps
	role := d.cfg.Roles["coder"]
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
		"MEMORY":           mem,
		"TASK_ID":          taskID,
		"TASK_TITLE":       taskTitle,
		"TASK_DESCRIPTION": taskDesc,
		"ATTEMPT_NUMBER":   strconv.Itoa(attempt),
		"TESTER_FEEDBACK":  testerFB,
		"CRITIC_FEEDBACK":  criticFB,
	})

	if err := d.callRole(ctx, "coder", prompt, role); err != nil {
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

	return loops.CoderOutcome{Status: r.Status, Summary: r.Summary}, nil
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
