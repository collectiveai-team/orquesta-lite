package artifacts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDir_CollidesWithRerunSuffix(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}

	d1, err := s.Dir("T003", "coder", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveInvocation(d1, Invocation{Prompt: "p1", Stdout: "o1", Agent: "claude"}); err != nil {
		t.Fatal(err)
	}
	// Same task/role/cycle/attempt again → collisions get .r2.
	d2, err := s.Dir("T003", "coder", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if d2 == d1 {
		t.Fatalf("rerun dir should differ: d1=%s d2=%s", d1, d2)
	}
	if !strings.HasSuffix(filepath.Base(d2), ".r2") {
		t.Fatalf("rerun dir should end with .r2: %s", d2)
	}
	d3, err := s.Dir("T003", "coder", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(filepath.Base(d3), ".r3") {
		t.Fatalf("third rerun should end with .r3: %s", d3)
	}
}

func TestSaveInvocation_WritesFourFiles(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}
	dir, err := s.Dir("T007", "tester", 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveInvocation(dir, Invocation{
		Prompt:    "the prompt",
		Stdout:    "stdout body",
		Stderr:    "stderr body",
		Agent:     "codex_gpt5",
		Model:     "gpt-5",
		Provider:  "codex",
		DurationS: 12,
		ExitCode:  0,
		SessionID: "sess-1",
		CmdLine:   "codex --model gpt-5",
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"prompt.md", "stdout.log", "stderr.log", "meta.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "prompt.md")); string(got) != "the prompt" {
		t.Errorf("prompt.md = %q", got)
	}
}

func TestDir_EmptyTaskKeyUsesDash(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}
	d, err := s.Dir("", "reviewer", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d, filepath.Join("agents", "-", "reviewer.c1.a1")) {
		t.Errorf("empty task key should map to '-', got %s", d)
	}
}

func TestPrune_KeepsNewest(t *testing.T) {
	runs := t.TempDir() // .orquestalite/runs
	for _, id := range []string{"r20260701T100000Z-aaaa", "r20260701T110000Z-bbbb", "r20260701T120000Z-cccc"} {
		if err := os.MkdirAll(filepath.Join(runs, id), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s := &Store{Root: filepath.Join(runs, "r20260701T120000Z-cccc")}
	if err := s.Prune(2); err != nil {
		t.Fatal(err)
	}
	left, _ := os.ReadDir(runs)
	if len(left) != 2 {
		t.Fatalf("after prune, %d run dirs remain, want 2", len(left))
	}
	// The oldest (10:00) must be gone; the two newest survive.
	for _, e := range left {
		if strings.HasPrefix(e.Name(), "r20260701T100000Z") {
			t.Errorf("oldest run not pruned: %s", e.Name())
		}
	}
}

func TestNilStore_NoOp(t *testing.T) {
	var s *Store
	d, err := s.Dir("T1", "coder", 1, 1)
	if err != nil || d != "" {
		t.Errorf("nil Store.Dir should be a no-op, got dir=%q err=%v", d, err)
	}
	if err := s.SaveInvocation("", Invocation{}); err != nil {
		t.Errorf("nil Store.SaveInvocation should be a no-op, got %v", err)
	}
}