package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/supervision"
	"github.com/collectiveai-team/orquesta-lite/internal/workflow"
)

func supervisorFixture(t *testing.T, activityRef, reason string) (string, *workflow.Store, *workflow.Approval, string) {
	t.Helper()
	project := t.TempDir()
	root := filepath.Join(project, ".orquestalite")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := workflow.Open(filepath.Join(root, "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	_, _, err = store.CreateRunOnce(ctx, workflow.CreateRunParams{ID: "checkpoint-run", FlowRef: "flow:test@1", IR: json.RawMessage(`{}`), Inputs: json.RawMessage(`{}`), Policy: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	step, err := store.EnsureStep(ctx, "checkpoint-run", "root/unit", "repair", "lens", "activity:agent.invoke@1", json.RawMessage(`{"role":"verified_integrator"}`))
	if err != nil {
		t.Fatal(err)
	}
	if activityRef != "agent.invoke@1" {
		step, err = store.EnsureStep(ctx, "checkpoint-run", "root/unit", "other", "lens", activityRef, json.RawMessage(`{"role":"verified_integrator"}`))
		if err != nil {
			t.Fatal(err)
		}
	}
	approval, err := store.CreateApproval(ctx, step, reason)
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := json.Marshal([]string{"checkpoint-run", step.ScopePath, step.ForeachKey, step.StepID})
	task := fmt.Sprintf("%x", sha256.Sum256(identity))
	return project, store, approval, task
}

func supervisorArgs(approval, task, role string) []string {
	return []string{"decide", "checkpoint-run", "--approval", approval, "--task", task, "--role", role, "--action", "continue", "--reason", "fresh audit confirmed bounded continuation"}
}

func TestSupervisorDecisionMatchesActualPausedAgent(t *testing.T) {
	project, store, approval, task := supervisorFixture(t, "agent.invoke@1", supervision.ErrCheckpointRequired.Error()+": tool budget reached")
	var out bytes.Buffer
	if err := SupervisorCLI(context.Background(), project, supervisorArgs(approval.ID, task, "verified_integrator"), &out); err != nil {
		t.Fatal(err)
	}
	approvals, err := store.ListApprovals(context.Background(), "checkpoint-run")
	if err != nil || len(approvals) != 1 || approvals[0].Status != "approved" {
		t.Fatalf("checkpoint not acknowledged: %+v %v", approvals, err)
	}
	root := filepath.Join(project, ".orquestalite", "runs", "checkpoint-run")
	found, err := supervision.HasAcknowledgement(root, task, "verified_integrator", approval.CreatedAt)
	if err != nil || !found {
		t.Fatalf("fresh decision marker missing: %v", err)
	}
}

func TestSupervisorRejectsUnrelatedTaskRoleOrApproval(t *testing.T) {
	for _, test := range []struct{ name, activity, reason, task, role string }{
		{"task", "agent.invoke@1", supervision.ErrCheckpointRequired.Error() + ": tool budget reached", "unrelated", "verified_integrator"},
		{"role", "agent.invoke@1", supervision.ErrCheckpointRequired.Error() + ": tool budget reached", "", "other_role"},
		{"evidence", "agent.invoke@1", "evidence invalid: missing receipt", "", "verified_integrator"},
		{"nonagent", "unit.close@1", supervision.ErrCheckpointRequired.Error() + ": tool budget reached", "", "verified_integrator"},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, store, approval, task := supervisorFixture(t, test.activity, test.reason)
			if test.task != "" {
				task = test.task
			}
			var out bytes.Buffer
			if err := SupervisorCLI(context.Background(), project, supervisorArgs(approval.ID, task, test.role), &out); err == nil {
				t.Fatal("unrelated approval was acknowledged")
			}
			approvals, err := store.ListApprovals(context.Background(), "checkpoint-run")
			if err != nil || approvals[0].Status != "pending" {
				t.Fatal("rejected command changed approval")
			}
			if _, err := os.Stat(filepath.Join(project, ".orquestalite", "runs", "checkpoint-run", "supervision-decisions.jsonl")); !os.IsNotExist(err) {
				t.Fatal("rejected command reset checkpoint counters")
			}
		})
	}
}
