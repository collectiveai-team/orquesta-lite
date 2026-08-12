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
	"github.com/lionelchamorro/orquestalite/internal/artifacts"
	"github.com/lionelchamorro/orquestalite/internal/config"
	"github.com/lionelchamorro/orquestalite/internal/cost"
	"github.com/lionelchamorro/orquestalite/internal/eventlog"
	"github.com/lionelchamorro/orquestalite/internal/fallback"
	"github.com/lionelchamorro/orquestalite/internal/gitx"
	"github.com/lionelchamorro/orquestalite/internal/memory"
	"github.com/lionelchamorro/orquestalite/internal/prompts"
	"github.com/lionelchamorro/orquestalite/internal/providers"
	"github.com/lionelchamorro/orquestalite/internal/runner"
	"github.com/lionelchamorro/orquestalite/internal/skills"
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
	// SkillsDir is a project-relative (or absolute) directory of project-defined
	// skill markdown files (see internal/skills). When a role call requests
	// skills, the invoker loads this directory and injects each requested
	// skill's content as {{SKILLS}}. Empty defaults to "skills". A requested
	// skill that is not on disk is a clear, immediate error.
	SkillsDir string
	// AgentEnv, when non-empty, is the environment handed to every agent
	// subprocess instead of inheriting this process's. It carries the
	// context-optimization settings (proxy address, filter binary on PATH) so
	// they apply per project rather than leaking from the launching shell. Built
	// once per run by internal/contextopt.
	AgentEnv []string
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
	// BestEffortRoles names roles whose outcome must never gate progress (e.g.
	// the memory "compactor"). Their failures are excluded from the shared,
	// per-agent health tracker so a best-effort hiccup cannot bench the very
	// providers that the critical roles (coder/tester/critic) depend on. Without
	// this, a compactor that exits 0 without rewriting its result file (because
	// the digest is already compact) reads as result_missing, trips the circuit
	// breaker, and skips its agents for the rest of the run.
	BestEffortRoles map[string]bool
	// Artifacts, when set, persists the full prompt + stdout + stderr + meta
	// of every agent invocation under .orquestalite/runs/<run_id>/agents/. Nil
	// disables persistence (compat for callers that do not need it).
	Artifacts *artifacts.Store
	// CodeWritingRoles is the set of roles whose work mutates the work tree and
	// therefore should have a per-attempt diff captured into the artifacts dir
	// (attempt.diff) and surfaced as an agent_diff event. Defaults to coder and
	// generalist when nil.
	CodeWritingRoles map[string]bool
}

type RoleCall struct {
	AgentOverride string
	ArchiveRole   string
	PromptPath    string
	ResultPath    string
	Vars          map[string]string
	// Skills names the project-defined skills to inject as {{SKILLS}} for this
	// call (typically the current task's requested skills). Empty means no
	// skills were requested and a placeholder is injected. A name absent from
	// the skills/ directory is a clear, immediate error.
	Skills []string
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
	skillsText, err := inv.skillsFor(call.Skills)
	if err != nil {
		return err
	}
	roleVars["SKILLS"] = skillsText

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
	_, err := inv.runValidated(ctx, roleName, role, agentOverride, prompt, relResultPath, absResultPath, rc, nil)
	return err
}

// contractRetryBudget bounds the extra same-agent attempts runValidated makes
// after a result is missing entirely or fails schema validation. Each
// corrective attempt resumes the agent's own just-failed session with a
// targeted prompt ("write the JSON to <path>") instead of re-running the
// whole task from scratch — the agent typically already did the real work
// and only skipped emitting the artifact. Intentionally a constant, not a
// per-role config field: it is a cost-guard against a genuinely broken
// agent/prompt looping forever, not a knob any team.json needs to tune.
const contractRetryBudget = 2

// correctivePrompt builds the retry prompt for a same-agent corrective
// attempt. reason is "invalid_contract" or "result_missing" — the only two
// fallback reasons that route through this retry (see runValidated).
func correctivePrompt(roleName, absResultPath, reason string, validationErr error) string {
	if reason == "invalid_contract" {
		return fmt.Sprintf(
			"Your previous attempt wrote a result file to %s but it failed schema validation.\n"+
				"Validation error: %v\n\n"+
				"Do NOT redo any completed work. Fix the JSON at %s so it satisfies the "+
				"required schema for role %q, then write the corrected JSON to exactly that path.",
			absResultPath, validationErr, absResultPath, roleName)
	}
	return fmt.Sprintf(
		"Your previous attempt for role %q appears to have completed the required work but "+
			"did not write the result file.\n\n"+
			"Do NOT redo any completed work. Write your result as valid JSON to exactly: %s",
		roleName, absResultPath)
}

// runValidated runs the fallback chain and considers a result successful only
// after validate accepts it. This keeps invalid_contract inside the same
// per-agent fallback loop as missing results and provider failures.
//
// runValidated drives the fallback chain for one role invocation and reports
// the total spend it incurred. The spend covers *every* agent the chain
// touched, not just the one that finally succeeded: a fallback that burned two
// providers before landing cost real money, and a chain that failed outright
// cost the most of all. Callers hand that number to the runtime so a policy's
// maxCostUSD is a brake on what was actually spent.
func (inv *RoleInvoker) runValidated(ctx context.Context, roleName string, role config.RoleSpec, agentOverride, prompt, relResultPath, absResultPath string, rc RunContext, validate func(path string) error) (float64, error) {
	var spend float64
	agents, err := selectAgents(role, agentOverride)
	if err != nil {
		return spend, err
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
			return spend, fmt.Errorf("all agents for role %q are marked skipped: %v", roleName, inv.Health.SkippedAgents())
		}
	}

	var lastResult *runner.Result
	var lastErr error
	var lastValidationErr error
	var lastFallbackReason string
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
			SafeMode:                   ag.SafeMode,
			Prompt:                     prompt,
			ResultPath:                 absResultPath,
			Timeout:                    role.Timeout,
			RateLimitPattern:           pattern,
			TemplateVars: map[string]string{
				"RESULT_PATH": absResultPath,
				"ROLE":        roleName,
			},
			Env: inv.AgentEnv,
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
		spend += runSpendUSD(ag.Model, r)

		shouldFallback, fallbackReason := Classify(r)
		if !shouldFallback && validate != nil {
			if validationErr := validate(absResultPath); validationErr != nil {
				shouldFallback = true
				fallbackReason = "invalid_contract"
				lastValidationErr = validationErr
				lastErr = fmt.Errorf("agent %q (role %q) wrote invalid contract to %s: %w", agentName, roleName, relResultPath, validationErr)
			}
		}
		if shouldFallback {
			lastFallbackReason = fallbackReason
			if fallbackReason != "invalid_contract" {
				lastErr = fmt.Errorf("agent %q (role %q) did not write %s: exit=%d; detail: %s",
					agentName, roleName, relResultPath, r.ExitCode, errorDetail(r))
			}
		}

		artifactsDir := inv.saveArtifacts(roleName, agentName, ag, spec, prompt, r, rc)
		inv.logAgentRun(roleName, agentName, ag, spec, r, fallbackReason, rc, artifactsDir)

		// A missing/invalid result is often a cheap-to-fix slip, not a genuine
		// failure: the benchmark evidence motivating this loop showed agents
		// that had already done the real work (tests passing) but skipped
		// emitting the artifact, or wrote a result one field short of the
		// schema. Give the SAME agent a bounded number of extra, corrective
		// shots — resuming its own just-failed session with a targeted prompt
		// — before accepting the failure and letting fc.Call fall through to
		// the next agent (or terminate). rate_limit/timeout/auth_failed never
		// enter this loop; they already have correct handling below/in
		// fallback.Caller and must not be retried this way.
		isBestEffort := inv.BestEffortRoles[roleName]
		for attempt := 0; attempt < contractRetryBudget && !isBestEffort && shouldFallback &&
			(fallbackReason == "invalid_contract" || fallbackReason == "result_missing"); attempt++ {
			cSpec := spec
			cSpec.Prompt = correctivePrompt(roleName, absResultPath, fallbackReason, lastValidationErr)
			cSpec.ResumeSessionID = r.SessionID // ephemeral: the attempt that just failed

			cr, cErr := inv.runner().Run(ctx, cSpec)
			if cErr != nil {
				return fallback.Outcome{}, cErr
			}
			r = cr
			lastResult = cr

			shouldFallback, fallbackReason = Classify(cr)
			if !shouldFallback && validate != nil {
				if validationErr := validate(absResultPath); validationErr != nil {
					shouldFallback = true
					fallbackReason = "invalid_contract"
					lastValidationErr = validationErr
					lastErr = fmt.Errorf("agent %q (role %q) wrote invalid contract to %s: %w", agentName, roleName, relResultPath, validationErr)
				}
			}
			if shouldFallback {
				lastFallbackReason = fallbackReason
				if fallbackReason != "invalid_contract" {
					lastErr = fmt.Errorf("agent %q (role %q) did not write %s: exit=%d; detail: %s",
						agentName, roleName, relResultPath, cr.ExitCode, errorDetail(cr))
				}
			}

			crc := rc
			crc.Attempt = rc.Attempt + attempt + 1
			cArtifactsDir := inv.saveArtifacts(roleName, agentName, ag, cSpec, cSpec.Prompt, cr, crc)
			inv.logAgentRun(roleName, agentName, ag, cSpec, cr, fallbackReason, crc, cArtifactsDir)
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
		message := fmt.Sprintf("all agents failed for role %q: tried [%s]; last error: %s", roleName, tried, lastErrStr)
		if lastFallbackReason == "invalid_contract" {
			return spend, fmt.Errorf("%w: %s", ErrInvalidContract, message)
		}
		if lastFallbackReason == "timeout" {
			return spend, fmt.Errorf("%w: %s", ErrAgentTimeout, message)
		}
		return spend, fmt.Errorf("%s", message)
	}
	return spend, err
}

// runSpendUSD prices one agent invocation from the token usage its provider
// adapter reported. An unpriced or unknown model contributes 0 rather than a
// guess — the number feeds a hard budget check, so it must never be inflated.
func runSpendUSD(model string, result *runner.Result) float64 {
	if result == nil {
		return 0
	}
	usage := usageTotals(result)
	if usage.Input == 0 && usage.Output == 0 {
		return 0
	}
	usd, ok := cost.EstimateUSD(model, usage.Input, usage.Output)
	if !ok {
		return 0
	}
	return usd
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
	// Best-effort roles never gate progress, so they must not move the shared
	// per-agent health counters in either direction: a failure must not bench
	// the agent, and a success must not mask a critical role's failure streak.
	if inv.BestEffortRoles[roleName] {
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

func (inv *RoleInvoker) logAgentRun(roleName, agentName string, ag config.AgentSpec, spec runner.Spec, r *runner.Result, fallbackReason string, rc RunContext, artifactsDir string) {
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
	if artifactsDir != "" {
		fields["artifacts_dir"] = artifactsDir
	}
	usage := usageTotals(r)
	if usage.Input > 0 {
		fields["input_tokens"] = usage.Input
	}
	if usage.Output > 0 {
		fields["output_tokens"] = usage.Output
	}
	if usage.CachedInput > 0 {
		fields["cached_input_tokens"] = usage.CachedInput
	}
	if usage.Reasoning > 0 {
		fields["reasoning_tokens"] = usage.Reasoning
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

// writesCode reports whether roleName is one whose output mutates the work tree.
func (inv *RoleInvoker) writesCode(roleName string) bool {
	if inv.CodeWritingRoles == nil {
		return roleName == "coder" || roleName == "generalist"
	}
	return inv.CodeWritingRoles[roleName]
}

// captureDiff returns the accumulated work-tree diff for this run against the
// cycle base (when known) — covering committed tasks in the cycle plus the
// current pending changes — falling back to the pending work-tree diff vs HEAD.
// "" on a clean tree or non-repo.
func (inv *RoleInvoker) captureDiff(rc RunContext) (string, error) {
	if rc.CycleBaseSHA != "" {
		return gitx.DiffRefs(inv.Dir, rc.CycleBaseSHA, "HEAD")
	}
	return gitx.DiffWorktree(inv.Dir)
}

// diffStats summarises a `git diff` blob into file count, insertions, deletions.
func diffStats(diff string) (files, insertions, deletions int) {
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ ") && !strings.HasPrefix(line, "+++ /dev/null"):
			files++
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			insertions++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			deletions++
		}
	}
	return
}

// saveArtifacts persists the full prompt + stdout + stderr + meta.json for one
// agent invocation and returns the project-relative artifacts_dir (for the
// agent_run event) — "" when artifacts are disabled. Failures are best-effort:
// a missing dir is non-fatal so trace artifacts never gate the run.
func (inv *RoleInvoker) saveArtifacts(roleName, agentName string, ag config.AgentSpec, spec runner.Spec, prompt string, r *runner.Result, rc RunContext) string {
	if inv.Artifacts == nil {
		return ""
	}
	dir, err := inv.Artifacts.Dir(rc.TaskID, roleName, rc.Cycle, rc.Attempt)
	if err != nil || dir == "" {
		return ""
	}
	cmdLine := redactedCmdLine(ag.Cmd, spec.TemplateVars)
	invocation := artifacts.Invocation{
		Prompt:    prompt,
		Stdout:    r.Stdout,
		Stderr:    r.Stderr,
		Agent:     agentName,
		Model:     ag.Model,
		Provider:  ag.Provider,
		DurationS: int(r.Duration.Seconds()),
		ExitCode:  r.ExitCode,
		SessionID: r.SessionID,
		CmdLine:   cmdLine,
	}
	if sErr := inv.Artifacts.SaveInvocation(dir, invocation); sErr != nil {
		// Best-effort: log to stderr but never fail the run over trace capture.
		fmt.Fprintf(os.Stderr, "warning: artifacts save %s: %v\n", dir, sErr)
		return ""
	}
	relDir := artifacts.RelativeDir(dir, inv.Dir)
	// Code-writing roles get a per-attempt diff captured alongside the prompt/
	// stdout/stderr so the exact change an attempt made is recoverable. An
	// agent_diff event lets the dashboard link the diff to the invocation.
	if inv.writesCode(roleName) && inv.Log != nil {
		if diff, dErr := inv.captureDiff(rc); dErr == nil && diff != "" {
			_ = os.WriteFile(filepath.Join(dir, "attempt.diff"), []byte(diff), 0o644)
			files, ins, del := diffStats(diff)
			inv.Log.Log(eventlog.Event{Type: "agent_diff", Fields: map[string]any{
				"task_id":       rc.TaskID,
				"role":          roleName,
				"attempt":       rc.Attempt,
				"cycle":         rc.Cycle,
				"files_changed": files,
				"insertions":    ins,
				"deletions":     del,
				"artifacts_dir": relDir,
			}})
		}
	}
	return relDir
}

func absPath(dir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(dir, path)
}

// skillsFor renders the {{SKILLS}} injection for the requested skill names.
// Empty/nil names yield a placeholder (and read no files). Non-empty names load
// the skills/ directory fresh and render each requested skill; a name that is
// not on disk is a clear, immediate error so a typo in a plan cannot silently
// drop the working style the agent was supposed to follow.
func (inv *RoleInvoker) skillsFor(names []string) (string, error) {
	if len(names) == 0 {
		return "(no skills requested for this task)", nil
	}
	dir := inv.SkillsDir
	if dir == "" {
		dir = skills.DefaultDir
	}
	reg, err := skills.Load(absPath(inv.Dir, dir))
	if err != nil {
		return "", fmt.Errorf("load skills: %w", err)
	}
	return reg.Render(names)
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

type usageSummary struct {
	Input       int
	Output      int
	CachedInput int
	Reasoning   int
}

func usageTotals(res *runner.Result) usageSummary {
	var out usageSummary
	for _, ev := range res.Events {
		if ev.Type != providers.EventUsage {
			continue
		}
		out.Input += ev.Usage["input_tokens"]
		out.Output += ev.Usage["output_tokens"]
		out.CachedInput += ev.Usage["cached_input_tokens"]
		out.CachedInput += ev.Usage["cache_read_tokens"]
		out.CachedInput += ev.Usage["cache_creation_input_tokens"]
		out.CachedInput += ev.Usage["cache_write_tokens"]
		out.Reasoning += ev.Usage["reasoning_tokens"]
	}
	out.Input += out.CachedInput
	return out
}
