package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/providers"
)

var orchestratedRoles = []string{"parser", "coder", "tester", "critic", "reviewer"}

// optionalRoles are resolved when declared in team.json but are not required.
// "verifier" black-box-verifies the change after the critic approves.
// "planner" extracts vertical-slice features from a plan in factory mode; it is
// not used by `run`, so a team.json without it still works for non-factory use.
var optionalRoles = []string{"verifier", "planner", "compactor"}

type Agent struct {
	Cmd                        []string `json:"cmd,omitempty"`
	Provider                   string   `json:"provider,omitempty"`
	Model                      string   `json:"model,omitempty"`
	Effort                     string   `json:"effort,omitempty"`
	DangerouslySkipPermissions bool     `json:"dangerously_skip_permissions,omitempty"`
	RateLimitPattern           string   `json:"rate_limit_pattern,omitempty"`
}

type AgentSpec struct {
	Name        string
	Provider    string
	Model       string
	Effort      string
	SkipPerms   bool
	RatePattern string
	Cmd         []string
}

type Role struct {
	Agents           []string `json:"agents"`
	Prompt           string   `json:"prompt"`
	ResultPath       string   `json:"result_path"`
	TimeoutSeconds   int      `json:"timeout_seconds"`
	EscalationLadder []string `json:"escalation_ladder,omitempty"`
	DecomposePrompt  string   `json:"decompose_prompt,omitempty"`
	// Mode applies to the verifier role only: "per_cycle" (default) verifies
	// the whole increment once per review cycle and feeds the report to the
	// reviewer; "per_task" verifies inside every fix loop; "both" does both.
	Mode string `json:"mode,omitempty"`
	// CyclePrompt is the prompt used for the per-cycle verification pass
	// (verifier role only). Falls back to Prompt when unset.
	CyclePrompt string `json:"cycle_prompt,omitempty"`
}

type RoleSpec struct {
	Agents           []AgentSpec
	PromptPath       string
	ResultPath       string
	Timeout          time.Duration
	EscalationLadder []AgentSpec
	DecomposePrompt  string
	Mode             string
	CyclePrompt      string
}

type Limits struct {
	MaxReviewCycles  int  `json:"max_review_cycles"`
	MaxFixIterations int  `json:"max_fix_iterations"`
	PreflightEnabled bool `json:"preflight_enabled,omitempty"`
	// VerifyTesterCommand re-runs the tester's command_run in the orchestrator
	// after a reported pass; a non-zero exit overrides the pass. Defaults to
	// true (nil = enabled) because a tester claiming pass on a failing command
	// is the root cause of "tests pass but manual testing fails" runs.
	VerifyTesterCommand *bool `json:"verify_tester_command,omitempty"`
	// FactoryBudgetUSD stops the factory queue before starting the next
	// feature once the recorded spend (priced via agtop) reaches this amount.
	// 0 = unlimited.
	FactoryBudgetUSD float64 `json:"factory_budget_usd,omitempty"`
	// MaxVisualRounds caps how many times a visual feature's browser
	// verification can fail and feed its findings back as tasks before the
	// feature is marked failed. 0 = use the default (2).
	MaxVisualRounds int `json:"max_visual_rounds,omitempty"`
	// ResumeSessions lets the coder resume its provider session for a task on
	// fix-loop feedback (and after a factory --resume) when the same agent runs
	// again, instead of starting the conversation from scratch. Defaults to true
	// (nil = enabled); switching to a different provider always starts fresh.
	ResumeSessions *bool `json:"resume_sessions,omitempty"`
	// MemoryCompactChars is the size (in characters) of .orquestalite/memory.md
	// above which the compactor role rewrites it into a smaller, deduplicated
	// digest, so the full memory injected into every prompt stays bounded.
	// 0 = use the default (24000 ≈ 6k tokens). Compaction only runs when a
	// "compactor" role is configured.
	MemoryCompactChars int `json:"memory_compact_chars,omitempty"`
	// MaxFeatureRetries caps how many extra times the factory re-runs a feature
	// that did not pass the merge gate before giving up and stopping the queue.
	// The no-progress guard can stop earlier. 0 = use the default (1).
	MaxFeatureRetries int `json:"max_feature_retries,omitempty"`
	// FastMode raises orchestration from task-level to feature-level batching:
	// coder implements the whole feature/task list, then tester and critic run
	// once for the batch. CLI flags can override this per invocation.
	FastMode bool `json:"fast_mode,omitempty"`
}

// MemoryCompactThreshold returns the memory size above which compaction runs,
// defaulting to 24000 characters when unset.
func (l Limits) MemoryCompactThreshold() int {
	if l.MemoryCompactChars <= 0 {
		return 24000
	}
	return l.MemoryCompactChars
}

// SessionResumeEnabled reports whether agents may resume a prior provider
// session for the same task. Enabled unless explicitly set to false.
func (l Limits) SessionResumeEnabled() bool {
	return l.ResumeSessions == nil || *l.ResumeSessions
}

// VisualRounds returns the configured cap on browser-verify feedback rounds for
// a visual feature, defaulting to 2 when unset.
func (l Limits) VisualRounds() int {
	if l.MaxVisualRounds <= 0 {
		return 2
	}
	return l.MaxVisualRounds
}

// FeatureRetries returns the extra feature-level retry budget on a merge-gate
// failure, defaulting to 1 when unset.
func (l Limits) FeatureRetries() int {
	if l.MaxFeatureRetries <= 0 {
		return 1
	}
	return l.MaxFeatureRetries
}

// TesterVerificationEnabled reports whether the orchestrator should re-run the
// tester's command itself. Enabled unless explicitly set to false.
func (l Limits) TesterVerificationEnabled() bool {
	return l.VerifyTesterCommand == nil || *l.VerifyTesterCommand
}

type RateLimitBackoff struct {
	InitialSeconds int    `json:"initial_seconds"`
	Factor         int    `json:"factor"`
	MaxSeconds     int    `json:"max_seconds"`
	DefaultPattern string `json:"default_pattern"`
}

type Config struct {
	Agents           map[string]Agent `json:"agents"`
	Roles            map[string]Role  `json:"roles"`
	Limits           Limits           `json:"limits"`
	RateLimitBackoff RateLimitBackoff `json:"rate_limit_backoff"`
	FullTestCommand  string           `json:"full_test_command"`
	// LintCommand is an optional quality gate run before the test suite after
	// each task; a non-zero exit blocks the commit (the change is rolled back),
	// so lint/format violations cannot ship. Empty = no lint gate. A missing
	// lint binary is treated as a skip, not a failure, so an unconfigured tool
	// never blocks every task.
	LintCommand string `json:"lint_command,omitempty"`
	// ConventionsFile is a project-relative path to a house-style document
	// (coding conventions, structure, idioms). When set and present, its
	// contents are injected into the coder/tester/critic/reviewer/verifier
	// prompts as {{CONVENTIONS}} so agent output matches the team's style.
	// Empty = agents infer conventions from the existing codebase instead.
	ConventionsFile string `json:"conventions_file,omitempty"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) Resolve() (map[string]RoleSpec, error) {
	if c == nil {
		return nil, fmt.Errorf("config is nil")
	}
	if len(c.Agents) == 0 {
		return nil, fmt.Errorf("no agents declared")
	}

	resolvedAgents := make(map[string]AgentSpec, len(c.Agents))
	for name, agent := range c.Agents {
		spec, err := resolveAgentSpec(name, agent)
		if err != nil {
			return nil, err
		}
		resolvedAgents[name] = spec
	}

	roles := make(map[string]RoleSpec, len(orchestratedRoles)+len(optionalRoles))
	for _, roleName := range orchestratedRoles {
		role, ok := c.Roles[roleName]
		if !ok {
			return nil, fmt.Errorf("missing orchestrated role %q", roleName)
		}
		spec, err := resolveRoleSpec(roleName, role, resolvedAgents)
		if err != nil {
			return nil, err
		}
		roles[roleName] = spec
	}
	for _, roleName := range optionalRoles {
		role, ok := c.Roles[roleName]
		if !ok {
			continue
		}
		spec, err := resolveRoleSpec(roleName, role, resolvedAgents)
		if err != nil {
			return nil, err
		}
		roles[roleName] = spec
	}

	return roles, nil
}

// HasVerifier reports whether the optional verifier role is configured.
func (c *Config) HasVerifier() bool {
	_, ok := c.Roles["verifier"]
	return ok
}

// VerifierPerTask reports whether the verifier runs inside every fix loop.
func (c *Config) VerifierPerTask() bool {
	r, ok := c.Roles["verifier"]
	return ok && (r.Mode == "per_task" || r.Mode == "both")
}

// VerifierPerCycle reports whether the verifier runs once at the end of each
// review cycle, feeding its report to the reviewer. This is the default mode.
func (c *Config) VerifierPerCycle() bool {
	r, ok := c.Roles["verifier"]
	return ok && (r.Mode == "" || r.Mode == "per_cycle" || r.Mode == "both")
}

func (c *Config) Validate() error {
	if len(c.Agents) == 0 {
		return fmt.Errorf("no agents declared")
	}
	// First pass: validate role structure and unknown agent references before
	// per-agent cmd checks, so unknown-agent errors surface before marker errors.
	for rname, r := range c.Roles {
		if len(r.Agents) == 0 {
			return fmt.Errorf("role %q has no agents", rname)
		}
		for _, a := range r.Agents {
			if _, ok := c.Agents[a]; !ok {
				return fmt.Errorf("role %q references unknown agent %q", rname, a)
			}
		}
		for _, a := range r.EscalationLadder {
			if _, ok := c.Agents[a]; !ok {
				return fmt.Errorf("role %q escalation_ladder references unknown agent %q", rname, a)
			}
		}
		if r.Prompt == "" || r.ResultPath == "" {
			return fmt.Errorf("role %q must declare prompt and result_path", rname)
		}
		if r.TimeoutSeconds <= 0 {
			return fmt.Errorf("role %q timeout_seconds must be > 0", rname)
		}
		switch r.Mode {
		case "", "per_task", "per_cycle", "both":
		default:
			return fmt.Errorf("role %q mode %q invalid (per_task|per_cycle|both)", rname, r.Mode)
		}
	}
	// Second pass: validate agent invocation shape.
	for name, a := range c.Agents {
		hasCmd := len(a.Cmd) > 0
		hasProvider := a.Provider != ""
		if hasCmd && hasProvider {
			return fmt.Errorf("agent %q cannot specify both cmd and provider", name)
		}
		if !hasCmd && !hasProvider {
			return fmt.Errorf("agent %q must declare cmd or provider", name)
		}
		if hasProvider {
			if !providers.IsKnown(a.Provider) {
				return fmt.Errorf("agent %q has unknown provider %q", name, a.Provider)
			}
			continue
		}
		hasMarker := false
		for _, tok := range a.Cmd {
			if strings.Contains(tok, "{{PROMPT}}") {
				hasMarker = true
				break
			}
		}
		if !hasMarker {
			return fmt.Errorf("agent %q cmd is missing {{PROMPT}} marker", name)
		}
	}
	if c.Limits.MaxReviewCycles <= 0 || c.Limits.MaxFixIterations <= 0 {
		return fmt.Errorf("limits must be positive")
	}
	if c.RateLimitBackoff.InitialSeconds <= 0 || c.RateLimitBackoff.Factor < 2 || c.RateLimitBackoff.MaxSeconds < c.RateLimitBackoff.InitialSeconds {
		return fmt.Errorf("invalid rate_limit_backoff")
	}
	// full_test_command may be empty: it is a verification hook, not a
	// correctness requirement. runcmd treats empty as a no-op.
	return nil
}

func resolveAgentSpec(name string, agent Agent) (AgentSpec, error) {
	if agent.Provider != "" && !providers.IsKnown(agent.Provider) {
		return AgentSpec{}, fmt.Errorf("agent %q has unknown provider %q", name, agent.Provider)
	}
	if agent.Provider == "" && len(agent.Cmd) == 0 {
		return AgentSpec{}, fmt.Errorf("agent %q must declare registered provider or cmd", name)
	}
	return AgentSpec{
		Name:        name,
		Provider:    agent.Provider,
		Model:       agent.Model,
		Effort:      agent.Effort,
		SkipPerms:   agent.DangerouslySkipPermissions,
		RatePattern: agent.RateLimitPattern,
		Cmd:         append([]string(nil), agent.Cmd...),
	}, nil
}

func resolveRoleSpec(name string, role Role, agents map[string]AgentSpec) (RoleSpec, error) {
	if role.Prompt == "" || role.ResultPath == "" {
		return RoleSpec{}, fmt.Errorf("role %q must declare prompt and result_path", name)
	}
	if role.TimeoutSeconds <= 0 {
		return RoleSpec{}, fmt.Errorf("role %q timeout_seconds must be > 0", name)
	}
	if len(role.Agents) == 0 {
		return RoleSpec{}, fmt.Errorf("role %q has no agents", name)
	}

	agentSpecs, err := resolveAgentList(name, "agents", role.Agents, agents)
	if err != nil {
		return RoleSpec{}, err
	}
	escalationSpecs, err := resolveAgentList(name, "escalation_ladder", role.EscalationLadder, agents)
	if err != nil {
		return RoleSpec{}, err
	}

	return RoleSpec{
		Agents:           agentSpecs,
		PromptPath:       role.Prompt,
		ResultPath:       role.ResultPath,
		Timeout:          time.Duration(role.TimeoutSeconds) * time.Second,
		EscalationLadder: escalationSpecs,
		DecomposePrompt:  role.DecomposePrompt,
		Mode:             role.Mode,
		CyclePrompt:      role.CyclePrompt,
	}, nil
}

func resolveAgentList(roleName, field string, names []string, agents map[string]AgentSpec) ([]AgentSpec, error) {
	specs := make([]AgentSpec, 0, len(names))
	for _, name := range names {
		spec, ok := agents[name]
		if !ok {
			if field == "escalation_ladder" {
				return nil, fmt.Errorf("role %q escalation_ladder references unknown agent %q", roleName, name)
			}
			return nil, fmt.Errorf("role %q references unknown agent %q", roleName, name)
		}
		specs = append(specs, spec)
	}
	return specs, nil
}
