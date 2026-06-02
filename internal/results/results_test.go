package results

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "r.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseTester_PassNoFailures(t *testing.T) {
	p := write(t, `{"status":"pass","command_run":"go test","failures":[],"notes_for_memory":null}`)
	r, err := ParseTester(p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "pass" {
		t.Errorf("status = %q", r.Status)
	}
}

func TestParseTester_FailMissingCommand(t *testing.T) {
	p := write(t, `{"status":"fail","failures":[{"test":"t1","message":"boom"}]}`)
	_, err := ParseTester(p)
	if err == nil {
		t.Fatal("expected error: command_run required")
	}
}

func TestParseCritic_RejectedRequiresConcerns(t *testing.T) {
	p := write(t, `{"status":"rejected","concerns":[]}`)
	_, err := ParseCritic(p)
	if err == nil {
		t.Fatal("expected error: rejected critic must list concerns")
	}
}

func TestParseReviewer_ShouldStopMissing(t *testing.T) {
	p := write(t, `{"summary_of_cycle":"x","new_tasks":[]}`)
	_, err := ParseReviewer(p)
	if err == nil {
		t.Fatal("expected error: should_stop required")
	}
}

func TestParseParser_ZeroTasksOK(t *testing.T) {
	p := write(t, `{"tasks":[],"notes_for_memory":null}`)
	r, err := ParseParser(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Tasks) != 0 {
		t.Errorf("tasks = %d", len(r.Tasks))
	}
}

func TestArchive_WritesDistinctAttemptFilesWithoutTouchingCanonical(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, ".orquestalite", "results", "coder.json")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, []byte(`{"status":"canonical"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Archive(dir, "coder", "T005", 2, 1, []byte(`{"status":"attempt 1"}`)); err != nil {
		t.Fatal(err)
	}
	if err := Archive(dir, "coder", "T005", 2, 2, []byte(`{"status":"attempt 2"}`)); err != nil {
		t.Fatal(err)
	}

	assertFile(t, filepath.Join(dir, ".orquestalite", "results", "by-task", "T005", "coder.c2.a1.json"), `{"status":"attempt 1"}`)
	assertFile(t, filepath.Join(dir, ".orquestalite", "results", "by-task", "T005", "coder.c2.a2.json"), `{"status":"attempt 2"}`)
	assertFile(t, canonical, `{"status":"canonical"}`)
}

func TestArchive_UsesSyntheticTaskIDsForPlanAndReviewRoles(t *testing.T) {
	dir := t.TempDir()

	if err := Archive(dir, "parser", "ignored", 1, 1, []byte(`{"tasks":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := Archive(dir, "reviewer", "ignored", 3, 1, []byte(`{"summary_of_cycle":"done"}`)); err != nil {
		t.Fatal(err)
	}

	assertFile(t, filepath.Join(dir, ".orquestalite", "results", "by-task", "_plan", "parser.c1.a1.json"), `{"tasks":[]}`)
	assertFile(t, filepath.Join(dir, ".orquestalite", "results", "by-task", "_review", "reviewer.c3.a1.json"), `{"summary_of_cycle":"done"}`)
}

func TestArchive_WritesRerunSuffixWhenArchiveAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, ".orquestalite", "results", "by-task", "T005", "tester.c1.a1.json")
	rerunArchive := filepath.Join(dir, ".orquestalite", "results", "by-task", "T005", "tester.c1.a1.r2.json")

	if err := Archive(dir, "tester", "T005", 1, 1, []byte(`{"status":"first"}`)); err != nil {
		t.Fatal(err)
	}
	if err := Archive(dir, "tester", "T005", 1, 1, []byte(`{"status":"second"}`)); err != nil {
		t.Fatal(err)
	}

	assertFile(t, archive, `{"status":"first"}`)
	assertFile(t, rerunArchive, `{"status":"second"}`)
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
