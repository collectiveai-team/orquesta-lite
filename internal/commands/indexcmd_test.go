package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIndex_BuildsAndRebuildsDB(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, ".orquestalite")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	log := `{"ts":"2026-07-01T10:00:00Z","event":"run_start","run_id":"r1","command":"run","orq_version":"v0.2.0"}` + "\n"
	if err := os.WriteFile(filepath.Join(state, "run.log"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := Index(dir, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 runs") {
		t.Fatalf("output = %q, want '1 runs'", out.String())
	}
	if _, err := os.Stat(filepath.Join(state, "orq.db")); err != nil {
		t.Fatalf("orq.db missing: %v", err)
	}

	// --rebuild starts from scratch and lands on the same counts.
	out.Reset()
	if err := Index(dir, true, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 runs") || !strings.Contains(out.String(), "1 events") {
		t.Fatalf("rebuild output = %q", out.String())
	}
}
