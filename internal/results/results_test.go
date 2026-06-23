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

func TestParsePlanner_FeaturesWithVisualFlag(t *testing.T) {
	p := write(t, `{"features":[
		{"title":"Capacity page","plan":"render /capacity","acceptance_criteria":["banner shows"],"files_likely_touched":["app/capacity.tsx"],"visual":true},
		{"title":"Capacity rollup API","plan":"GET /api/capacity","acceptance_criteria":["200"],"visual":false}
	],"notes_for_memory":null}`)
	r, err := ParsePlanner(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Features) != 2 {
		t.Fatalf("got %d features", len(r.Features))
	}
	if !r.Features[0].Visual {
		t.Error("feature 0 should be visual")
	}
	if r.Features[1].Visual {
		t.Error("feature 1 (API) should not be visual")
	}
}

func TestParseCompactor_RequiresMemory(t *testing.T) {
	ok := write(t, `{"memory":"## Backend\n- uses postgres","kept_notes":3}`)
	r, err := ParseCompactor(ok)
	if err != nil {
		t.Fatal(err)
	}
	if r.KeptNotes != 3 || r.Memory == "" {
		t.Fatalf("got %+v", r)
	}
	if r.MemoryNote() != nil {
		t.Error("compactor must never append a memory note")
	}

	empty := write(t, `{"memory":"   ","kept_notes":0}`)
	if _, err := ParseCompactor(empty); err == nil {
		t.Fatal("expected error: compactor.memory required")
	}
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

func TestParseVerifier_PassWithChecks(t *testing.T) {
	p := write(t, `{"status":"pass","checks":[{"name":"health","action":"curl localhost:8000/healthz","expected":"200","actual":"200","passed":true}],"notes_for_memory":null}`)
	r, err := ParseVerifier(p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "pass" || len(r.Checks) != 1 {
		t.Errorf("got %+v", r)
	}
}

func TestParseVerifier_RequiresChecks(t *testing.T) {
	p := write(t, `{"status":"pass","checks":[]}`)
	if _, err := ParseVerifier(p); err == nil {
		t.Fatal("expected error: checks required")
	}
}

func TestParseVerifier_FailRequiresFailedCheck(t *testing.T) {
	p := write(t, `{"status":"fail","checks":[{"name":"x","action":"a","expected":"e","actual":"e","passed":true}]}`)
	if _, err := ParseVerifier(p); err == nil {
		t.Fatal("expected error: fail status must include a failed check")
	}
}

func TestParseVerifier_InvalidStatus(t *testing.T) {
	p := write(t, `{"status":"maybe","checks":[{"name":"x","action":"a","expected":"e","actual":"a","passed":false}]}`)
	if _, err := ParseVerifier(p); err == nil {
		t.Fatal("expected error: invalid status")
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

func TestLatestByTask_PicksHighestCycleAttemptRerun(t *testing.T) {
	dir := t.TempDir()
	for _, a := range []struct{ cycle, attempt, rerun int }{{1, 1, 0}, {1, 2, 0}, {2, 1, 0}} {
		if err := Archive(dir, "coder", "T010", a.cycle, a.attempt, []byte(`{"summary":"c`+itoa(a.cycle)+`a`+itoa(a.attempt)+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	raw, ok := LatestByTask(dir, "T010", "coder")
	if !ok {
		t.Fatal("expected an archive")
	}
	if string(raw) != `{"summary":"c2a1"}` {
		t.Fatalf("latest = %q, want c2a1", raw)
	}
	if _, ok := LatestByTask(dir, "T999", "coder"); ok {
		t.Fatal("missing task should return ok=false")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return string(rune('0' + n))
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
