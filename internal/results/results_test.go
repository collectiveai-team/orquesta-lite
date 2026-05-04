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
