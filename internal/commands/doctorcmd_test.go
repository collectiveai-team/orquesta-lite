package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderHasUsableCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GEMINI_API_KEY", "")

	// A provider we have no credential profile for: assume usable (cannot tell).
	if !providerHasUsableCredentials("mystery-provider") {
		t.Errorf("unknown provider should be assumed usable")
	}

	// No env var and no cached login: not usable (this is the gemini case that
	// caused interactive auth prompts mid-run).
	if providerHasUsableCredentials("gemini") {
		t.Errorf("expected gemini unusable with no env var and no cached login")
	}

	// API-key env var present: usable headless.
	t.Setenv("GEMINI_API_KEY", "test-key")
	if !providerHasUsableCredentials("gemini") {
		t.Errorf("expected gemini usable via GEMINI_API_KEY")
	}
	t.Setenv("GEMINI_API_KEY", "")

	// Cached login file present: usable.
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gemini", "oauth_creds.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !providerHasUsableCredentials("gemini") {
		t.Errorf("expected gemini usable via cached login file")
	}
}

func doctorLevels(checks []check) map[string]checkLevel {
	m := map[string]checkLevel{}
	for _, c := range checks {
		m[c.Name] = c.Level
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

	levels := doctorLevels(runDoctorChecks(dir))
	if levels["prompts"] != checkFail {
		t.Errorf("prompts = %v, want FAIL", levels["prompts"])
	}
	// cmd-based agent: no provider credential checks should appear.
	if _, ok := levels["claude CLI"]; ok {
		t.Error("cmd-only team should not check provider CLIs")
	}
	if levels["full_test_command"] != checkPass {
		t.Errorf("full_test_command = %v", levels["full_test_command"])
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

	levels := doctorLevels(runDoctorChecks(dir))
	if levels["full_test_command"] != checkFail {
		t.Errorf("full_test_command = %v, want FAIL", levels["full_test_command"])
	}
}
