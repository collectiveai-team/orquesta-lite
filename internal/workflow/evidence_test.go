package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
)

type checkpointEvidenceExecutor struct {
	spec        activity.Spec
	executes    int
	checkpoints int
	proof       string
}

func (e *checkpointEvidenceExecutor) Spec() activity.Spec { return e.spec }
func (e *checkpointEvidenceExecutor) Execute(_ context.Context, request activity.Request) (activity.Result, error) {
	e.executes++
	if e.executes <= e.checkpoints {
		return activity.Result{}, &activity.ApprovalRequiredError{Reason: fmt.Sprintf("checkpoint %d", e.executes), Input: request.Inputs}
	}
	result := activity.Result{Output: json.RawMessage(`{"ok":true}`)}
	if e.proof != "" {
		if err := os.WriteFile(e.proof, []byte("runtime proof"), 0600); err != nil {
			return result, err
		}
		result.Artifacts = []activity.Artifact{{Kind: "test_proof", URI: e.proof, Digest: "runtime proof"}}
	}
	return result, nil
}
func (e *checkpointEvidenceExecutor) ValidateEvidence(_ context.Context, request activity.Request, result activity.Result) error {
	if e.proof == "" {
		return nil
	}
	if request.Attempt < 1 || len(result.Artifacts) != 1 || result.Artifacts[0].URI != e.proof {
		return fmt.Errorf("runtime proof metadata unavailable")
	}
	raw, err := os.ReadFile(e.proof)
	if err != nil {
		return err
	}
	if string(raw) != "runtime proof" {
		return fmt.Errorf("runtime proof corrupt")
	}
	return nil
}

func checkpointRuntime(t *testing.T, checkpoints int, proof string) (*Runtime, *checkpointEvidenceExecutor) {
	t.Helper()
	_, catalog := recoveryIR(t, activity.EffectAtMostOnce)
	store, err := Open(filepath.Join(t.TempDir(), "workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	executor := &checkpointEvidenceExecutor{spec: activity.Spec{Name: "test.effect", Version: "1", Effect: activity.EffectAtMostOnce}, checkpoints: checkpoints, proof: proof}
	registry := activity.NewRegistry()
	if err := registry.Register(executor); err != nil {
		t.Fatal(err)
	}
	return &Runtime{Store: store, Activities: registry, Catalog: catalog}, executor
}

func onlyCheckpointApproval(t *testing.T, runtime *Runtime, runID string) Approval {
	t.Helper()
	approvals, err := runtime.Store.ListApprovals(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(approvals) != 1 {
		t.Fatalf("approvals=%+v", approvals)
	}
	return approvals[0]
}

func TestCheckpointPendingAndRejectedNeverRepeatInvoke(t *testing.T) {
	runtime, executor := checkpointRuntime(t, 1, "")
	ir, _ := recoveryIR(t, activity.EffectAtMostOnce)
	ctx := context.Background()
	run, err := runtime.Start(ctx, ir, StartOptions{RunID: "checkpoint-pending", Policy: DefaultPolicy(), Inputs: map[string]any{}})
	if !errors.Is(err, ErrNeedsHuman) || run.Status != RunNeedsHuman {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	for i := 0; i < 2; i++ {
		resumed, err := runtime.Resume(ctx, run.ID)
		if !errors.Is(err, ErrNeedsHuman) || resumed.Status != RunNeedsHuman || executor.executes != 1 {
			t.Fatalf("pending checkpoint repeated invoke: run=%+v err=%v calls=%d", resumed, err, executor.executes)
		}
	}
	approval := onlyCheckpointApproval(t, runtime, run.ID)
	if approval.Status != "pending" {
		t.Fatalf("approval=%+v", approval)
	}
	if err := runtime.Store.DecideApproval(ctx, run.ID, approval.ID, "reject"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		resumed, err := runtime.Resume(ctx, run.ID)
		if err == nil || !strings.Contains(err.Error(), "rejected") || executor.executes != 1 {
			t.Fatalf("rejected checkpoint repeated invoke: run=%+v err=%v calls=%d", resumed, err, executor.executes)
		}
	}
}

func TestCheckpointAfterApprovalReopensActualApprovalID(t *testing.T) {
	runtime, executor := checkpointRuntime(t, 2, "")
	ir, _ := recoveryIR(t, activity.EffectAtMostOnce)
	ctx := context.Background()
	run, err := runtime.Start(ctx, ir, StartOptions{RunID: "checkpoint-reopen", Policy: DefaultPolicy(), Inputs: map[string]any{}})
	if !errors.Is(err, ErrNeedsHuman) {
		t.Fatal(err)
	}
	first := onlyCheckpointApproval(t, runtime, run.ID)
	if err := runtime.Store.DecideApproval(ctx, run.ID, first.ID, "approve"); err != nil {
		t.Fatal(err)
	}
	run, err = runtime.Resume(ctx, run.ID)
	if !errors.Is(err, ErrNeedsHuman) || run.Status != RunNeedsHuman || executor.executes != 2 {
		t.Fatalf("run=%+v err=%v calls=%d", run, err, executor.executes)
	}
	second := onlyCheckpointApproval(t, runtime, run.ID)
	if second.ID != first.ID || second.Status != "pending" || second.Decision != "" || second.DecidedAt != nil || second.Reason != "checkpoint 2" {
		t.Fatalf("checkpoint was not reopened: first=%+v second=%+v", first, second)
	}
	events, err := runtime.Store.ListEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	requested := 0
	for _, raw := range events {
		var event struct {
			Event      string `json:"event"`
			ApprovalID string `json:"approval_id"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		if event.Event == "approval_requested" {
			requested++
			if event.ApprovalID != first.ID {
				t.Fatalf("event points at nonexistent approval %s; actual=%s", event.ApprovalID, first.ID)
			}
		}
	}
	if requested != 2 {
		t.Fatalf("approval_requested events=%d want 2", requested)
	}
	if err := runtime.Store.DecideApproval(ctx, run.ID, second.ID, "approve"); err != nil {
		t.Fatal(err)
	}
	run, err = runtime.Resume(ctx, run.ID)
	if err != nil || run.Status != RunSucceeded || executor.executes != 3 {
		t.Fatalf("run=%+v err=%v calls=%d", run, err, executor.executes)
	}
}

func TestEvidenceIdempotentStartRevalidatesWithoutReplayingEffect(t *testing.T) {
	proof := filepath.Join(t.TempDir(), "receipt")
	runtime, executor := checkpointRuntime(t, 0, proof)
	ir, _ := recoveryIR(t, activity.EffectAtMostOnce)
	ctx := context.Background()
	run, err := runtime.Start(ctx, ir, StartOptions{RunID: "evidence-first", SourceKey: "same-trigger", Policy: DefaultPolicy(), Inputs: map[string]any{}})
	if err != nil || run.Status != RunSucceeded || executor.executes != 1 {
		t.Fatalf("run=%+v err=%v calls=%d", run, err, executor.executes)
	}
	if err := os.Remove(proof); err != nil {
		t.Fatal(err)
	}
	reused, err := runtime.Start(ctx, ir, StartOptions{RunID: "must-not-create", SourceKey: "same-trigger", Policy: DefaultPolicy(), Inputs: map[string]any{}})
	if !errors.Is(err, ErrNeedsHuman) || reused.ID != run.ID || reused.Status != RunNeedsHuman || executor.executes != 1 {
		t.Fatalf("lost evidence reused as success or replayed: run=%+v err=%v calls=%d", reused, err, executor.executes)
	}
	runs, err := runtime.Store.ListRuns(ctx, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("idempotent trigger created another run: %+v err=%v", runs, err)
	}
	approval := onlyCheckpointApproval(t, runtime, run.ID)
	if err := os.WriteFile(proof, []byte("runtime proof"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Store.DecideApproval(ctx, run.ID, approval.ID, "approve"); err != nil {
		t.Fatal(err)
	}
	resumed, err := runtime.Resume(ctx, run.ID)
	if err != nil || resumed.Status != RunSucceeded || executor.executes != 1 {
		t.Fatalf("restored evidence replayed completed effect: status=%s err=%v calls=%d", resumed.Status, err, executor.executes)
	}
}
