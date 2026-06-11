package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTeamJSON(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "team.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad_Valid(t *testing.T) {
	p := writeTeamJSON(t, `{
		"agents": {
			"a1": {"cmd": ["claude", "-p", "{{PROMPT}}"], "rate_limit_pattern": "rate_?limit"}
		},
		"roles": {
			"coder": {"agents": ["a1"], "prompt": "prompts/coder.md", "result_path": ".orquestalite/results/coder.json", "timeout_seconds": 900}
		},
		"limits": {"max_review_cycles": 3, "max_fix_iterations": 5},
		"rate_limit_backoff": {"initial_seconds": 30, "factor": 2, "max_seconds": 1800, "default_pattern": "rate_?limit|429"},
		"full_test_command": "go test ./..."
	}`)

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Limits.MaxFixIterations != 5 {
		t.Errorf("MaxFixIterations = %d, want 5", cfg.Limits.MaxFixIterations)
	}
	if got := cfg.Roles["coder"].Agents[0]; got != "a1" {
		t.Errorf("coder.agents[0] = %q, want %q", got, "a1")
	}
}

func TestLoad_UnknownAgentInRoleFails(t *testing.T) {
	p := writeTeamJSON(t, `{
		"agents": {"a1": {"cmd": ["x"]}},
		"roles": {"coder": {"agents": ["a1", "ghost"], "prompt": "p.md", "result_path": "r.json", "timeout_seconds": 1}},
		"limits": {"max_review_cycles": 1, "max_fix_iterations": 1},
		"rate_limit_backoff": {"initial_seconds": 1, "factor": 2, "max_seconds": 2, "default_pattern": "x"},
		"full_test_command": "true"
	}`)

	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("expected error mentioning unknown agent 'ghost', got: %v", err)
	}
}

func TestLoad_PromptInCmdMustContainMarker(t *testing.T) {
	p := writeTeamJSON(t, `{
		"agents": {"a1": {"cmd": ["claude", "-p", "no-marker"]}},
		"roles": {"coder": {"agents": ["a1"], "prompt": "p.md", "result_path": "r.json", "timeout_seconds": 1}},
		"limits": {"max_review_cycles": 1, "max_fix_iterations": 1},
		"rate_limit_backoff": {"initial_seconds": 1, "factor": 2, "max_seconds": 2, "default_pattern": "x"},
		"full_test_command": "true"
	}`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "{{PROMPT}}") {
		t.Fatalf("expected error about missing {{PROMPT}} marker, got: %v", err)
	}
}

func TestLoad_ProviderAgentDoesNotRequirePromptMarker(t *testing.T) {
	p := writeTeamJSON(t, `{
		"agents": {"a1": {"provider": "codex", "model": "gpt-5", "effort": "medium"}},
		"roles": {"coder": {"agents": ["a1"], "prompt": "p.md", "result_path": "r.json", "timeout_seconds": 1}},
		"limits": {"max_review_cycles": 1, "max_fix_iterations": 1},
		"rate_limit_backoff": {"initial_seconds": 1, "factor": 2, "max_seconds": 2, "default_pattern": "x"},
		"full_test_command": "true"
	}`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.Agents["a1"].Provider; got != "codex" {
		t.Fatalf("Provider = %q, want codex", got)
	}
}

func TestLoad_AgentCannotSetCmdAndProvider(t *testing.T) {
	p := writeTeamJSON(t, `{
		"agents": {"a1": {"cmd": ["claude", "-p", "{{PROMPT}}"], "provider": "claude"}},
		"roles": {"coder": {"agents": ["a1"], "prompt": "p.md", "result_path": "r.json", "timeout_seconds": 1}},
		"limits": {"max_review_cycles": 1, "max_fix_iterations": 1},
		"rate_limit_backoff": {"initial_seconds": 1, "factor": 2, "max_seconds": 2, "default_pattern": "x"},
		"full_test_command": "true"
	}`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "both cmd and provider") {
		t.Fatalf("expected cmd/provider conflict error, got: %v", err)
	}
}

func TestLoad_UnknownProviderFails(t *testing.T) {
	p := writeTeamJSON(t, `{
		"agents": {"a1": {"provider": "ghost"}},
		"roles": {"coder": {"agents": ["a1"], "prompt": "p.md", "result_path": "r.json", "timeout_seconds": 1}},
		"limits": {"max_review_cycles": 1, "max_fix_iterations": 1},
		"rate_limit_backoff": {"initial_seconds": 1, "factor": 2, "max_seconds": 2, "default_pattern": "x"},
		"full_test_command": "true"
	}`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("expected unknown provider error, got: %v", err)
	}
}

func TestLoad_EscalationLadderRoundTrip(t *testing.T) {
	p := writeTeamJSON(t, `{
		"agents": {
			"a1": {"cmd": ["claude", "-p", "{{PROMPT}}"]},
			"a2": {"cmd": ["claude", "-p", "{{PROMPT}}"]}
		},
		"roles": {
			"coder": {
				"agents": ["a1"],
				"escalation_ladder": ["a2"],
				"prompt": "prompts/coder.md",
				"result_path": ".orquestalite/results/coder.json",
				"timeout_seconds": 900
			}
		},
		"limits": {"max_review_cycles": 3, "max_fix_iterations": 5},
		"rate_limit_backoff": {"initial_seconds": 30, "factor": 2, "max_seconds": 1800, "default_pattern": "rate_?limit|429"},
		"full_test_command": "go test ./..."
	}`)

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ladder := cfg.Roles["coder"].EscalationLadder
	if len(ladder) != 1 || ladder[0] != "a2" {
		t.Errorf("EscalationLadder = %v, want [a2]", ladder)
	}
}

func TestLoad_EscalationLadderUnknownAgentFails(t *testing.T) {
	p := writeTeamJSON(t, `{
		"agents": {
			"a1": {"cmd": ["claude", "-p", "{{PROMPT}}"]}
		},
		"roles": {
			"coder": {
				"agents": ["a1"],
				"escalation_ladder": ["ghost_agent"],
				"prompt": "prompts/coder.md",
				"result_path": ".orquestalite/results/coder.json",
				"timeout_seconds": 900
			}
		},
		"limits": {"max_review_cycles": 3, "max_fix_iterations": 5},
		"rate_limit_backoff": {"initial_seconds": 30, "factor": 2, "max_seconds": 1800, "default_pattern": "rate_?limit|429"},
		"full_test_command": "go test ./..."
	}`)

	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "ghost_agent") {
		t.Fatalf("expected error mentioning unknown agent 'ghost_agent', got: %v", err)
	}
}

func TestConfig_PreflightEnabledRoundTrips(t *testing.T) {
	p := writeTeamJSON(t, `{
		"agents": {
			"a1": {"cmd": ["claude", "-p", "{{PROMPT}}"]}
		},
		"roles": {
			"coder": {
				"agents": ["a1"],
				"prompt": "prompts/coder.md",
				"result_path": ".orquestalite/results/coder.json",
				"timeout_seconds": 900
			}
		},
		"limits": {"max_review_cycles": 3, "max_fix_iterations": 5, "preflight_enabled": true},
		"rate_limit_backoff": {"initial_seconds": 30, "factor": 2, "max_seconds": 1800, "default_pattern": "rate_?limit|429"},
		"full_test_command": "go test ./..."
	}`)

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Limits.PreflightEnabled {
		t.Errorf("PreflightEnabled = false, want true")
	}

	// Verify default (omitempty) — a config without the field should default to false.
	p2 := writeTeamJSON(t, `{
		"agents": {
			"a1": {"cmd": ["claude", "-p", "{{PROMPT}}"]}
		},
		"roles": {
			"coder": {
				"agents": ["a1"],
				"prompt": "prompts/coder.md",
				"result_path": ".orquestalite/results/coder.json",
				"timeout_seconds": 900
			}
		},
		"limits": {"max_review_cycles": 3, "max_fix_iterations": 5},
		"rate_limit_backoff": {"initial_seconds": 30, "factor": 2, "max_seconds": 1800, "default_pattern": "rate_?limit|429"},
		"full_test_command": "go test ./..."
	}`)
	cfg2, err := Load(p2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg2.Limits.PreflightEnabled {
		t.Errorf("PreflightEnabled = true, want false (default)")
	}
}

func TestConfig_DecomposePromptRoundTrips(t *testing.T) {
	p := writeTeamJSON(t, `{
		"agents": {
			"a1": {"cmd": ["claude", "-p", "{{PROMPT}}"]}
		},
		"roles": {
			"parser": {
				"agents": ["a1"],
				"prompt": "prompts/parser.md",
				"result_path": ".orquestalite/results/parser.json",
				"timeout_seconds": 300,
				"decompose_prompt": "prompts/parser-decompose.md"
			}
		},
		"limits": {"max_review_cycles": 3, "max_fix_iterations": 5},
		"rate_limit_backoff": {"initial_seconds": 30, "factor": 2, "max_seconds": 1800, "default_pattern": "rate_?limit|429"},
		"full_test_command": "go test ./..."
	}`)

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := cfg.Roles["parser"].DecomposePrompt
	if got != "prompts/parser-decompose.md" {
		t.Errorf("DecomposePrompt = %q, want %q", got, "prompts/parser-decompose.md")
	}
}

func TestConfig_ResolveValidTeam(t *testing.T) {
	p := writeTeamJSON(t, `{
		"agents": {
			"coder_agent": {"provider": "codex", "model": "gpt-5", "effort": "high", "rate_limit_pattern": "429"},
			"parser_agent": {"cmd": ["parser", "{{PROMPT}}"], "dangerously_skip_permissions": true},
			"tester_agent": {"provider": "claude", "model": "claude-sonnet-4-6"},
			"critic_agent": {"provider": "claude", "model": "claude-opus-4-8"},
			"reviewer_agent": {"cmd": ["reviewer", "{{PROMPT}}"]}
		},
		"roles": {
			"parser": {"agents": ["parser_agent"], "prompt": "prompts/parser.md", "result_path": ".orquestalite/results/parser.json", "timeout_seconds": 300, "decompose_prompt": "prompts/parser-decompose.md"},
			"coder": {"agents": ["coder_agent", "parser_agent"], "prompt": "prompts/coder.md", "result_path": ".orquestalite/results/coder.json", "timeout_seconds": 1200, "escalation_ladder": ["parser_agent"]},
			"tester": {"agents": ["tester_agent"], "prompt": "prompts/tester.md", "result_path": ".orquestalite/results/tester.json", "timeout_seconds": 600},
			"critic": {"agents": ["critic_agent"], "prompt": "prompts/critic.md", "result_path": ".orquestalite/results/critic.json", "timeout_seconds": 300},
			"reviewer": {"agents": ["reviewer_agent"], "prompt": "prompts/reviewer.md", "result_path": ".orquestalite/results/reviewer.json", "timeout_seconds": 600}
		},
		"limits": {"max_review_cycles": 3, "max_fix_iterations": 5},
		"rate_limit_backoff": {"initial_seconds": 30, "factor": 2, "max_seconds": 1800, "default_pattern": "rate_?limit|429"},
		"full_test_command": "go test ./..."
	}`)

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	resolved, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	coder := resolved["coder"]
	if coder.PromptPath != "prompts/coder.md" {
		t.Errorf("coder.PromptPath = %q, want prompts/coder.md", coder.PromptPath)
	}
	if coder.Timeout != 1200*time.Second {
		t.Errorf("coder.Timeout = %s, want 1200s", coder.Timeout)
	}
	if len(coder.Agents) != 2 || coder.Agents[0].Provider != "codex" || coder.Agents[1].Cmd[0] != "parser" {
		t.Errorf("coder.Agents = %+v, want ordered resolved agent specs", coder.Agents)
	}
	if len(coder.EscalationLadder) != 1 || len(coder.EscalationLadder[0].Cmd) == 0 {
		t.Errorf("coder.EscalationLadder = %+v, want resolved parser_agent", coder.EscalationLadder)
	}
	parser := resolved["parser"]
	if parser.DecomposePrompt != "prompts/parser-decompose.md" {
		t.Errorf("parser.DecomposePrompt = %q, want prompts/parser-decompose.md", parser.DecomposePrompt)
	}
	if !parser.Agents[0].SkipPerms {
		t.Errorf("parser agent SkipPerms = false, want true")
	}
}

func TestConfig_ResolveMissingOrchestratedRoleFails(t *testing.T) {
	cfg := completeResolveConfig()
	delete(cfg.Roles, "reviewer")

	_, err := cfg.Resolve()
	if err == nil || !strings.Contains(err.Error(), `missing orchestrated role "reviewer"`) {
		t.Fatalf("expected missing reviewer role error, got: %v", err)
	}
}

func TestConfig_ResolveEscalationLadderTypoFails(t *testing.T) {
	cfg := completeResolveConfig()
	coder := cfg.Roles["coder"]
	coder.EscalationLadder = []string{"claude_oppus"}
	cfg.Roles["coder"] = coder

	_, err := cfg.Resolve()
	if err == nil || !strings.Contains(err.Error(), "claude_oppus") {
		t.Fatalf("expected escalation ladder typo error, got: %v", err)
	}
}

func TestConfig_ResolveDoesNotStatDecomposePrompt(t *testing.T) {
	cfg := completeResolveConfig()
	parser := cfg.Roles["parser"]
	parser.DecomposePrompt = filepath.Join(t.TempDir(), "does-not-exist.md")
	cfg.Roles["parser"] = parser

	resolved, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}
	if resolved["parser"].DecomposePrompt != parser.DecomposePrompt {
		t.Errorf("DecomposePrompt = %q, want %q", resolved["parser"].DecomposePrompt, parser.DecomposePrompt)
	}
}

func completeResolveConfig() *Config {
	return &Config{
		Agents: map[string]Agent{
			"claude_opus": {
				Provider:                   "claude",
				Model:                      "claude-opus-4-8",
				DangerouslySkipPermissions: true,
				RateLimitPattern:           "429",
			},
			"claude_sonnet": {
				Provider: "claude",
				Model:    "claude-sonnet-4-6",
			},
			"codex_gpt5": {
				Provider: "codex",
				Model:    "gpt-5",
				Effort:   "high",
			},
		},
		Roles: map[string]Role{
			"parser": {
				Agents:          []string{"claude_opus"},
				Prompt:          "prompts/parser.md",
				ResultPath:      ".orquestalite/results/parser.json",
				TimeoutSeconds:  300,
				DecomposePrompt: "prompts/parser-decompose.md",
			},
			"coder": {
				Agents:           []string{"codex_gpt5", "claude_sonnet"},
				Prompt:           "prompts/coder.md",
				ResultPath:       ".orquestalite/results/coder.json",
				TimeoutSeconds:   1200,
				EscalationLadder: []string{"claude_opus"},
			},
			"tester": {
				Agents:         []string{"claude_sonnet"},
				Prompt:         "prompts/tester.md",
				ResultPath:     ".orquestalite/results/tester.json",
				TimeoutSeconds: 600,
			},
			"critic": {
				Agents:         []string{"claude_opus"},
				Prompt:         "prompts/critic.md",
				ResultPath:     ".orquestalite/results/critic.json",
				TimeoutSeconds: 300,
			},
			"reviewer": {
				Agents:         []string{"claude_opus"},
				Prompt:         "prompts/reviewer.md",
				ResultPath:     ".orquestalite/results/reviewer.json",
				TimeoutSeconds: 600,
			},
		},
	}
}

func fiveRolesJSON() string {
	role := func(name string) string {
		return `"` + name + `": {"agents": ["a1"], "prompt": "prompts/` + name + `.md", "result_path": ".orquestalite/results/` + name + `.json", "timeout_seconds": 60}`
	}
	return role("parser") + "," + role("coder") + "," + role("tester") + "," + role("critic") + "," + role("reviewer")
}

func TestConfig_ResolveOptionalVerifierRole(t *testing.T) {
	p := writeTeamJSON(t, `{
		"agents": {"a1": {"provider": "claude"}},
		"roles": {`+fiveRolesJSON()+`,
			"verifier": {"agents": ["a1"], "prompt": "prompts/verifier.md", "result_path": ".orquestalite/results/verifier.json", "timeout_seconds": 600}
		},
		"limits": {"max_review_cycles": 1, "max_fix_iterations": 1},
		"rate_limit_backoff": {"initial_seconds": 1, "factor": 2, "max_seconds": 2, "default_pattern": "x"},
		"full_test_command": "true"
	}`)

	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.HasVerifier() {
		t.Error("HasVerifier() = false, want true")
	}
	specs, err := cfg.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	v, ok := specs["verifier"]
	if !ok {
		t.Fatal("verifier role not resolved")
	}
	if v.Timeout != 600*time.Second {
		t.Errorf("verifier timeout = %v", v.Timeout)
	}
}

func TestConfig_ResolveWithoutVerifierRole(t *testing.T) {
	p := writeTeamJSON(t, `{
		"agents": {"a1": {"provider": "claude"}},
		"roles": {`+fiveRolesJSON()+`},
		"limits": {"max_review_cycles": 1, "max_fix_iterations": 1},
		"rate_limit_backoff": {"initial_seconds": 1, "factor": 2, "max_seconds": 2, "default_pattern": "x"},
		"full_test_command": "true"
	}`)

	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HasVerifier() {
		t.Error("HasVerifier() = true, want false")
	}
	specs, err := cfg.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := specs["verifier"]; ok {
		t.Error("verifier should not be resolved when absent")
	}
}

func TestLimits_TesterVerificationDefaultsOn(t *testing.T) {
	var l Limits
	if !l.TesterVerificationEnabled() {
		t.Error("nil verify_tester_command should default to enabled")
	}
	off := false
	l.VerifyTesterCommand = &off
	if l.TesterVerificationEnabled() {
		t.Error("explicit false should disable verification")
	}
}
