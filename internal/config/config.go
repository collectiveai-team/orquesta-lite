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

type Agent struct {
	Cmd                        []string `json:"cmd,omitempty"`
	Provider                   string   `json:"provider,omitempty"`
	Model                      string   `json:"model,omitempty"`
	Effort                     string   `json:"effort,omitempty"`
	DangerouslySkipPermissions bool     `json:"dangerously_skip_permissions,omitempty"`
	RateLimitPattern           string   `json:"rate_limit_pattern,omitempty"`
}

type AgentSpec struct {
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
}

type RoleSpec struct {
	Agents           []AgentSpec
	PromptPath       string
	ResultPath       string
	Timeout          time.Duration
	EscalationLadder []AgentSpec
	DecomposePrompt  string
}

type Limits struct {
	MaxReviewCycles  int  `json:"max_review_cycles"`
	MaxFixIterations int  `json:"max_fix_iterations"`
	PreflightEnabled bool `json:"preflight_enabled,omitempty"`
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

	roles := make(map[string]RoleSpec, len(orchestratedRoles))
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

	return roles, nil
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
