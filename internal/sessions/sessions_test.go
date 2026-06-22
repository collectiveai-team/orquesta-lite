package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStore_SetGetPersistsAcrossLoad(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".orquestalite"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := Load(dir)
	if got := s.Get("T001", "coder", "claude_sonnet"); got != "" {
		t.Fatalf("empty store should return \"\", got %q", got)
	}
	if err := s.Set("T001", "coder", "claude_sonnet", "sess-abc"); err != nil {
		t.Fatal(err)
	}

	// A fresh Load must see the persisted value.
	s2 := Load(dir)
	if got := s2.Get("T001", "coder", "claude_sonnet"); got != "sess-abc" {
		t.Fatalf("persisted value = %q, want sess-abc", got)
	}
	// Different agent / task / role keys are independent.
	if got := s2.Get("T001", "coder", "codex_gpt5"); got != "" {
		t.Fatalf("different agent should be empty, got %q", got)
	}
	if got := s2.Get("T002", "coder", "claude_sonnet"); got != "" {
		t.Fatalf("different task should be empty, got %q", got)
	}
}

func TestStore_SetEmptyIDIsNoop(t *testing.T) {
	dir := t.TempDir()
	s := Load(dir)
	if err := s.Set("T001", "coder", "claude_sonnet", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".orquestalite", "sessions.json")); !os.IsNotExist(err) {
		t.Fatalf("empty id must not create the store file (err=%v)", err)
	}
}

func TestStore_DeleteRemovesAndPersists(t *testing.T) {
	dir := t.TempDir()
	s := Load(dir)
	_ = s.Set("T001", "coder", "claude_sonnet", "sess-1")
	if err := s.Delete("T001", "coder", "claude_sonnet"); err != nil {
		t.Fatal(err)
	}
	if got := Load(dir).Get("T001", "coder", "claude_sonnet"); got != "" {
		t.Fatalf("deleted value should be empty, got %q", got)
	}
	// Deleting a missing key is a no-op, not an error.
	if err := s.Delete("nope", "coder", "x"); err != nil {
		t.Fatal(err)
	}
}

func TestStore_OverwriteUpdatesValue(t *testing.T) {
	dir := t.TempDir()
	s := Load(dir)
	_ = s.Set("T001", "coder", "claude_sonnet", "sess-1")
	_ = s.Set("T001", "coder", "claude_sonnet", "sess-2")
	if got := Load(dir).Get("T001", "coder", "claude_sonnet"); got != "sess-2" {
		t.Fatalf("value = %q, want sess-2", got)
	}
}
