package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/doctor"
)

func doctorStatuses(checks []doctor.Check) map[string]doctor.Status {
	m := map[string]doctor.Status{}
	for _, c := range checks {
		m[c.Name] = c.Status
	}
	return m
}

func TestDoctor_MissingTeamJSONFails(t *testing.T) {
	dir := t.TempDir()
	var sb strings.Builder
	err := Doctor(dir, &sb)
	if err == nil {
		t.Fatal("expected failure without team.json")
	}
	if !strings.Contains(sb.String(), "[FAIL] team.json") {
		t.Fatalf("output:\n%s", sb.String())
	}
}

func TestDoctor_MissingPromptsFail(t *testing.T) {
	dir := initTestRepo(t)
	team := `{
		"agents": {"a1": {"cmd": ["true", "{{PROMPT}}"]}},
		"roles": {
			"parser":   {"agents": ["a1"], "prompt": "prompts/parser.md",   "result_path": "r.json", "timeout_seconds": 1},
			"coder":    {"agents": ["a1"], "prompt": "prompts/coder.md",    "result_path": "r.json", "timeout_seconds": 1},
			"tester":   {"agents": ["a1"], "prompt": "prompts/tester.md",   "result_path": "r.json", "timeout_seconds": 1},
			"critic":   {"agents": ["a1"], "prompt": "prompts/critic.md",   "result_path": "r.json", "timeout_seconds": 1},
			"reviewer": {"agents": ["a1"], "prompt": "prompts/reviewer.md", "result_path": "r.json", "timeout_seconds": 1}
		},
		"limits": {"max_review_cycles": 1, "max_fix_iterations": 1},
		"rate_limit_backoff": {"initial_seconds": 1, "factor": 2, "max_seconds": 2, "default_pattern": "x"},
		"full_test_command": "true"
	}`
	if err := os.WriteFile(filepath.Join(dir, "team.json"), []byte(team), 0o644); err != nil {
		t.Fatal(err)
	}
	// Only create two of the five prompts.
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"parser.md", "coder.md"} {
		if err := os.WriteFile(filepath.Join(dir, "prompts", p), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	statuses := doctorStatuses(doctor.Run(context.Background(), dir))
	if statuses["prompts"] != doctor.StatusError {
		t.Errorf("prompts = %v, want error", statuses["prompts"])
	}
	// cmd-based agent: no provider credential checks should appear.
	if _, ok := statuses["provider:claude"]; ok {
		t.Error("cmd-only team should not check provider CLIs")
	}
	if statuses["full_test_command"] != doctor.StatusOK {
		t.Errorf("full_test_command = %v", statuses["full_test_command"])
	}
}

func TestDoctor_UnknownTestBinaryFails(t *testing.T) {
	dir := initTestRepo(t)
	team := `{
		"agents": {"a1": {"cmd": ["true", "{{PROMPT}}"]}},
		"roles": {
			"parser":   {"agents": ["a1"], "prompt": "p.md", "result_path": "r.json", "timeout_seconds": 1},
			"coder":    {"agents": ["a1"], "prompt": "p.md", "result_path": "r.json", "timeout_seconds": 1},
			"tester":   {"agents": ["a1"], "prompt": "p.md", "result_path": "r.json", "timeout_seconds": 1},
			"critic":   {"agents": ["a1"], "prompt": "p.md", "result_path": "r.json", "timeout_seconds": 1},
			"reviewer": {"agents": ["a1"], "prompt": "p.md", "result_path": "r.json", "timeout_seconds": 1}
		},
		"limits": {"max_review_cycles": 1, "max_fix_iterations": 1},
		"rate_limit_backoff": {"initial_seconds": 1, "factor": 2, "max_seconds": 2, "default_pattern": "x"},
		"full_test_command": "definitely-not-a-real-binary-xyz test"
	}`
	if err := os.WriteFile(filepath.Join(dir, "team.json"), []byte(team), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	statuses := doctorStatuses(doctor.Run(context.Background(), dir))
	if statuses["full_test_command"] != doctor.StatusError {
		t.Errorf("full_test_command = %v, want error", statuses["full_test_command"])
	}
}
