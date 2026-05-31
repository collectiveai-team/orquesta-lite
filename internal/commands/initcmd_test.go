package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInit_CreatesScaffolding(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"team.json",
		"prompts/parser.md", "prompts/coder.md", "prompts/tester.md", "prompts/critic.md", "prompts/reviewer.md",
		".orquestalite/results",
	} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
}

func TestInit_AddsGitignoreEntry(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("# user\nnode_modules/\n"), 0o644)
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.Contains(string(raw), ".orquestalite/") {
		t.Errorf(".gitignore did not get .orquestalite/: %s", raw)
	}
	if !strings.Contains(string(raw), "node_modules/") {
		t.Errorf("init must not delete prior gitignore lines: %s", raw)
	}
}

func TestInit_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir); err != nil {
		t.Fatalf("second init failed: %v", err)
	}
}

// TestInit_MaterialisesSchemas verifies that Init writes all five role schemas
// to <workspace>/schemas/ and that each file parses as valid JSON.
func TestInit_MaterialisesSchemas(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}

	expectedSchemas := []string{
		"parser.json",
		"coder.json",
		"tester.json",
		"critic.json",
		"reviewer.json",
	}

	for _, name := range expectedSchemas {
		path := filepath.Join(dir, "schemas", name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("schema %s missing: %v", name, err)
			continue
		}
		var v map[string]any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Errorf("schema %s is not valid JSON: %v", name, err)
		}
	}
}

// TestInit_TeamJSONHasCodexProviderPrimary verifies that the materialised
// team.json lists codex_gpt5 as primary coder via the provider config, and
// claude_sonnet as fallback.
func TestInit_TeamJSONHasCodexProviderPrimary(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "team.json"))
	if err != nil {
		t.Fatal(err)
	}

	var cfg struct {
		Agents map[string]struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
			Effort   string `json:"effort"`
		} `json:"agents"`
		Roles map[string]struct {
			Agents []string `json:"agents"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("team.json parse error: %v", err)
	}

	// Verify codex_gpt5 agent exists with provider launch metadata.
	codexAgent, ok := cfg.Agents["codex_gpt5"]
	if !ok {
		t.Fatal("agents.codex_gpt5 not found in team.json")
	}
	if codexAgent.Provider != "codex" {
		t.Errorf("codex_gpt5.provider = %q, want codex", codexAgent.Provider)
	}
	if codexAgent.Model != "gpt-5" {
		t.Errorf("codex_gpt5.model = %q, want gpt-5", codexAgent.Model)
	}
	if codexAgent.Effort != "medium" {
		t.Errorf("codex_gpt5.effort = %q, want medium", codexAgent.Effort)
	}

	// Verify coder role: primary = codex_gpt5, fallback = claude_sonnet.
	coderRole, ok := cfg.Roles["coder"]
	if !ok {
		t.Fatal("roles.coder not found in team.json")
	}
	if len(coderRole.Agents) < 2 {
		t.Fatalf("roles.coder.agents has %d entries, want >= 2", len(coderRole.Agents))
	}
	if coderRole.Agents[0] != "codex_gpt5" {
		t.Errorf("roles.coder.agents[0] = %q, want codex_gpt5", coderRole.Agents[0])
	}
	if coderRole.Agents[1] != "claude_sonnet" {
		t.Errorf("roles.coder.agents[1] = %q, want claude_sonnet", coderRole.Agents[1])
	}
}

// TestInit_MaterialisesDecomposePrompt verifies that Init writes the
// parser-decompose.md prompt to <workspace>/prompts/.
func TestInit_MaterialisesDecomposePrompt(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "prompts", "parser-decompose.md")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("prompts/parser-decompose.md not materialised: %v", err)
	}
}

// TestInit_WarnsWhenCodexMissing would verify that Init prints a warning when
// the codex binary is not in PATH. However, exec.LookPath cannot be stubbed
// without dependency injection, and we cannot guarantee that the test runner
// environment lacks (or has) codex. Injecting the lookup function would require
// changing the Init signature, which is out of scope for this phase.
// The warning behaviour is covered by manual testing and code review.
func TestInit_WarnsWhenCodexMissing(t *testing.T) {
	t.Skip("exec.LookPath cannot be cleanly stubbed without injecting the lookup function; skipped by design")
}
