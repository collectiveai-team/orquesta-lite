package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/flow"
)

type recoveryExecutor struct {
	spec                 activity.Spec
	executes, reconciles int
	reconcile            activity.ReconcileStatus
	failExecutes         int
}

func (e *recoveryExecutor) Spec() activity.Spec { return e.spec }
func (e *recoveryExecutor) Execute(_ context.Context, request activity.Request) (activity.Result, error) {
	e.executes++
	result := activity.Result{Output: json.RawMessage(`{"ok":true}`), Receipt: request.IdempotencyKey}
	if e.executes <= e.failExecutes {
		return result, &activity.Error{Class: activity.ErrorTransient, Op: "test.effect", Err: errors.New("uncertain transient failure")}
	}
	return result, nil
}
func (e *recoveryExecutor) Reconcile(_ context.Context, request activity.ReconcileRequest) (activity.ReconcileResult, error) {
	e.reconciles++
	return activity.ReconcileResult{Status: e.reconcile, Result: &activity.Result{Output: json.RawMessage(`{"ok":true}`), Receipt: request.IdempotencyKey}}, nil
}

func recoveryIR(t *testing.T, effect activity.EffectMode) (*flow.IR, *memoryCatalog) {
	t.Helper()
	doc, err := flow.Decode(strings.NewReader(`{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"recover","version":"1"},"steps":[{"id":"effect","uses":"activity:test.effect@1"}],"outputs":{"ok":{"$ref":"steps.effect.output.ok"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	catalog := newMemoryCatalog()
	catalog.Activities["activity:test.effect@1"] = activity.Spec{Name: "test.effect", Version: "1", Effect: effect}
	ir, diagnostics := flow.Compile(doc, catalog)
	if diagnostics.HasErrors() {
		t.Fatalf("%+v", diagnostics)
	}
	return ir, catalog
}

func interruptedRun(t *testing.T, store *Store, ir *flow.IR) (*StepRun, *Attempt) {
	t.Helper()
	ctx := context.Background()
	irRaw, _ := json.Marshal(ir)
	policyRaw, _ := json.Marshal(DefaultPolicy())
	if _, _, err := store.CreateRunOnce(ctx, CreateRunParams{ID: "r", FlowRef: "flow:recover@1", DefinitionHash: string(ir.Digest), IR: irRaw, Inputs: json.RawMessage(`{}`), Policy: policyRaw}); err != nil {
		t.Fatal(err)
	}
	step, err := store.EnsureStep(ctx, "r", "root", "effect", "", "activity:test.effect@1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := store.StartAttempt(ctx, step, "test.effect@1", "r/root/effect/")
	if err != nil {
		t.Fatal(err)
	}
	return step, attempt
}

func TestResumeReconcilesBeforeRepeating(t *testing.T) {
	ir, catalog := recoveryIR(t, activity.EffectReconcilable)
	store, _ := Open(filepath.Join(t.TempDir(), "w.db"))
	defer store.Close()
	interruptedRun(t, store, ir)
	executor := &recoveryExecutor{spec: activity.Spec{Name: "test.effect", Version: "1", Effect: activity.EffectReconcilable}, reconcile: activity.ReconcileApplied}
	registry := activity.NewRegistry()
	if err := registry.Register(executor); err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{Store: store, Activities: registry, Catalog: catalog}
	run, err := runtime.Resume(context.Background(), "r")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != RunSucceeded || executor.reconciles != 1 || executor.executes != 0 {
		t.Fatalf("run=%s reconcile=%d execute=%d", run.Status, executor.reconciles, executor.executes)
	}
}

func TestAtMostOnceInterruptedNeedsHuman(t *testing.T) {
	ir, catalog := recoveryIR(t, activity.EffectAtMostOnce)
	store, _ := Open(filepath.Join(t.TempDir(), "w.db"))
	defer store.Close()
	interruptedRun(t, store, ir)
	executor := &recoveryExecutor{spec: activity.Spec{Name: "test.effect", Version: "1", Effect: activity.EffectAtMostOnce}}
	registry := activity.NewRegistry()
	_ = registry.Register(executor)
	runtime := &Runtime{Store: store, Activities: registry, Catalog: catalog}
	run, err := runtime.Resume(context.Background(), "r")
	if !errors.Is(err, ErrNeedsHuman) {
		t.Fatalf("err=%v", err)
	}
	if run.Status != RunNeedsHuman || executor.executes != 0 {
		t.Fatalf("run=%s execute=%d", run.Status, executor.executes)
	}
}

func TestAtMostOnceLiveErrorIsNeverRetried(t *testing.T) {
	ir, catalog := recoveryIR(t, activity.EffectAtMostOnce)
	store, _ := Open(filepath.Join(t.TempDir(), "w.db"))
	defer store.Close()
	executor := &recoveryExecutor{spec: activity.Spec{Name: "test.effect", Version: "1", Effect: activity.EffectAtMostOnce}, failExecutes: 10}
	registry := activity.NewRegistry()
	_ = registry.Register(executor)
	runtime := &Runtime{Store: store, Activities: registry, Catalog: catalog, Sleep: func(context.Context, time.Duration) error { return nil }}
	run, err := runtime.Start(context.Background(), ir, StartOptions{RunID: "once", Inputs: map[string]any{}, Policy: DefaultPolicy()})
	if err == nil || run.Status != RunFailed || executor.executes != 1 {
		t.Fatalf("run=%+v err=%v executes=%d", run, err, executor.executes)
	}
	_, _ = runtime.Resume(context.Background(), run.ID)
	if executor.executes != 1 {
		t.Fatalf("terminal at-most-once step repeated on resume: %d", executor.executes)
	}
}

func TestReconcilableLiveErrorRetriesOnlyAfterNotApplied(t *testing.T) {
	ir, catalog := recoveryIR(t, activity.EffectReconcilable)
	store, _ := Open(filepath.Join(t.TempDir(), "w.db"))
	defer store.Close()
	executor := &recoveryExecutor{spec: activity.Spec{Name: "test.effect", Version: "1", Effect: activity.EffectReconcilable}, failExecutes: 1, reconcile: activity.ReconcileNotApplied}
	registry := activity.NewRegistry()
	_ = registry.Register(executor)
	runtime := &Runtime{Store: store, Activities: registry, Catalog: catalog, Sleep: func(context.Context, time.Duration) error { return nil }}
	run, err := runtime.Start(context.Background(), ir, StartOptions{RunID: "reconcile-live", Inputs: map[string]any{}, Policy: DefaultPolicy()})
	if err != nil || run.Status != RunSucceeded || executor.executes != 2 || executor.reconciles != 1 {
		t.Fatalf("run=%+v err=%v executes=%d reconciles=%d", run, err, executor.executes, executor.reconciles)
	}
}
