package commands

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/results"
)

type stubIntakeCaller struct {
	out *results.IntakeResult
}

func (s *stubIntakeCaller) RunIntake(_ context.Context, _ string) (*results.IntakeResult, error) {
	return s.out, nil
}

func writeIssue(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "issue.md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseIntake_ActionableRequiresPlan(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "intake.json")
	if err := os.WriteFile(p, []byte(`{"actionable":true,"plan":"# Plan\n..."}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := results.ParseIntake(p)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Actionable || strings.TrimSpace(r.Plan) == "" {
		t.Fatalf("parsed = %+v", r)
	}
}

func TestParseIntake_NotActionableRequiresMissingInfo(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "intake.json")
	if err := os.WriteFile(p, []byte(`{"actionable":false,"missing_info":"- need endpoint path"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := results.ParseIntake(p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Actionable || !strings.Contains(r.MissingInfo, "endpoint path") {
		t.Fatalf("parsed = %+v", r)
	}
}

func TestParseIntake_ActionableWithoutPlanErrors(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "intake.json")
	if err := os.WriteFile(p, []byte(`{"actionable":true,"plan":""}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := results.ParseIntake(p); err == nil || !strings.Contains(err.Error(), "plan is empty") {
		t.Fatalf("expected plan-is-empty error, got %v", err)
	}
}

func TestParseIntake_NotActionableWithoutMissingInfoErrors(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "intake.json")
	if err := os.WriteFile(p, []byte(`{"actionable":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := results.ParseIntake(p); err == nil || !strings.Contains(err.Error(), "missing_info is empty") {
		t.Fatalf("expected missing_info error, got %v", err)
	}
}

func TestIntake_ActionableProducesTasksAndReachesRun(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".orquestalite"), 0o755); err != nil {
		t.Fatal(err)
	}
	issue := writeIssue(t, dir, "Add password reset; acceptance: POST /reset returns 200")

	stub := &stubIntakeCaller{out: &results.IntakeResult{
		Actionable: true,
		Plan:       "# Add password reset\nGoal: ...\n",
	}}
	var (
		planCalled bool
		runCalled  bool
	)
	plan := func(_ context.Context, projectDir, planPath string) error {
		planCalled = true
		// Simulate the parser: write tasks.json next to the plan.
		if err := os.MkdirAll(filepath.Join(projectDir, ".orquestalite"), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(projectDir, ".orquestalite", "tasks.json"),
			[]byte(`{"tasks":[{"id":"T001","title":"reset endpoint","status":"pending","priority":1}]}`), 0o644)
	}
	run := func(_ context.Context, _ string) error {
		runCalled = true
		return nil
	}
	var out bytes.Buffer
	err := IntakeWith(context.Background(), IntakeOptions{
		ProjectDir: dir,
		IssuePath:  issue,
		PlanPath:   "plan.md",
		Run:        true,
		Out:        &out,
	}, stub, plan, run)
	if err != nil {
		t.Fatal(err)
	}
	if !planCalled {
		t.Error("plan step was not called for an actionable issue")
	}
	if !runCalled {
		t.Error("run loop was not reached for an actionable issue")
	}
	if _, err := os.Stat(filepath.Join(dir, ".orquestalite", "tasks.json")); err != nil {
		t.Errorf("tasks.json not produced: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "plan.md")); err != nil {
		t.Errorf("plan.md not written: %v", err)
	}
	if !strings.Contains(out.String(), "actionable") {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestIntake_NotActionableWritesMissingInfoAndStops(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".orquestalite"), 0o755); err != nil {
		t.Fatal(err)
	}
	issue := writeIssue(t, dir, "make it better")

	stub := &stubIntakeCaller{out: &results.IntakeResult{
		Actionable:  false,
		MissingInfo: "- The endpoint path is not specified\n- No acceptance criteria given",
	}}
	var planCalled, runCalled bool
	plan := func(context.Context, string, string) error { planCalled = true; return nil }
	run := func(context.Context, string) error { runCalled = true; return nil }
	var out bytes.Buffer
	if err := IntakeWith(context.Background(), IntakeOptions{
		ProjectDir: dir, IssuePath: issue, Out: &out,
	}, stub, plan, run); err != nil {
		t.Fatal(err)
	}
	if planCalled || runCalled {
		t.Error("an incomplete issue must not reach plan or run")
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".orquestalite", "intake-missing-info.md"))
	if err != nil {
		t.Fatalf("missing_info file not written: %v", err)
	}
	if !strings.Contains(string(raw), "endpoint path is not specified") {
		t.Errorf("missing_info body = %q", raw)
	}
	if !strings.Contains(out.String(), "NOT actionable") {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestIntake_NoIssueErrors(t *testing.T) {
	err := IntakeWith(context.Background(), IntakeOptions{
		ProjectDir: t.TempDir(), Out: &bytes.Buffer{},
	}, &stubIntakeCaller{}, func(context.Context, string, string) error { return nil }, func(context.Context, string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "--issue") {
		t.Fatalf("expected --issue error, got %v", err)
	}
}

func TestIntake_ReadError(t *testing.T) {
	err := IntakeWith(context.Background(), IntakeOptions{
		ProjectDir: t.TempDir(),
		IssuePath:  filepath.Join(t.TempDir(), "missing.md"),
		Out:        &bytes.Buffer{},
	}, &stubIntakeCaller{}, func(context.Context, string, string) error { return nil }, func(context.Context, string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "read issue") {
		t.Fatalf("expected read error, got %v", err)
	}
	_ = errors.New
}
