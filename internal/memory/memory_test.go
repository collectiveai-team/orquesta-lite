package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendCreatesFileAndAddsHeader(t *testing.T) {
	p := filepath.Join(t.TempDir(), "memory.md")
	if err := Append(p, Entry{Cycle: 1, TaskID: "T003", Role: "critic", Body: "snake_case in DB"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, "## [cycle 1, task T003, critic]") {
		t.Errorf("missing header: %s", got)
	}
	if !strings.Contains(got, "snake_case in DB") {
		t.Errorf("missing body: %s", got)
	}
}

func TestAppendIsAppendOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "memory.md")
	_ = Append(p, Entry{Cycle: 1, TaskID: "T001", Role: "coder", Body: "first"})
	_ = Append(p, Entry{Cycle: 1, TaskID: "T002", Role: "coder", Body: "second"})
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), "first") || !strings.Contains(string(raw), "second") {
		t.Errorf("append did not preserve prior content: %s", raw)
	}
}

func TestReadAllReturnsEmptyIfMissing(t *testing.T) {
	got, err := ReadAll(filepath.Join(t.TempDir(), "nope.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}
