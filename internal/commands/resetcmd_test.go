package commands

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReset_RemovesPyorquestaDir(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, ".pyorquesta")
	_ = os.MkdirAll(filepath.Join(state, "results"), 0o755)
	_ = os.WriteFile(filepath.Join(state, "tasks.json"), []byte("{}"), 0o644)
	if err := Reset(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Errorf("expected .pyorquesta to be gone")
	}
}

func TestReset_NoOpIfMissing(t *testing.T) {
	if err := Reset(t.TempDir()); err != nil {
		t.Fatalf("expected no-op, got %v", err)
	}
}
