package invoke

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/agenthealth"
	"github.com/lionelchamorro/orquestalite/internal/config"
	"github.com/lionelchamorro/orquestalite/internal/eventlog"
	"github.com/lionelchamorro/orquestalite/internal/fallback"
	"github.com/lionelchamorro/orquestalite/internal/memory"
	"github.com/lionelchamorro/orquestalite/internal/prompts"
	"github.com/lionelchamorro/orquestalite/internal/providers"
	"github.com/lionelchamorro/orquestalite/internal/results"
	"github.com/lionelchamorro/orquestalite/internal/runner"
)

type AgentRunner interface {
	Run(ctx context.Context, s runner.Spec) (*runner.Result, error)
}

// SessionStore records and recalls a provider session id per (task, role,
// agent) so a re-invocation of the same agent on the same task can resume the
// conversation. Implemented by internal/sessions.Store.
type SessionStore interface {
	Get(task, role, agent string) string
	Set(task, role, agent, id string) error
	Delete(task, role, agent string) error
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, s runner.Spec) (*runner.Result, error) {
	return runner.RunAgent(ctx, s)
}

type RoleInvoker struct {
	Specs                   map[string]config.RoleSpec
	Dir                     string
	Fallback                *fallback.Caller
	Log                     *eventlog.Logger
	Health                  *agenthealth.Tracker
	MemPath                 string
	Runner                  AgentRunner
	DefaultRateLimitPattern string
	AgentHealthThreshold    int
	OnAgentSuccess          func(role, agent string)
	// ConventionsPath is a project-relative path to a house-style document.
	// When set and readable, its contents are injected into every role prompt
	// as {{CONVENTIONS}}. Read fresh per call so edits take effect mid-run.
	ConventionsPath string
	// Sessions, when set, records each successful run's provider session id and
	// supplies it back to resume the conversation when the same agent runs again
	// on the same task. Nil disables session tracking entirely.
	Sessions SessionStore
	// ResumeRoles is the set of roles allowed to resume a prior session. Empty
	// or nil means no role resumes (tracking still happens for all roles).
	ResumeRoles map[string]bool
	// SessionNamespace scopes session keys (e.g. the factory feature ID "F002")
	// so identical per-feature task IDs do not resume each other's sessions.
	// The composed key has the form "<namespace>/<taskID>" (e.g. "F002/T001").
	// Empty (non-factory run) leaves keys as the bare task ID.
	SessionNamespace string
}

type RoleCall struct {
	AgentOverride string
	ArchiveRole   string
	PromptPath    string
	ResultPath    string
	Vars          map[string]string
}

func Role[T MemoryNoting](
	ctx context.Context,
	inv *RoleInvoker,
	roleName string,
	call RoleCall,
	rc RunContext,
	parse func(path string) (*T, error),
) (*T, error) {
	if inv == nil {
		return nil, fmt.Errorf("role invoker is nil")
	}
	spec, ok := inv.Specs[roleName]
	if !ok {
		return nil, fmt.Errorf("role %q is not configured", roleName)
	}

	roleVars := call.templateVars()
	promptPath := call.PromptPath
	if promptPath == "" {
		promptPath = spec.PromptPath
	}
	resultPath := call.ResultPath
	if resultPath == "" {
		resultPath = spec.ResultPath
	}
	archiveRole := call.ArchiveRole
	if archiveRole == "" {
		archiveRole = roleName
	}

	mem, _ := memory.ReadAll(inv.MemPath)
	roleVars["MEMORY"] = mem
	roleVars["CONVENTIONS"] = inv.readConventions()

	tmpl, err := prompts.Load(absPath(inv.Dir, promptPath))
	if err != nil {
		return nil, err
	}
	prompt := prompts.Interpolate(tmpl, roleVars)
	resultAbs := absPath(inv.Dir, resultPath)

	if err := inv.run(ctx, roleName, spec, call.AgentOverride, prompt, resultPath, resultAbs, rc); err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(resultAbs)
	if err != nil {
		return nil, fmt.Errorf("read role result %s: %w", resultAbs, err)
	}
	if err := results.Archive(inv.Dir, archiveRole, rc.TaskID, rc.Cycle, rc.Attempt, raw); err != nil {
		return nil, err
	}

	parsed, err := parse(resultAbs)
	if err != nil {
		return nil, err
	}
	if note := (*parsed).MemoryNote(); note != nil {
		taskID := rc.TaskID
		if taskID == "" {
			taskID = "-"
		}
		_ = memory.Append(inv.MemPath, memory.Entry{
			Cycle:  rc.Cycle,
			TaskID: taskID,
			Role:   roleName,
			Body:   *note,
		})
	}
	return parsed, nil
}

// RunOnce invokes a role exactly once, building the prompt from the spec and the
// provided RoleCall vars (memory and conventions are injected automatically). It
// returns nil when the agent wrote a result file, otherwise an error (including
// fallback.ErrAllAgentsFailed when every configured agent produced no result).
// Callers that need bounded retry should loop themselves; RunOnce itself does
// not retry.
func (inv *RoleInvoker) RunOnce(ctx context.Context, roleName string, call RoleCall, rc RunContext) error {
	if inv == nil {
		return fmt.Errorf("role invoker is nil")
	}
	spec, ok := inv.Specs[roleName]
	if !ok {
		return fmt.Errorf("role %q is not configured", roleName)
	}

	roleVars := call.templateVars()
	promptPath := call.PromptPath
	if promptPath == "" {
		promptPath = spec.PromptPath
	}
	resultPath := call.ResultPath
	if resultPath == "" {
		resultPath = spec.ResultPath
	}

	mem, _ := memory.ReadAll(inv.MemPath)
	roleVars["MEMORY"] = mem
	roleVars["CONVENTIONS"] = inv.readConventions()

	tmpl, err := prompts.Load(absPath(inv.Dir, promptPath))
	if err != nil {
		return err
	}
	prompt := prompts.Interpolate(tmpl, roleVars)
	resultAbs := absPath(inv.Dir, resultPath)

	return inv.run(ctx, roleName, spec, call.AgentOverride, prompt, resultPath, resultAbs, rc)
}

// readConventions returns the house-style document injected as {{CONVENTIONS}},
// or a placeholder telling the agent to infer conventions from the codebase
// when none is configured or the file is missing/empty.
func (inv *RoleInvoker) readConventions() string {
	const fallback = "(no project conventions file configured — infer the house style from the surrounding code and mirror it)"
	if inv.ConventionsPath == "" {
		return fallback
	}
	raw, err := os.ReadFile(absPath(inv.Dir, inv.ConventionsPath))
	if err != nil {
		return fallback
	}
	if text := strings.TrimSpace(string(raw)); text != "" {
		return text
	}
	return fallback
}

func (call RoleCall) templateVars() map[string]string {
	out := make(map[string]string, len(call.Vars)+1)
	for k, v := range call.Vars {
		out[k] = v
	}
	return out
}

func (inv *RoleInvoker) run(ctx context.Context, roleName string, role config.RoleSpec, agentOverride, prompt, relResultPath, absResultPath string, rc RunContext) error {
	agents, err := selectAgents(role, agentOverride)
	if err != nil {
		return err
	}
	agentByName := make(map[string]config.AgentSpec, len(agents))
	chain := make([]string, 0, len(agents))
	for _, ag := range agents {
		agentByName[ag.Name] = ag
		chain = append(chain, ag.Name)
	}

	if inv.Health != nil {
		chain = inv.Health.Filter(chain)
		if len(chain) == 0 {
			return fmt.Errorf("all agents for role %q are marked skipped: %v", roleName, inv.Health.SkippedAgents())
		}
	}

	var lastResult *runner.Result
	var lastErr error
	var triedAgents []string
	fc := inv.Fallback
	if fc == nil {
		fc = fallback.NewCaller(fallback.Config{})
	}

	_, _, err = fc.Call(ctx, chain, func(ctx context.Context, agentName string) (fallback.Outcome, error) {
		// An agent skipped earlier in this same Call (e.g. an auth failure marks
		// it immediately) must not be re-spawned — fall straight through without
		// burning another invocation. The chain is filtered only at Call start,
		// so this guard catches skips that happen mid-loop.
		if inv.Health != nil {
			if reason, skipped := inv.Health.IsSkipped(agentName); skipped {
				return fallback.Outcome{ShouldFallback: true, FallbackReason: string(reason)}, nil
			}
		}

		ag := agentByName[agentName]
		pattern := ag.RatePattern
		if pattern == "" {
			pattern = inv.DefaultRateLimitPattern
		}
		spec := runner.Spec{
			Cmd:                        ag.Cmd,
			Provider:                   ag.Provider,
			Model:                      ag.Model,
			Effort:                     ag.Effort,
			DangerouslySkipPermissions: ag.SkipPerms,
			Prompt:                     prompt,
			ResultPath:                 absResultPath,
			Timeout:                    role.Timeout,
			RateLimitPattern:           pattern,
			TemplateVars: map[string]string{
				"RESULT_PATH": absResultPath,
				"ROLE":        roleName,
			},
		}
		// Resume this agent's prior conversation for the task when one exists.
		// Only the same agent on the same task resumes; a fallback to a different
		// agent finds no entry and starts fresh (the desired "switch provider →
		// from scratch" behaviour).
		spec.ResumeSessionID = inv.resumeSessionID(roleName, agentName, rc.TaskID)
		r, err := inv.runner().Run(ctx, spec)
		if err != nil {
			return fallback.Outcome{}, err
		}
		lastResult = r
		triedAgents = append(triedAgents, agentName)

		shouldFallback, fallbackReason := Classify(r)
		if shouldFallback {
			lastErr = fmt.Errorf("agent %q (role %q) did not write %s: exit=%d; detail: %s",
				agentName, roleName, relResultPath, r.ExitCode, errorDetail(r))
		}

		inv.recordHealth(roleName, agentName, shouldFallback, fallbackReason)
		if !shouldFallback && inv.OnAgentSuccess != nil {
			inv.OnAgentSuccess(roleName, agentName)
		}
		// Record the session of a successful run so the next invocation of this
		// same agent on this same task can resume it. On a non-rate-limit failure
		// of a resumed run, drop the stored session so a stale/expired id is not
		// retried on every subsequent attempt.
		if inv.Sessions != nil && inv.ResumeRoles[roleName] && rc.TaskID != "" {
			key := inv.sessionTaskKey(rc.TaskID)
			switch {
			case !shouldFallback:
				_ = inv.Sessions.Set(key, roleName, agentName, r.SessionID)
			case spec.ResumeSessionID != "" && !r.RateLimited:
				_ = inv.Sessions.Delete(key, roleName, agentName)
			}
		}
		inv.logAgentRun(roleName, agentName, ag, spec, r, fallbackReason, rc)

		out := fallback.Outcome{
			RateLimited:    r.RateLimited,
			ResultExists:   r.ResultExists,
			TimedOut:       r.TimedOut,
			ShouldFallback: shouldFallback,
			FallbackReason: fallbackReason,
		}
		// When rate-limited, mine the agent's output for a reset hint
		// ("try again at 4:30 PM") so the fallback loop can wait until the real
		// reset instead of guessing with exponential backoff.
		if r.RateLimited {
			if reset, ok := fallback.ParseResetTime(r.Stdout+"\n"+r.Stderr, time.Now()); ok {
				out.ResetAt = reset
			}
		}
		return out, nil
	})

	if errors.Is(err, fallback.ErrAllAgentsFailed) {
		tried := strings.Join(triedAgents, ", ")
		lastErrStr := ""
		if lastErr != nil {
			lastErrStr = lastErr.Error()
		} else if lastResult != nil {
			lastErrStr = fmt.Sprintf("exit=%d; detail: %s", lastResult.ExitCode, errorDetail(lastResult))
		}
		return fmt.Errorf("all agents failed for role %q: tried [%s]; last error: %s", roleName, tried, lastErrStr)
	}
	return err
}

func (inv *RoleInvoker) runner() AgentRunner {
	if inv.Runner != nil {
		return inv.Runner
	}
	return ExecRunner{}
}

// sessionTaskKey scopes a task ID by the current SessionNamespace so the session
// store never collides identical task IDs across features. Empty namespace
// returns the task ID unchanged (non-factory runs are unaffected).
func (inv *RoleInvoker) sessionTaskKey(taskID string) string {
	if inv.SessionNamespace == "" {
		return taskID
	}
	return inv.SessionNamespace + "/" + taskID
}

// resumeSessionID returns the stored session for (task, role, agent) when
// session resume is enabled for the role, else "".
func (inv *RoleInvoker) resumeSessionID(role, agent, taskID string) string {
	if inv.Sessions == nil || taskID == "" || !inv.ResumeRoles[role] {
		return ""
	}
	return inv.Sessions.Get(inv.sessionTaskKey(taskID), role, agent)
}

func (inv *RoleInvoker) recordHealth(roleName, agentName string, shouldFallback bool, fallbackReason string) {
	if inv.Health == nil {
		return
	}
	switch {
	case !shouldFallback:
		inv.Health.MarkSuccess(agentName)
	case fallbackReason == "auth_failed":
		// An interactive auth prompt will not fix itself this session — skip the
		// agent immediately rather than burning the full failure threshold.
		if _, already := inv.Health.IsSkipped(agentName); !already {
			inv.Health.Skip(agentName, agenthealth.ReasonAuth)
			if inv.Log != nil {
				inv.Log.Log(eventlog.Event{Type: "agent_marked_skipped", Fields: map[string]any{
					"agent":                agentName,
					"role":                 roleName,
					"reason":               "auth",
					"last_fallback_reason": fallbackReason,
				}})
			}
		}
	case fallbackReason != "rate_limit":
		if inv.Health.MarkFailure(agentName, agenthealth.ReasonResultMissing) && inv.Log != nil {
			threshold := inv.AgentHealthThreshold
			if threshold == 0 {
				threshold = 2
			}
			inv.Log.Log(eventlog.Event{Type: "agent_marked_skipped", Fields: map[string]any{
				"agent":                agentName,
				"role":                 roleName,
				"reason":               "consecutive_failures",
				"threshold":            threshold,
				"last_fallback_reason": fallbackReason,
			}})
		}
	}
}

func (inv *RoleInvoker) logAgentRun(roleName, agentName string, ag config.AgentSpec, spec runner.Spec, r *runner.Result, fallbackReason string, rc RunContext) {
	if inv.Log == nil {
		return
	}
	fields := map[string]any{
		"role":             roleName,
		"agent":            agentName,
		"task_id":          rc.TaskID,
		"cycle":            rc.Cycle,
		"attempt":          rc.Attempt,
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
	if spec.ResumeSessionID != "" {
		fields["resumed"] = true
		fields["resumed_from"] = spec.ResumeSessionID
	}
	if cmdLine := redactedCmdLine(ag.Cmd, spec.TemplateVars); cmdLine != "" {
		fields["cmd_line"] = cmdLine
	}
	if r.CodexHeader != nil {
		fields["codex_header"] = r.CodexHeader
	}
	inv.Log.Log(eventlog.Event{Type: "agent_run", Fields: fields})
}

func selectAgents(role config.RoleSpec, override string) ([]config.AgentSpec, error) {
	if override == "" {
		return role.Agents, nil
	}
	for _, ag := range append(append([]config.AgentSpec{}, role.Agents...), role.EscalationLadder...) {
		if ag.Name == override {
			return []config.AgentSpec{ag}, nil
		}
	}
	return nil, fmt.Errorf("agent override %q is not configured for role", override)
}

func absPath(dir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(dir, path)
}

func redactedCmdLine(cmd []string, vars map[string]string) string {
	if len(cmd) == 0 {
		return ""
	}
	parts := make([]string, len(cmd))
	for i, tok := range cmd {
		s := strings.ReplaceAll(tok, "{{PROMPT}}", "<elided>")
		for k, v := range vars {
			s = strings.ReplaceAll(s, "{{"+k+"}}", v)
		}
		parts[i] = s
	}
	return strings.Join(parts, " ")
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
