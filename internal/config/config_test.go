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
			"coder": {"agents": ["a1"], "prompt": "prompts/coder.md", "result_path": ".pyorquesta/results/coder.json", "timeout_seconds": 900}
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
