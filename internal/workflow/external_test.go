package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/activity/builtin"
	"github.com/collectiveai-team/orquesta-lite/internal/external"
	"github.com/collectiveai-team/orquesta-lite/internal/flow"
)

type externalFake struct {
	mu                                         sync.Mutex
	employee                                   string
	resolved, dispatched, inspected, cancelled int
	requests                                   []external.DispatchRequest
	result                                     external.Result
	inspectErr                                 error
	dispatchHook                               func(external.DispatchRequest)
	dispatchErr                                error
	cancelResult                               *external.Cancellation
	cancelErr                                  error
	inspectHook                                func(context.Context)
}

func (f *externalFake) ResolveEmployee(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolved++
	return f.employee, nil
}
func (f *externalFake) Dispatch(_ context.Context, req external.DispatchRequest) (external.References, error) {
	f.mu.Lock()
	f.dispatched++
	f.requests = append(f.requests, req)
	hook, err := f.dispatchHook, f.dispatchErr
	f.mu.Unlock()
	if hook != nil {
		hook(req)
	}
	return external.References{IssueID: "issue-1", RunID: "run-1"}, err
}
func (f *externalFake) Inspect(ctx context.Context, _ external.References) (external.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspected++
	if f.inspectHook != nil {
		f.inspectHook(ctx)
	}
	return f.result, f.inspectErr
}

func TestExternalCodingResultKeepsDownstreamContract(t *testing.T) {
	r, ir, _, _, _ := externalRuntimeFixture(t, map[string]any{"outputMode": "deliverables"})
	ir.Steps = ir.Steps[:2]
	ir.Outputs = map[string]flow.Value{"approved": {Ref: &flow.Ref{Path: "steps.external.output.approved"}}}
	startExternalFixture(t, r, ir)
	run, err := r.ReconcileExternal(context.Background(), "mixed")
	if err != nil || run.Status != RunSucceeded {
		t.Fatalf("coding result changed contract: %+v %v", run, err)
	}
	step, err := r.Store.GetStep(context.Background(), "mixed", "root", "external", "")
	if err != nil || string(step.Output) != `{"approved":true}` {
		t.Fatalf("output=%s err=%v", step.Output, err)
	}
}

func TestExternalHostShutdownWhileInspectingPreservesWait(t *testing.T) {
	r, ir, fake, _, _ := externalRuntimeFixture(t, nil)
	startExternalFixture(t, r, ir)
	ctx, cancel := context.WithCancel(context.Background())
	fake.inspectHook = func(context.Context) { cancel() }
	fake.inspectErr = context.Canceled
	run, err := r.ReconcileExternal(ctx, "mixed")
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	run, err = r.Store.GetRun(context.Background(), "mixed")
	if err != nil || run.Status != RunWaitingExternal {
		t.Fatalf("host shutdown lost wait: %+v %v", run, err)
	}
	fake.inspectHook = nil
	fake.inspectErr = nil
	run, err = r.ReconcileExternal(context.Background(), "mixed")
	if err != nil || run.Status != RunSucceeded || fake.dispatched != 1 {
		t.Fatalf("recovery=%+v %v", run, err)
	}
}
func (f *externalFake) Cancel(context.Context, external.References) (external.Cancellation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled++
	if f.cancelErr != nil {
		return external.Cancellation{}, f.cancelErr
	}
	if f.cancelResult != nil {
		return *f.cancelResult, nil
	}
	return external.Cancellation{Accepted: true}, nil
}

func TestExternalRetryCannotOverlapUnconfirmedWork(t *testing.T) {
	for _, reason := range []string{"unavailable", "rejected", "uncertain-dispatch", "cancelled-task-live-run"} {
		t.Run(reason, func(t *testing.T) {
			r, ir, fake, _, _ := externalRuntimeFixture(t, nil)
			if reason == "uncertain-dispatch" {
				fake.dispatchErr = &external.UnavailableError{Operation: "dispatch"}
			}
			record := startExternalFixture(t, r, ir)
			fake.cancelResult = &external.Cancellation{Accepted: false}
			if reason == "unavailable" {
				fake.cancelErr = &external.UnavailableError{Operation: "cancel"}
			}
			if reason == "cancelled-task-live-run" {
				fake.result.TaskStatus = "cancelled"
				fake.result.RunStatus = "running"
				_, err := r.ReconcileExternal(context.Background(), "mixed")
				if !errors.Is(err, ErrNeedsHuman) {
					t.Fatalf("cancelled live run=%v", err)
				}
			}
			if err := r.RetryExternal(context.Background(), "mixed", record.ID); !errors.Is(err, ErrNeedsHuman) {
				t.Fatalf("retry permitted: %v", err)
			}
			records, err := r.Store.ListExternalAttempts(context.Background(), "mixed")
			if err != nil || len(records) != 1 || fake.dispatched != 1 {
				t.Fatalf("duplicate assignment: %+v %v", records, err)
			}
			step, _ := r.Store.GetStep(context.Background(), "mixed", "root", "external", "")
			if step.Status == StepReady {
				t.Fatal("retry readied an overlapping attempt")
			}
		})
	}
}

func externalRuntimeFixture(t *testing.T, overrides map[string]any) (*Runtime, *flow.IR, *externalFake, *echoExecutor, string) {
	t.Helper()
	with := map[string]any{"adapter": "paperclip", "employee": "alice", "instructions": "work", "completionCondition": "approved evidence", "timeoutSeconds": 3600, "completionGate": "item.deliverables.approved == true", "outputSchema": "schema:external-output@1"}
	for key, value := range overrides {
		with[key] = value
	}
	rawWith, _ := json.Marshal(with)
	doc, err := flow.Decode(strings.NewReader(fmt.Sprintf(`{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"mixed-external","version":"1"},"steps":[{"id":"before","uses":"activity:test.echo@1"},{"id":"external","uses":"activity:external.dispatch@1","with":%s},{"id":"after","uses":"activity:test.echo@1","if":"steps.external.output.outcome == \"success\""},{"id":"failed_route","uses":"activity:test.echo@1","if":"steps.external.output.outcome != \"success\""}]}`, rawWith)))
	if err != nil {
		t.Fatal(err)
	}
	catalog := newMemoryCatalog()
	echo := &echoExecutor{}
	registry := activity.NewRegistry()
	for _, exec := range []activity.Executor{echo, builtin.ExternalExecutor{}} {
		if err := registry.Register(exec); err != nil {
			t.Fatal(err)
		}
		catalog.Activities["activity:"+exec.Spec().Ref()] = exec.Spec()
	}
	catalog.Schemas["schema:external-output@1"] = &flow.Schema{Type: flow.Types{"object"}, Required: []string{"approved"}, Properties: map[string]*flow.Schema{"approved": {Type: flow.Types{"boolean"}}}}
	ir, diags := flow.Compile(doc, catalog)
	if diags.HasErrors() {
		t.Fatalf("compile: %+v", diags)
	}
	path := filepath.Join(t.TempDir(), "workflows.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	fake := &externalFake{employee: "employee-1", result: external.Result{TaskStatus: "done", RunStatus: "succeeded", EmployeeID: "employee-1", Deliverables: json.RawMessage(`{"approved":true}`), Evidence: []string{"artifact://proof"}, CostUSD: 0.25, InputTokens: 10, OutputTokens: 5}}
	runtime := &Runtime{Store: store, Activities: registry, Catalog: catalog, ExternalAdapters: map[string]external.Adapter{"paperclip": fake}}
	t.Cleanup(func() { runtime.Store.Close() })
	return runtime, ir, fake, echo, path
}

func startExternalFixture(t *testing.T, r *Runtime, ir *flow.IR) *ExternalAttempt {
	t.Helper()
	run, err := r.Start(context.Background(), ir, StartOptions{RunID: "mixed", Policy: DefaultPolicy()})
	if err != nil || run.Status != RunWaitingExternal {
		t.Fatalf("start run=%+v err=%v", run, err)
	}
	records, err := r.Store.ListExternalAttempts(context.Background(), run.ID)
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	return &records[0]
}

func TestExternalMixedRunRestartsAndDuplicateCallbacksTransitionOnce(t *testing.T) {
	r, ir, fake, echo, path := externalRuntimeFixture(t, nil)
	record := startExternalFixture(t, r, ir)
	if echo.calls != 1 {
		t.Fatalf("sync before calls=%d", echo.calls)
	}
	if err := r.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	r.Store = store
	fake.employee = "reconfigured-employee"
	run, err := r.ResumeExternal(context.Background(), "mixed", "external", record.ID, external.Event{ID: "finished", Type: "agent.run.finished"})
	if err != nil || run.Status != RunSucceeded {
		t.Fatalf("resume run=%+v err=%v", run, err)
	}
	_, err = r.ResumeExternal(context.Background(), "mixed", "external", record.ID, external.Event{ID: "finished", Type: "agent.run.finished"})
	if err != nil {
		t.Fatal(err)
	}
	if fake.resolved != 1 || fake.dispatched != 1 || echo.calls != 2 {
		t.Fatalf("resolve=%d dispatch=%d sync=%d", fake.resolved, fake.dispatched, echo.calls)
	}
	var decisions int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM outbox_events WHERE json_extract(payload_json,'$.event')='external_decided'`).Scan(&decisions); err != nil || decisions != 1 {
		t.Fatalf("decisions=%d err=%v", decisions, err)
	}
	updated, _ := store.GetExternalAttempt(context.Background(), record.ID)
	if updated.Result == nil || updated.Result.CostUSD != 0.25 || updated.Result.InputTokens != 10 {
		t.Fatalf("usage=%+v", updated)
	}
}

func TestExternalFinishedRunWithOpenIssueWaitsAndReconciliationCompletes(t *testing.T) {
	r, ir, fake, echo, _ := externalRuntimeFixture(t, nil)
	record := startExternalFixture(t, r, ir)
	fake.result.TaskStatus = "in_progress"
	run, err := r.ResumeExternal(context.Background(), "mixed", "external", record.ID, external.Event{Type: "agent.run.finished"})
	if err != nil || run.Status != RunWaitingExternal || echo.calls != 1 {
		t.Fatalf("run=%+v err=%v calls=%d", run, err, echo.calls)
	}
	fake.result.TaskStatus = "done"
	run, err = r.ReconcileExternal(context.Background(), "mixed")
	if err != nil || run.Status != RunSucceeded || fake.dispatched != 1 || echo.calls != 2 {
		t.Fatalf("run=%+v err=%v", run, err)
	}
}

func TestExternalEarlyCallbackPersistsWithoutTakingDispatchLock(t *testing.T) {
	r, ir, fake, _, _ := externalRuntimeFixture(t, nil)
	fake.dispatchHook = func(req external.DispatchRequest) {
		run, err := r.ResumeExternal(context.Background(), req.FlowRunID, req.StepID, req.AttemptID, external.Event{ID: "early", Type: "agent.run.finished"})
		if err != nil || run == nil {
			t.Errorf("callback: run=%+v err=%v", run, err)
		}
	}
	record := startExternalFixture(t, r, ir)
	if record.PendingEvents != 1 {
		t.Fatalf("early inbox=%+v", record)
	}
	run, err := r.ReconcileExternal(context.Background(), "mixed")
	if err != nil || run.Status != RunSucceeded || fake.dispatched != 1 {
		t.Fatalf("run=%+v err=%v", run, err)
	}
}

func TestExternalOutcomesAreExplicitAndCanRouteFailure(t *testing.T) {
	for _, outcome := range []string{"timeout", "cancelled", "blocked", "failed", "unavailable", "invalid"} {
		t.Run(outcome, func(t *testing.T) {
			r, ir, fake, echo, _ := externalRuntimeFixture(t, map[string]any{"routes": map[string]string{outcome: "continue"}})
			record := startExternalFixture(t, r, ir)
			switch outcome {
			case "timeout":
				r.Now = func() time.Time { return record.Request.Deadline.Add(time.Second) }
			case "cancelled":
				fake.result.TaskStatus = "cancelled"
			case "blocked":
				fake.result.EmployeeStatus = "paused"
			case "failed":
				fake.result.RunStatus = "failed"
			case "unavailable":
				fake.inspectErr = &external.UnavailableError{Operation: "inspect"}
			case "invalid":
				fake.result.Deliverables = json.RawMessage(`{"approved":false}`)
			}
			run, err := r.ReconcileExternal(context.Background(), "mixed")
			if err != nil || run.Status != RunSucceeded {
				t.Fatalf("run=%+v err=%v", run, err)
			}
			step, _ := r.Store.GetStep(context.Background(), "mixed", "root", "external", "")
			updated, _ := r.Store.GetExternalAttempt(context.Background(), record.ID)
			if step.Status != StepRouted || updated.Outcome != outcome || echo.calls != 2 {
				t.Fatalf("step=%+v record=%+v sync=%d", step, updated, echo.calls)
			}
			attempt, _ := r.Store.LatestAttempt(context.Background(), step.ID)
			if attempt.Status != AttemptFailed {
				t.Fatalf("failure became success: %+v", attempt)
			}
		})
	}
}

func TestExternalUnavailableWaitsThenTimeoutRequiresIntervention(t *testing.T) {
	r, ir, fake, _, _ := externalRuntimeFixture(t, nil)
	record := startExternalFixture(t, r, ir)
	fake.inspectErr = &external.UnavailableError{Operation: "inspect"}
	run, err := r.ReconcileExternal(context.Background(), "mixed")
	if err != nil || run.Status != RunWaitingExternal {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	r.Now = func() time.Time { return record.Request.Deadline.Add(time.Second) }
	run, err = r.ReconcileExternal(context.Background(), "mixed")
	if !errors.Is(err, ErrNeedsHuman) || run.Status != RunNeedsHuman {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	if fake.cancelled != 1 {
		t.Fatalf("cancellations=%d", fake.cancelled)
	}
}

func TestExternalPauseCancelRetryAndOldCallback(t *testing.T) {
	r, ir, fake, _, _ := externalRuntimeFixture(t, nil)
	record := startExternalFixture(t, r, ir)
	ctx := context.Background()
	if err := r.Pause(ctx, "mixed"); err != nil {
		t.Fatal(err)
	}
	run, err := r.ResumeExternal(ctx, "mixed", "external", record.ID, external.Event{ID: "while-paused", Type: "finished"})
	if err != nil || run.Status != RunPaused || fake.inspected != 0 {
		t.Fatalf("paused run=%+v err=%v", run, err)
	}
	if err := r.RetryExternal(ctx, "mixed", record.ID); err != nil {
		t.Fatal(err)
	}
	run, err = r.Resume(ctx, "mixed")
	if err != nil || run.Status != RunWaitingExternal {
		t.Fatalf("retry run=%+v err=%v", run, err)
	}
	records, _ := r.Store.ListExternalAttempts(ctx, "mixed")
	if len(records) != 2 || records[0].ID == records[1].ID || fake.requests[0].IdempotencyKey == fake.requests[1].IdempotencyKey {
		t.Fatalf("attempt history=%+v", records)
	}
	run, err = r.ResumeExternal(ctx, "mixed", "external", record.ID, external.Event{ID: "late", Type: "finished"})
	if err != nil || run.Status != RunWaitingExternal || fake.inspected != 0 {
		t.Fatalf("old callback advanced run=%+v err=%v", run, err)
	}
	if err := r.CancelExternal(ctx, "mixed"); err != nil {
		t.Fatal(err)
	}
	run, err = r.ReconcileExternal(ctx, "mixed")
	if err != nil || run.Status != RunCancelled || fake.dispatched != 2 || fake.cancelled < 1 {
		t.Fatalf("cancel run=%+v err=%v", run, err)
	}
}

func TestExternalUnknownDispatchReusesPinnedIntentAfterCrash(t *testing.T) {
	r, ir, fake, _, path := externalRuntimeFixture(t, nil)
	fake.dispatchErr = &external.UnavailableError{Operation: "lost dispatch acknowledgement"}
	record := startExternalFixture(t, r, ir)
	if record.References.IssueID != "" || record.Status != "prepared" {
		t.Fatalf("uncertain=%+v", record)
	}
	if err := r.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	r.Store = store
	fake.employee = "different-config"
	fake.dispatchErr = nil
	run, err := r.ReconcileExternal(context.Background(), "mixed")
	if err != nil || run.Status != RunWaitingExternal {
		t.Fatalf("recovery=%+v err=%v", run, err)
	}
	if fake.resolved != 1 || len(fake.requests) != 2 || !reflect.DeepEqual(fake.requests[0], fake.requests[1]) {
		t.Fatalf("recovery changed durable request: %+v", fake.requests)
	}
	run, err = r.ReconcileExternal(context.Background(), "mixed")
	if err != nil || run.Status != RunSucceeded || fake.dispatched != 2 {
		t.Fatalf("completion=%+v err=%v", run, err)
	}
}

func TestExternalUnknownDispatchBeyondDeduplicationWindowDoesNotResend(t *testing.T) {
	r, ir, fake, _, _ := externalRuntimeFixture(t, map[string]any{"timeoutSeconds": 365 * 24 * 60 * 60})
	fake.dispatchErr = &external.UnavailableError{Operation: "lost dispatch acknowledgement"}
	record := startExternalFixture(t, r, ir)
	r.Now = func() time.Time { return record.CreatedAt.Add(6*24*time.Hour + time.Second) }
	run, err := r.ReconcileExternal(context.Background(), "mixed")
	if !errors.Is(err, ErrNeedsHuman) || run.Status != RunNeedsHuman || fake.dispatched != 1 {
		t.Fatalf("unsafe resend: run=%+v err=%v dispatched=%d", run, err, fake.dispatched)
	}
}

func TestExternalValidationRejectsMissingEvidenceSchemaAndEmployee(t *testing.T) {
	for _, invalid := range []string{"evidence", "schema", "employee", "null", "usage"} {
		t.Run(invalid, func(t *testing.T) {
			r, ir, fake, echo, _ := externalRuntimeFixture(t, nil)
			startExternalFixture(t, r, ir)
			switch invalid {
			case "evidence":
				fake.result.Evidence = nil
			case "schema":
				fake.result.Deliverables = json.RawMessage(`{"approved":"true"}`)
			case "employee":
				fake.result.EmployeeID = "another-employee"
			case "null":
				fake.result.Deliverables = json.RawMessage(`null`)
			case "usage":
				negative := -1.0
				fake.result.Usage = &external.Usage{CostUSD: &negative}
			}
			run, err := r.ReconcileExternal(context.Background(), "mixed")
			if !errors.Is(err, ErrNeedsHuman) || run.Status != RunNeedsHuman || echo.calls != 1 {
				t.Fatalf("invalid result advanced: %+v %v", run, err)
			}
		})
	}
}

func TestExternalReconciliationAtAttemptBudgetStillInspects(t *testing.T) {
	r, ir, fake, _, _ := externalRuntimeFixture(t, nil)
	// This flow spends one synchronous and one external attempt. No new
	// attempts may be created, but inspecting the external one must still work.
	ir.Steps = ir.Steps[:2]
	policy := DefaultPolicy()
	policy.MaxAttempts = 2
	run, err := r.Start(context.Background(), ir, StartOptions{RunID: "mixed", Policy: policy})
	if err != nil || run.Status != RunWaitingExternal {
		t.Fatalf("start=%+v %v", run, err)
	}
	run, err = r.ReconcileExternal(context.Background(), "mixed")
	if err != nil || run.Status != RunSucceeded || fake.inspected != 1 {
		t.Fatalf("inspection consumed attempt budget: %+v %v inspect=%d", run, err, fake.inspected)
	}
}

type pauseOnEvidence struct {
	runtime *Runtime
	enabled bool
}

func (e *pauseOnEvidence) Spec() activity.Spec { return (&echoExecutor{}).Spec() }
func (e *pauseOnEvidence) Execute(context.Context, activity.Request) (activity.Result, error) {
	return activity.Result{Output: json.RawMessage(`{}`)}, nil
}
func (e *pauseOnEvidence) ValidateEvidence(ctx context.Context, _ activity.Request, _ activity.Result) error {
	if e.enabled {
		e.enabled = false
		return e.runtime.Pause(ctx, "mixed")
	}
	return nil
}

func TestExternalReconciliationDoesNotClearPauseDuringEvidenceValidation(t *testing.T) {
	r, ir, fake, _, _ := externalRuntimeFixture(t, nil)
	startExternalFixture(t, r, ir)
	pauser := &pauseOnEvidence{runtime: r, enabled: true}
	registry := activity.NewRegistry()
	_ = registry.Register(pauser)
	_ = registry.Register(builtin.ExternalExecutor{})
	r.Activities = registry
	run, err := r.ReconcileExternal(context.Background(), "mixed")
	if err != nil || run.Status != RunPaused || fake.inspected != 0 {
		t.Fatalf("pause overwritten: %+v %v", run, err)
	}
}

func TestExternalPeriodicRecoveryContinuesAfterDecisionCommit(t *testing.T) {
	r, ir, fake, echo, _ := externalRuntimeFixture(t, nil)
	record := startExternalFixture(t, r, ir)
	output, _ := json.Marshal(map[string]any{"outcome": "success", "decision": "continue", "result": fake.result})
	applied, err := r.Store.DecideExternal(context.Background(), record.ID, fake.result, "success", "continue", output, StepSucceeded)
	if err != nil || !applied {
		t.Fatalf("decision=%v %v", applied, err)
	}
	// Simulate process death here, before executeIR can schedule `after`.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err = r.RunExternalReconciler(ctx, time.Hour)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reconciler=%v", err)
	}
	run, err := r.Store.GetRun(context.Background(), "mixed")
	if err != nil || run.Status != RunSucceeded || echo.calls != 2 || fake.dispatched != 1 || fake.inspected != 0 {
		t.Fatalf("continuation stranded: %+v %v calls=%d", run, err, echo.calls)
	}
}

func TestExternalWorkflowDeadlineCancelsRemoteAndRecordsTimeout(t *testing.T) {
	r, ir, fake, _, _ := externalRuntimeFixture(t, nil)
	record := startExternalFixture(t, r, ir)
	policy := DefaultPolicy()
	policy.MaxDurationSeconds = 1
	raw, _ := json.Marshal(policy)
	_, err := r.Store.db.Exec(`UPDATE workflow_runs SET policy_json=?,started_at=? WHERE id='mixed'`, raw, stamp(time.Now().Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	run, err := r.ReconcileExternal(context.Background(), "mixed")
	if !errors.Is(err, context.DeadlineExceeded) || run.Status != RunCancelled || fake.cancelled != 1 {
		t.Fatalf("policy deadline left remote running: %+v %v cancelled=%d", run, err, fake.cancelled)
	}
	updated, _ := r.Store.GetExternalAttempt(context.Background(), record.ID)
	if updated.Outcome != "timeout" || updated.Cancellation == nil || !updated.Cancellation.Accepted {
		t.Fatalf("missing timeout evidence: %+v", updated)
	}
}

func TestExternalFailureHandlerSuspendsBeforeCompensation(t *testing.T) {
	r, ir, _, echo, _ := externalRuntimeFixture(t, nil)
	externalStep := ir.Steps[1]
	failing := &recordingExecutor{}
	if err := r.Activities.Register(failing); err != nil {
		t.Fatal(err)
	}
	before := ir.Steps[0]
	before.Compensate = &flow.IRHandler{Uses: before.Uses, Activity: before.Activity}
	spec := failing.Spec()
	ir.Steps = []flow.IRStep{before, {ID: "fails", Uses: flow.ResourceRef{Kind: "activity", Name: spec.Name, Version: spec.Version}, Activity: &spec, With: map[string]flow.Value{"name": {Literal: "fail"}}, OnError: &flow.IRHandler{Uses: externalStep.Uses, Activity: externalStep.Activity, With: externalStep.With}}}
	startExternalFixture(t, r, ir)
	if echo.calls != 1 {
		t.Fatalf("compensated before asynchronous failure handler completed: calls=%d", echo.calls)
	}
}
