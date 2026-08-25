package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/providers"
)

type Agent struct {
	Cmd      []string `json:"cmd,omitempty"`
	Provider string   `json:"provider,omitempty"`
	// UsageProvider associates a custom command with the subscription it
	// consumes. Registered providers infer this automatically; wrappers that
	// ultimately launch Claude or Codex must declare it explicitly.
	UsageProvider              string   `json:"usage_provider,omitempty"`
	Model                      string   `json:"model,omitempty"`
	Effort                     string   `json:"effort,omitempty"`
	DangerouslySkipPermissions bool     `json:"dangerously_skip_permissions,omitempty"`
	SafeMode                   bool     `json:"safe_mode,omitempty"`
	ExtraArgs                  []string `json:"extra_args,omitempty"`
	RateLimitPattern           string   `json:"rate_limit_pattern,omitempty"`
}

type AgentSpec struct {
	Name          string
	Provider      string
	UsageProvider string
	Model         string
	Effort        string
	SkipPerms     bool
	SafeMode      bool
	ExtraArgs     []string
	RatePattern   string
	Cmd           []string
}

type Role struct {
	Agents           []string `json:"agents"`
	Prompt           string   `json:"prompt"`
	ResultPath       string   `json:"result_path"`
	TimeoutSeconds   int      `json:"timeout_seconds"`
	EscalationLadder []string `json:"escalation_ladder,omitempty"`
}

type RoleSpec struct {
	Agents           []AgentSpec
	PromptPath       string
	ResultPath       string
	Timeout          time.Duration
	EscalationLadder []AgentSpec
}

type Limits struct {
	// ResumeSessions lets a role resume its provider session for the same
	// durable scope. Switching providers always starts a fresh session.
	ResumeSessions *bool `json:"resume_sessions,omitempty"`
	// UsageGuard prevents an invocation from consuming a provider subscription
	// past the configured usage thresholds. It is disabled when Providers is
	// empty, preserving the historical behaviour for existing projects.
	UsageGuard UsageGuard `json:"usage_guard,omitempty"`
}

// UsageGuard configures the pre-invocation provider subscription check. The
// providers currently supported by the local readers are "claude" and
// "codex". Thresholds use the provider windows "5h" and "7d" and express
// used (not remaining) percentage.
type UsageGuard struct {
	CacheTTLSeconds int `json:"cache_ttl_seconds,omitempty"`
	// OnUnavailable controls an unreadable provider usage source: "fallback"
	// (the safe default) advances to the next configured agent, while "allow"
	// runs the agent without a reading.
	OnUnavailable string                         `json:"on_unavailable,omitempty"`
	Providers     map[string]UsageProviderBudget `json:"providers,omitempty"`
}

type UsageProviderBudget struct {
	MaxUsedPercent map[string]float64 `json:"max_used_percent"`
}

// SessionResumeEnabled reports whether agents may resume a prior provider
// session for the same task. Enabled unless explicitly set to false.
func (l Limits) SessionResumeEnabled() bool {
	return l.ResumeSessions == nil || *l.ResumeSessions
}

// UsageGuardEnabled reports whether at least one provider has a configured
// usage threshold.
func (l Limits) UsageGuardEnabled() bool { return len(l.UsageGuard.Providers) > 0 }

type RateLimitBackoff struct {
	InitialSeconds int    `json:"initial_seconds"`
	Factor         int    `json:"factor"`
	MaxSeconds     int    `json:"max_seconds"`
	DefaultPattern string `json:"default_pattern"`
}

type Runtime struct {
	RetentionRuns    int              `json:"retention_runs,omitempty"`
	ArtifactMaxBytes int64            `json:"artifact_max_bytes,omitempty"`
	ProviderBackoff  RateLimitBackoff `json:"provider_backoff,omitempty"`
	// ContextOptimization configures the external tools that shrink what an
	// agent invocation carries. Both default to enabled-when-available: a
	// missing tool degrades to an unoptimized run and never fails one.
	ContextOptimization ContextOptimization `json:"context_optimization,omitempty"`
}

// ContextOptimization holds the two measured context-reduction tools. Neither
// is vendored with orq-lite, so "enabled" means "used when present" — see
// GUIDE.md for the install steps and `orq-lite doctor` for what is active.
type ContextOptimization struct {
	// CompressionProxy compresses request bodies — chiefly the tool schemas
	// declared on every invocation — between the agent and the provider API.
	// Measured at −38% cost on an end-to-end benchmark run.
	CompressionProxy CompressionProxy `json:"compression_proxy,omitempty"`
	// CommandFilter rewrites the agent's shell commands so verbose output is
	// filtered before it becomes a tool result. Measured at −25% cost.
	CommandFilter CommandFilter `json:"command_filter,omitempty"`
}

type CompressionProxy struct {
	// Enabled defaults to true when omitted. A pointer so "absent" and
	// "false" stay distinguishable.
	Enabled *bool `json:"enabled,omitempty"`
	// URL is where the proxy listens. orq-lite does not start or supervise
	// the daemon; it probes this address and uses it when reachable.
	URL string `json:"url,omitempty"`
}

type CommandFilter struct {
	// Enabled defaults to true when omitted.
	Enabled *bool `json:"enabled,omitempty"`
	// Binary is resolved on PATH, or used as-is when it contains a separator.
	// The hook rewrites commands to `<binary> <cmd>` with no path, so the
	// binary must be reachable by name in the agent subprocess.
	Binary string `json:"binary,omitempty"`
}

// DefaultProxyURL is probed when a project enables the proxy without naming one.
const DefaultProxyURL = "http://127.0.0.1:8787"

// DefaultFilterBinary is resolved when a project enables the filter without
// naming a binary.
const DefaultFilterBinary = "rtk"

func enabled(flag *bool) bool { return flag == nil || *flag }

// ProxyEnabled reports whether the compression proxy should be used. Omitting
// the block enables it.
func (c ContextOptimization) ProxyEnabled() bool { return enabled(c.CompressionProxy.Enabled) }

// FilterEnabled reports whether the command filter should be used. Omitting
// the block enables it.
func (c ContextOptimization) FilterEnabled() bool { return enabled(c.CommandFilter.Enabled) }

// ProxyURL returns the configured proxy address or the default.
func (c ContextOptimization) ProxyURL() string {
	if c.CompressionProxy.URL == "" {
		return DefaultProxyURL
	}
	return c.CompressionProxy.URL
}

// FilterBinary returns the configured filter binary or the default.
func (c ContextOptimization) FilterBinary() string {
	if c.CommandFilter.Binary == "" {
		return DefaultFilterBinary
	}
	return c.CommandFilter.Binary
}

func (r Runtime) RetentionCeiling() int {
	if r.RetentionRuns <= 0 {
		return 20
	}
	return r.RetentionRuns
}

func (r Runtime) ArtifactLimit() int64 {
	if r.ArtifactMaxBytes <= 0 {
		return 8 << 20
	}
	return r.ArtifactMaxBytes
}

type Config struct {
	Agents           map[string]Agent `json:"agents"`
	Roles            map[string]Role  `json:"roles"`
	Limits           Limits           `json:"limits"`
	RateLimitBackoff RateLimitBackoff `json:"rate_limit_backoff"`
	Runtime          Runtime          `json:"runtime,omitempty"`
	// LintArgv and TestArgv are argv gates exposed through the read-only
	// `config.` namespace. Flows get argv rather than shell strings because
	// `allowShell` is false by policy: a gate the engine runs directly cannot
	// smuggle in a pipeline or a redirect.
	//
	// A project that ships flows using `config.lint_argv` / `config.test_argv`
	// and does not declare these cannot start a run at all — the pre-run
	// validator rejects it. That is why `orq-lite init` scaffolds both and
	// `doctor` reports on them: the failure has to be visible from the
	// commands an adopter runs before their first flow, not only from the run
	// itself.
	LintArgv []string `json:"lint_argv,omitempty"`
	TestArgv []string `json:"test_argv,omitempty"`
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

// LoadDynamic decodes team configuration for a compiled v2 flow. Validation is
// deferred to ResolveRoles so unrelated roles cannot block
// a flow that does not reference them.
func LoadDynamic(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var config Config
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &config, nil
}

func (c *Config) Resolve() (map[string]RoleSpec, error) {
	return c.ResolveAll()
}

// ResolveAll resolves exactly the roles declared by configuration without
// imposing a hardcoded role set. Durable flows validate only roles referenced
// by their compiled IR.
func (c *Config) ResolveAll() (map[string]RoleSpec, error) {
	if c == nil {
		return nil, fmt.Errorf("config is nil")
	}
	names := make([]string, 0, len(c.Roles))
	for name := range c.Roles {
		names = append(names, name)
	}
	return c.ResolveRoles(names)
}

// ResolveRoles resolves only names referenced by one compiled workflow IR.
func (c *Config) ResolveRoles(names []string) (map[string]RoleSpec, error) {
	if c == nil {
		return nil, fmt.Errorf("config is nil")
	}
	resolved := make(map[string]RoleSpec, len(names))
	for _, name := range names {
		if _, done := resolved[name]; done {
			continue
		}
		role, ok := c.Roles[name]
		if !ok {
			return nil, fmt.Errorf("missing referenced role %q", name)
		}
		agents := make(map[string]AgentSpec, len(role.Agents)+len(role.EscalationLadder))
		for _, agentName := range append(append([]string(nil), role.Agents...), role.EscalationLadder...) {
			agent, exists := c.Agents[agentName]
			if !exists {
				return nil, fmt.Errorf("role %q references unknown agent %q", name, agentName)
			}
			spec, err := resolveAgentSpec(agentName, agent)
			if err != nil {
				return nil, err
			}
			agents[agentName] = spec
		}
		spec, err := resolveRoleSpec(name, role, agents)
		if err != nil {
			return nil, err
		}
		resolved[name] = spec
	}
	return resolved, nil
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
	}
	// Second pass: validate agent invocation shape.
	for name, a := range c.Agents {
		if err := validateAgentInvocation(name, a); err != nil {
			return err
		}
	}
	if c.RateLimitBackoff.InitialSeconds <= 0 || c.RateLimitBackoff.Factor < 2 || c.RateLimitBackoff.MaxSeconds < c.RateLimitBackoff.InitialSeconds {
		return fmt.Errorf("invalid rate_limit_backoff")
	}
	if err := ValidateUsageGuard(c.Limits.UsageGuard); err != nil {
		return err
	}
	return nil
}

// ValidateUsageGuard validates the part of team configuration consumed before
// agent execution. It is exported because dynamic flow configuration defers
// general validation until it knows which roles are referenced.
func ValidateUsageGuard(guard UsageGuard) error {
	if len(guard.Providers) == 0 {
		return nil
	}
	if guard.CacheTTLSeconds < 0 {
		return fmt.Errorf("limits.usage_guard.cache_ttl_seconds must be >= 0")
	}
	if guard.OnUnavailable != "" && guard.OnUnavailable != "fallback" && guard.OnUnavailable != "allow" {
		return fmt.Errorf("limits.usage_guard.on_unavailable must be fallback or allow")
	}
	for provider, budget := range guard.Providers {
		if provider != "claude" && provider != "codex" {
			return fmt.Errorf("limits.usage_guard has unsupported provider %q", provider)
		}
		if len(budget.MaxUsedPercent) == 0 {
			return fmt.Errorf("limits.usage_guard provider %q must configure max_used_percent", provider)
		}
		for window, percent := range budget.MaxUsedPercent {
			if window != "5h" && window != "7d" {
				return fmt.Errorf("limits.usage_guard provider %q has unsupported window %q (use 5h or 7d)", provider, window)
			}
			if percent <= 0 || percent > 100 {
				return fmt.Errorf("limits.usage_guard provider %q window %q max_used_percent must be in (0, 100]", provider, window)
			}
		}
	}
	return nil
}

func resolveAgentSpec(name string, agent Agent) (AgentSpec, error) {
	if err := validateAgentInvocation(name, agent); err != nil {
		return AgentSpec{}, err
	}
	return AgentSpec{
		Name:          name,
		Provider:      agent.Provider,
		UsageProvider: agentUsageProvider(agent),
		Model:         agent.Model,
		Effort:        agent.Effort,
		SkipPerms:     agent.DangerouslySkipPermissions,
		SafeMode:      agent.SafeMode,
		ExtraArgs:     append([]string(nil), agent.ExtraArgs...),
		RatePattern:   agent.RateLimitPattern,
		Cmd:           append([]string(nil), agent.Cmd...),
	}, nil
}

func agentUsageProvider(agent Agent) string {
	if agent.Provider != "" {
		return agent.Provider
	}
	if agent.UsageProvider != "" {
		return agent.UsageProvider
	}
	if len(agent.Cmd) > 0 {
		binary := strings.TrimSuffix(strings.ToLower(filepath.Base(agent.Cmd[0])), ".exe")
		if binary == "claude" || binary == "codex" {
			return binary
		}
	}
	return ""
}

func validateAgentInvocation(name string, agent Agent) error {
	hasCmd := len(agent.Cmd) > 0
	hasProvider := agent.Provider != ""
	if hasCmd && hasProvider {
		return fmt.Errorf("agent %q cannot specify both cmd and provider", name)
	}
	if !hasCmd && !hasProvider {
		return fmt.Errorf("agent %q must declare cmd or provider", name)
	}
	if hasProvider && agent.UsageProvider != "" {
		return fmt.Errorf("agent %q cannot specify usage_provider with provider; it is inferred", name)
	}
	if agent.UsageProvider != "" && agent.UsageProvider != "claude" && agent.UsageProvider != "codex" {
		return fmt.Errorf("agent %q has unsupported usage_provider %q", name, agent.UsageProvider)
	}
	if len(agent.ExtraArgs) > 0 && !hasProvider {
		return fmt.Errorf("agent %q extra_args requires provider", name)
	}
	if hasProvider {
		if !providers.IsKnown(agent.Provider) {
			return fmt.Errorf("agent %q has unknown provider %q", name, agent.Provider)
		}
		return nil
	}
	for _, tok := range agent.Cmd {
		if strings.Contains(tok, "{{PROMPT}}") {
			return nil
		}
	}
	return fmt.Errorf("agent %q cmd is missing {{PROMPT}} marker", name)
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
