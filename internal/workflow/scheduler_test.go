package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/flow"
)

type echoExecutor struct{ calls int }

type recordingExecutor struct {
	mu    sync.Mutex
	calls []string
}

func (e *recordingExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "test.record", Version: "1", Effect: activity.EffectIdempotent}
}
func (e *recordingExecutor) Execute(_ context.Context, request activity.Request) (activity.Result, error) {
	var input struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(request.Inputs, &input)
	e.mu.Lock()
	e.calls = append(e.calls, input.Name)
	e.mu.Unlock()
	if input.Name == "fail" {
		return activity.Result{}, &activity.Error{Class: activity.ErrorPermanent, Op: "record", Err: fmt.Errorf("failed")}
	}
	return activity.Result{Output: json.RawMessage(`{"ok":true}`)}, nil
}

func (e *echoExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "test.echo", Version: "1", Effect: activity.EffectIdempotent}
}

type incrementExecutor struct {
	mu                sync.Mutex
	active, maxActive int
}

func (e *incrementExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "test.increment", Version: "1", Effect: activity.EffectIdempotent}
}
func (e *incrementExecutor) Execute(_ context.Context, request activity.Request) (activity.Result, error) {
	var input struct {
		Value float64 `json:"value"`
	}
	_ = json.Unmarshal(request.Inputs, &input)
	e.mu.Lock()
	e.active++
	if e.active > e.maxActive {
		e.maxActive = e.active
	}
	e.mu.Unlock()
	time.Sleep(10 * time.Millisecond)
	e.mu.Lock()
	e.active--
	e.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{"value": input.Value + 1})
	return activity.Result{Output: raw}, nil
}

func TestSchedulerBoundedWhile(t *testing.T) {
	doc, err := flow.Decode(strings.NewReader(`{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"loop","version":"1"},"steps":[{"id":"inc","uses":"activity:test.increment@1","while":{"condition":"item.value < 3","maxIterations":5,"initial":{"value":0}},"with":{"value":{"$ref":"item.value"}}}],"outputs":{"values":{"$ref":"steps.inc.output"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	catalog := newMemoryCatalog()
	catalog.Activities["activity:test.increment@1"] = activity.Spec{Name: "test.increment", Version: "1", Effect: activity.EffectIdempotent}
	ir, diags := flow.Compile(doc, catalog)
	if diags.HasErrors() {
		t.Fatalf("%+v", diags)
	}
	store, _ := Open(filepath.Join(t.TempDir(), "w.db"))
	defer store.Close()
	registry := activity.NewRegistry()
	executor := &incrementExecutor{}
	_ = registry.Register(executor)
	runtime := &Runtime{Store: store, Activities: registry, Catalog: catalog}
	run, err := runtime.Start(context.Background(), ir, StartOptions{RunID: "loop", Inputs: map[string]any{}, Policy: DefaultPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != RunSucceeded {
		t.Fatalf("status=%s", run.Status)
	}
	step, err := store.GetStep(context.Background(), "loop", "root", "inc", "")
	if err != nil {
		t.Fatal(err)
	}
	var values []any
	_ = json.Unmarshal(step.Output, &values)
	if len(values) != 3 {
		t.Fatalf("values=%s", step.Output)
	}
}

func TestSchedulerParallelForeachHonorsIsolation(t *testing.T) {
	doc, err := flow.Decode(strings.NewReader(`{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"parallel","version":"1"},"inputs":{"items":{"schema":"schema:any@1"}},"steps":[{"id":"inc","uses":"activity:test.increment@1","foreach":{"items":{"$ref":"inputs.items"},"maxConcurrency":2,"isolationKey":{"$ref":"item.id"}},"with":{"value":{"$ref":"item.value"}}}],"outputs":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	catalog := newMemoryCatalog()
	catalog.Schemas["schema:any@1"] = &flow.Schema{}
	catalog.Activities["activity:test.increment@1"] = activity.Spec{Name: "test.increment", Version: "1", Effect: activity.EffectIdempotent}
	ir, diags := flow.Compile(doc, catalog)
	if diags.HasErrors() {
		t.Fatalf("%+v", diags)
	}
	store, _ := Open(filepath.Join(t.TempDir(), "w.db"))
	defer store.Close()
	registry := activity.NewRegistry()
	executor := &incrementExecutor{}
	_ = registry.Register(executor)
	runtime := &Runtime{Store: store, Activities: registry, Catalog: catalog}
	policy := DefaultPolicy()
	policy.MaxParallelism = 2
	_, err = runtime.Start(context.Background(), ir, StartOptions{RunID: "parallel", Inputs: map[string]any{"items": []any{map[string]any{"id": "a", "value": 1}, map[string]any{"id": "b", "value": 2}}}, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if executor.maxActive != 2 {
		t.Fatalf("max active=%d", executor.maxActive)
	}
}
func (e *echoExecutor) Execute(_ context.Context, request activity.Request) (activity.Result, error) {
	e.calls++
	return activity.Result{Output: request.Inputs}, nil
}

func TestSchedulerRunsAndResumesWithoutRepeatingSucceededStep(t *testing.T) {
	doc, err := flow.Decode(strings.NewReader(`{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"demo","version":"1"},"inputs":{"value":{"schema":"schema:any@1"}},"steps":[{"id":"echo","uses":"activity:test.echo@1","with":{"value":{"$ref":"inputs.value"}}}],"outputs":{"result":{"$ref":"steps.echo.output.value"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	catalog := newMemoryCatalog()
	catalog.Activities["activity:test.echo@1"] = activity.Spec{Name: "test.echo", Version: "1", Effect: activity.EffectIdempotent}
	catalog.Schemas["schema:any@1"] = &flow.Schema{}
	ir, diagnostics := flow.Compile(doc, catalog)
	if diagnostics.HasErrors() {
		t.Fatalf("%+v", diagnostics)
	}
	store, err := Open(filepath.Join(t.TempDir(), "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	registry := activity.NewRegistry()
	echo := &echoExecutor{}
	if err = registry.Register(echo); err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{Store: store, Activities: registry, Catalog: catalog}
	run, err := runtime.Start(context.Background(), ir, StartOptions{RunID: "r1", Inputs: map[string]any{"value": "hello"}, Policy: DefaultPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != RunSucceeded || echo.calls != 1 {
		t.Fatalf("run=%s calls=%d", run.Status, echo.calls)
	}
	_, err = runtime.Resume(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if echo.calls != 1 {
		t.Fatalf("resume repeated effect: %d", echo.calls)
	}
}

func TestSchedulerRunsErrorHandlerAndCompensatesInReverse(t *testing.T) {
	doc, err := flow.Decode(strings.NewReader(`{
  "apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"handlers","version":"1"},
  "steps":[
    {"id":"a","uses":"activity:test.record@1","with":{"name":"a"},"compensate":{"uses":"activity:test.record@1","with":{"name":"undo-a"}}},
    {"id":"b","uses":"activity:test.record@1","with":{"name":"b"},"compensate":{"uses":"activity:test.record@1","with":{"name":"undo-b"}}},
    {"id":"broken","uses":"activity:test.record@1","with":{"name":"fail"},"onError":{"uses":"activity:test.record@1","with":{"name":"on-error"}}}
  ]}`))
	if err != nil {
		t.Fatal(err)
	}
	catalog := newMemoryCatalog()
	catalog.Activities["activity:test.record@1"] = activity.Spec{Name: "test.record", Version: "1", Effect: activity.EffectIdempotent}
	ir, diagnostics := flow.Compile(doc, catalog)
	if diagnostics.HasErrors() {
		t.Fatalf("diagnostics: %+v", diagnostics)
	}
	store, _ := Open(filepath.Join(t.TempDir(), "workflows.db"))
	defer store.Close()
	registry := activity.NewRegistry()
	recorder := &recordingExecutor{}
	_ = registry.Register(recorder)
	runtime := &Runtime{Store: store, Activities: registry, Catalog: catalog}
	run, err := runtime.Start(context.Background(), ir, StartOptions{RunID: "handlers", Inputs: map[string]any{}, Policy: DefaultPolicy()})
	if err == nil || run.Status != RunFailed {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	want := []string{"a", "b", "fail", "on-error", "undo-b", "undo-a"}
	if strings.Join(recorder.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls=%v want=%v", recorder.calls, want)
	}
}

func TestSchedulerEnforcesRunAttemptBudget(t *testing.T) {
	doc, _ := flow.Decode(strings.NewReader(`{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"budget","version":"1"},"steps":[{"id":"one","uses":"activity:test.echo@1"},{"id":"two","uses":"activity:test.echo@1"}]}`))
	catalog := newMemoryCatalog()
	catalog.Activities["activity:test.echo@1"] = activity.Spec{Name: "test.echo", Version: "1", Effect: activity.EffectIdempotent}
	ir, _ := flow.Compile(doc, catalog)
	store, _ := Open(filepath.Join(t.TempDir(), "workflows.db"))
	defer store.Close()
	registry := activity.NewRegistry()
	echo := &echoExecutor{}
	_ = registry.Register(echo)
	policy := DefaultPolicy()
	policy.MaxAttempts = 1
	run, err := (&Runtime{Store: store, Activities: registry, Catalog: catalog}).Start(context.Background(), ir, StartOptions{RunID: "budget", Inputs: map[string]any{}, Policy: policy})
	if err == nil || !strings.Contains(err.Error(), "attempt budget exhausted") || run.Status != RunFailed || echo.calls != 1 {
		t.Fatalf("run=%+v err=%v calls=%d", run, err, echo.calls)
	}
}

func TestSchedulerUnsafeActivityRequiresApproval(t *testing.T) {
	doc, _ := flow.Decode(strings.NewReader(`{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"approval","version":"1"},"steps":[{"id":"publish","uses":"activity:test.echo@1"}]}`))
	catalog := newMemoryCatalog()
	catalog.Activities["activity:test.echo@1"] = activity.Spec{Name: "test.echo", Version: "1", Effect: activity.EffectUnsafe}
	ir, _ := flow.Compile(doc, catalog)
	store, _ := Open(filepath.Join(t.TempDir(), "workflows.db"))
	defer store.Close()
	registry := activity.NewRegistry()
	executor := &recoveryExecutor{spec: activity.Spec{Name: "test.echo", Version: "1", Effect: activity.EffectUnsafe}}
	_ = registry.Register(executor)
	runtime := &Runtime{Store: store, Activities: registry, Catalog: catalog}
	run, err := runtime.Start(context.Background(), ir, StartOptions{RunID: "approval", Inputs: map[string]any{}, Policy: DefaultPolicy()})
	if !errors.Is(err, ErrNeedsHuman) || run.Status != RunNeedsHuman || executor.executes != 0 {
		t.Fatalf("run=%+v err=%v calls=%d", run, err, executor.executes)
	}
	approvals, _ := store.ListApprovals(context.Background(), run.ID)
	if len(approvals) != 1 {
		t.Fatalf("approvals=%+v", approvals)
	}
	if err = store.DecideApproval(context.Background(), run.ID, approvals[0].ID, "approve"); err != nil {
		t.Fatal(err)
	}
	run, err = runtime.Resume(context.Background(), run.ID)
	if err != nil || run.Status != RunSucceeded || executor.executes != 1 {
		t.Fatalf("resumed run=%+v err=%v calls=%d", run, err, executor.executes)
	}
}

func TestSchedulerForeach(t *testing.T) {
	doc, err := flow.Decode(strings.NewReader(`{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"many","version":"1"},"inputs":{"items":{"schema":"schema:any@1"}},"steps":[{"id":"echo","uses":"activity:test.echo@1","foreach":{"items":{"$ref":"inputs.items"}},"with":{"value":{"$ref":"item"}}}],"outputs":{"result":{"$ref":"steps.echo.output"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	catalog := newMemoryCatalog()
	catalog.Activities["activity:test.echo@1"] = activity.Spec{Name: "test.echo", Version: "1", Effect: activity.EffectIdempotent}
	catalog.Schemas["schema:any@1"] = &flow.Schema{}
	ir, diags := flow.Compile(doc, catalog)
	if diags.HasErrors() {
		t.Fatalf("%+v", diags)
	}
	store, _ := Open(filepath.Join(t.TempDir(), "w.db"))
	defer store.Close()
	registry := activity.NewRegistry()
	echo := &echoExecutor{}
	_ = registry.Register(echo)
	runtime := &Runtime{Store: store, Activities: registry, Catalog: catalog}
	run, err := runtime.Start(context.Background(), ir, StartOptions{RunID: "r2", Inputs: map[string]any{"items": []any{"a", "b"}}, Policy: DefaultPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != RunSucceeded || echo.calls != 2 {
		t.Fatalf("run=%s calls=%d", run.Status, echo.calls)
	}
	step, err := store.GetStep(context.Background(), "r2", "root", "echo", "")
	if err != nil {
		t.Fatal(err)
	}
	var output []any
	if err = json.Unmarshal(step.Output, &output); err != nil || len(output) != 2 {
		t.Fatalf("output=%s err=%v", step.Output, err)
	}
}
