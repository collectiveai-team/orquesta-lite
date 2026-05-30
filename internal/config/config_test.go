package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
