package commands

import (
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
		".pyorquesta/results",
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
	if !strings.Contains(string(raw), ".pyorquesta/") {
		t.Errorf(".gitignore did not get .pyorquesta/: %s", raw)
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
