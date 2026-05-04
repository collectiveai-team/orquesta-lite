package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInterpolate(t *testing.T) {
	tmpl := "Task: {{TASK_TITLE}}\nAttempt {{ATTEMPT_NUMBER}}\n{{TESTER_FEEDBACK}}"
	got := Interpolate(tmpl, map[string]string{
		"TASK_TITLE":      "Add login",
		"ATTEMPT_NUMBER":  "2",
		"TESTER_FEEDBACK": "test_login failed: unexpected status",
	})
	want := "Task: Add login\nAttempt 2\ntest_login failed: unexpected status"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestInterpolate_MissingVarStaysLiteral(t *testing.T) {
	got := Interpolate("hello {{NAME}} {{UNKNOWN}}", map[string]string{"NAME": "world"})
	if !strings.Contains(got, "{{UNKNOWN}}") {
		t.Errorf("unknown vars should remain literal, got: %q", got)
	}
}

func TestInterpolate_PreservesCurlyBracesInContent(t *testing.T) {
	tmpl := `Output JSON: {"status": "{{STATUS}}"}`
	got := Interpolate(tmpl, map[string]string{"STATUS": "pass"})
	want := `Output JSON: {"status": "pass"}`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.md")
	if err := os.WriteFile(p, []byte("hello {{X}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmpl, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if tmpl != "hello {{X}}" {
		t.Errorf("Load mismatch: %q", tmpl)
	}
}
