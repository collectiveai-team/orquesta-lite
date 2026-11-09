package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/workflow"
)

func TestBlockedPlanCLIReportPreservesExternalWorkVisibility(t *testing.T) {
	ctx := context.Background()
	store, err := workflow.Open(filepath.Join(t.TempDir(), "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.CreateRunOnce(ctx, workflow.CreateRunParams{ID: "blocked-report", FlowRef: "flow:factory-governed@2", IR: json.RawMessage(`{}`), Inputs: json.RawMessage(`{}`), Policy: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	step, err := store.EnsureStep(ctx, "blocked-report", "root", "plan_outcome", "", "activity:plan.outcome@1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteVirtualStep(ctx, step, json.RawMessage(`{"status":"blocked","complete":false,"blocked":["T02","T03"],"pending":[{"id":"T02"},{"id":"T03"}],"summary":"External access is unavailable"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunStatus(ctx, "blocked-report", workflow.RunSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := reportBlockedPlan(ctx, store, "blocked-report", &output); err != nil {
		t.Fatal(err)
	}
	want := "plan_status=blocked pending=2 blocked=T02,T03 full_scope_approved=false\n"
	if output.String() != want {
		t.Fatalf("successful orchestration hid incomplete product scope: got %q want %q", output.String(), want)
	}
}

func TestBlockedPlanCLIReportDoesNotWarnForCompletedScope(t *testing.T) {
	ctx := context.Background()
	store, err := workflow.Open(filepath.Join(t.TempDir(), "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.CreateRunOnce(ctx, workflow.CreateRunParams{ID: "complete-report", IR: json.RawMessage(`{}`), Inputs: json.RawMessage(`{}`), Policy: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	step, err := store.EnsureStep(ctx, "complete-report", "root", "plan_outcome", "", "activity:plan.outcome@1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteVirtualStep(ctx, step, json.RawMessage(`{"status":"complete","complete":true,"blocked":[],"pending":[]}`)); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := reportBlockedPlan(ctx, store, "complete-report", &output); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("completed scope received blocked warning: %q", output.String())
	}
}
