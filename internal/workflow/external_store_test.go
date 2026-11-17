package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/external"
)

func externalStoreFixture(t *testing.T) (*Store, *StepRun, external.DispatchRequest) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	_, _, err = store.CreateRunOnce(ctx, CreateRunParams{ID: "external-run", FlowRef: "flow:external@1", IR: json.RawMessage(`{}`), Inputs: json.RawMessage(`{}`), Policy: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	step, err := store.EnsureStep(ctx, "external-run", "root", "employee", "", "external.wait@1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	return store, step, external.DispatchRequest{EmployeeID: "stable-agent", Instructions: "work", CompletionCondition: "completed with evidence", Deadline: time.Now().Add(time.Hour)}
}

func TestExternalPreparePinsEmployeeAndRecoversBeforeDispatch(t *testing.T) {
	store, step, request := externalStoreFixture(t)
	ctx := context.Background()
	first, err := store.PrepareExternal(ctx, step, "external.wait@1", request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Request.AttemptID != first.ID || first.Request.IdempotencyKey == "" || first.Status != "prepared" {
		t.Fatalf("invalid durable intent: %+v", first)
	}
	request.EmployeeID = "reconfigured-agent"
	next, err := store.PrepareExternal(ctx, step, "external.wait@1", request)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != first.ID || next.EmployeeID != "stable-agent" || next.Number != 1 {
		t.Fatalf("recovered assignment changed: %+v", next)
	}
	var count int
	if err = store.db.QueryRow(`SELECT COUNT(*) FROM attempts`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("attempts=%d err=%v", count, err)
	}
}

func TestExternalInboxPersistsBeforeDispatchAndDeduplicates(t *testing.T) {
	store, step, request := externalStoreFixture(t)
	ctx := context.Background()
	assignment, err := store.PrepareExternal(ctx, step, "external.wait@1", request)
	if err != nil {
		t.Fatal(err)
	}
	event := external.Event{ID: "callback-1", Type: "agent.run.finished"}
	pending, err := store.ReceiveExternalEvent(ctx, step.RunID, step.StepID, assignment.ID, event)
	if err != nil || !pending {
		t.Fatalf("receive=%v %v", pending, err)
	}
	pending, err = store.ReceiveExternalEvent(ctx, step.RunID, step.StepID, assignment.ID, event)
	if err != nil || pending {
		t.Fatalf("duplicate=%v %v", pending, err)
	}
	loaded, err := store.GetExternalAttempt(ctx, assignment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PendingEvents != 1 || loaded.Status != "prepared" {
		t.Fatalf("early event lost: %+v", loaded)
	}
	if err = store.SaveExternalDispatch(ctx, assignment.ID, external.References{IssueID: "issue-1"}); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.GetExternalAttempt(ctx, assignment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PendingEvents != 1 || loaded.References.IssueID != "issue-1" {
		t.Fatalf("pending callback lost: %+v", loaded)
	}
	if err = store.RecordExternalCheck(ctx, assignment.ID, external.Result{TaskStatus: "in_progress", References: external.References{IssueID: "issue-1", RunID: "run-2"}}, "waiting"); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.GetExternalAttempt(ctx, assignment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PendingEvents != 0 || loaded.LastCheckedAt == nil || loaded.References.RunID != "run-2" {
		t.Fatalf("check not durable: %+v", loaded)
	}
}

func TestExternalDecisionTransitionsExactlyOnce(t *testing.T) {
	store, step, request := externalStoreFixture(t)
	ctx := context.Background()
	assignment, err := store.PrepareExternal(ctx, step, "external.wait@1", request)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveExternalDispatch(ctx, assignment.ID, external.References{IssueID: "issue-1"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	decisions := make(chan bool, 2)
	failures := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			changed, err := store.DecideExternal(ctx, assignment.ID, external.Result{TaskStatus: "done", EmployeeID: "stable-agent", CostUSD: 1.25}, "success", "continue", json.RawMessage(`{"outcome":"success"}`), StepSucceeded)
			decisions <- changed
			failures <- err
		}()
	}
	wg.Wait()
	close(decisions)
	close(failures)
	count := 0
	for changed := range decisions {
		if changed {
			count++
		}
	}
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if count != 1 {
		t.Fatalf("transitions=%d", count)
	}
	persisted, err := store.GetStep(ctx, step.RunID, step.ScopePath, step.StepID, "")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetExternalAttempt(ctx, assignment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != StepSucceeded || loaded.Decision != "continue" || loaded.Status != "decided" {
		t.Fatalf("inconsistent atomic decision: step=%+v record=%+v", persisted, loaded)
	}
	var events int
	if err = store.db.QueryRow(`SELECT COUNT(*) FROM outbox_events WHERE json_extract(payload_json,'$.event')='external_decided'`).Scan(&events); err != nil || events != 1 {
		t.Fatalf("decisions in outbox=%d err=%v", events, err)
	}
}

func TestExternalRetryMakesOldEventsAndDecisionsInert(t *testing.T) {
	store, step, request := externalStoreFixture(t)
	ctx := context.Background()
	old, err := store.PrepareExternal(ctx, step, "external.wait@1", request)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RetryExternal(ctx, step.RunID, step.StepID, old.ID); err != nil {
		t.Fatal(err)
	}
	next, err := store.PrepareExternal(ctx, step, "external.wait@1", request)
	if err != nil {
		t.Fatal(err)
	}
	if old.ID == next.ID || next.Number != 2 || old.Request.IdempotencyKey == next.Request.IdempotencyKey {
		t.Fatalf("retry reused assignment old=%+v next=%+v", old, next)
	}
	changed, err := store.ReceiveExternalEvent(ctx, step.RunID, step.StepID, old.ID, external.Event{ID: "late", Type: "agent.run.finished"})
	if err != nil || changed {
		t.Fatalf("late callback active=%v err=%v", changed, err)
	}
	changed, err = store.DecideExternal(ctx, old.ID, external.Result{}, "success", "continue", json.RawMessage(`{}`), StepSucceeded)
	if err != nil || changed {
		t.Fatalf("old decision active=%v err=%v", changed, err)
	}
	loaded, err := store.GetExternalAttempt(ctx, next.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != "prepared" || loaded.Decision != "" {
		t.Fatalf("new attempt changed by stale callback: %+v", loaded)
	}
	var disposition string
	if err = store.db.QueryRow(`SELECT disposition FROM external_events WHERE attempt_id=?`, old.ID).Scan(&disposition); err != nil || disposition != "ignored" {
		t.Fatalf("late event not audited disposition=%s err=%v", disposition, err)
	}
}

func TestExternalPausedAndCancelledCannotDispatchOrAdvance(t *testing.T) {
	for _, control := range []RunStatus{RunPaused, RunCancelled} {
		t.Run(string(control), func(t *testing.T) {
			store, step, request := externalStoreFixture(t)
			ctx := context.Background()
			assignment, err := store.PrepareExternal(ctx, step, "external.wait@1", request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.db.Exec(`UPDATE workflow_runs SET status=? WHERE id=?`, control, step.RunID); err != nil {
				t.Fatal(err)
			}
			changed, err := store.DecideExternal(ctx, assignment.ID, external.Result{}, "success", "continue", json.RawMessage(`{}`), StepSucceeded)
			if err != nil || changed {
				t.Fatalf("controlled run advanced=%v err=%v", changed, err)
			}
			if _, err = store.PrepareExternal(ctx, step, "external.wait@1", request); !errors.Is(err, ErrExternalInactive) {
				t.Fatalf("controlled dispatch err=%v", err)
			}
			if err = store.SaveExternalDispatch(ctx, assignment.ID, external.References{IssueID: "accepted-during-control"}); err != nil {
				t.Fatalf("lost accepted reference after control: %v", err)
			}
			if err = store.SaveExternalCancellation(ctx, assignment.ID, external.Cancellation{Accepted: true}); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.GetExternalAttempt(ctx, assignment.ID)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Cancellation == nil || !loaded.Cancellation.Accepted {
				t.Fatal("cancellation acknowledgement missing")
			}
		})
	}
}

func TestExternalRoutedFailureRetainsFailedAttempt(t *testing.T) {
	store, step, request := externalStoreFixture(t)
	ctx := context.Background()
	assignment, err := store.PrepareExternal(ctx, step, "external.wait@1", request)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := store.DecideExternal(ctx, assignment.ID, external.Result{TaskStatus: "cancelled"}, "task_cancelled", "continue", json.RawMessage(`{"outcome":"task_cancelled"}`), StepRouted)
	if err != nil || !changed {
		t.Fatalf("routed=%v err=%v", changed, err)
	}
	attempt, err := store.LatestAttempt(ctx, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Status != AttemptFailed {
		t.Fatalf("failure recorded as %s", attempt.Status)
	}
}
