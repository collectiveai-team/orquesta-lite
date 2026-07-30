package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/activity"
	"github.com/lionelchamorro/orquestalite/internal/flow"
)

// planExecutor stands in for the ticket planner: every pass reports one more
// completed ticket, and it may raise the loop's own iteration budget mid-flight
// the way a replan that discovered new work does.
type planExecutor struct {
	budgets []int // budget reported on pass 0, 1, 2, ...; the last value repeats
	passes  int
	total   int // number of passes after which the plan reports "complete"
}

func (e *planExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "test.plan", Version: "1", Effect: activity.EffectIdempotent}
}

func (e *planExecutor) Execute(_ context.Context, request activity.Request) (activity.Result, error) {
	var input struct {
		Pass json.Number `json:"pass"`
	}
	_ = json.Unmarshal(request.Inputs, &input)
	budget := e.budgets[len(e.budgets)-1]
	if e.passes < len(e.budgets) {
		budget = e.budgets[e.passes]
	}
	e.passes++
	status := "active"
	if e.passes >= e.total {
		status = "complete"
	}
	raw, _ := json.Marshal(map[string]any{"status": status, "iteration_budget": budget})
	return activity.Result{Output: raw}, nil
}

// budgetedLoopFlow loops while `item.status == "active"`, taking its bound from
// the carried value itself.
func budgetedLoopFlow(bound string) string {
	return fmt.Sprintf(`{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"budgeted","version":"1"},"steps":[{"id":"plan","uses":"activity:test.plan@1","while":{"condition":"item.status == \"active\"","maxIterations":%s,"initial":{"status":"active","iteration_budget":2}},"with":{"pass":{"$ref":"index"}}}],"outputs":{"passes":{"$ref":"steps.plan.output"}}}`, bound)
}

func runBudgetedLoop(t *testing.T, bound string, executor activity.Executor, config map[string]any) (*Run, *Store) {
	t.Helper()
	doc, err := flow.Decode(strings.NewReader(budgetedLoopFlow(bound)))
	if err != nil {
		t.Fatal(err)
	}
	catalog := flow.NewMemoryCatalog()
	catalog.Activities["activity:test.plan@1"] = activity.Spec{Name: "test.plan", Version: "1", Effect: activity.EffectIdempotent}
	ir, diagnostics := flow.Compile(doc, catalog)
	if diagnostics.HasErrors() {
		t.Fatalf("%+v", diagnostics)
	}
	store, err := Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	registry := activity.NewRegistry()
	if err = registry.Register(executor); err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{Store: store, Activities: registry, Catalog: catalog, Config: config}
	run, _ := runtime.Start(context.Background(), ir, StartOptions{RunID: "budgeted", Inputs: map[string]any{}, Policy: DefaultPolicy()})
	return run, store
}

func loopPassCount(t *testing.T, store *Store) int {
	t.Helper()
	step, err := store.GetStep(context.Background(), "budgeted", "root", "plan", "")
	if err != nil {
		t.Fatal(err)
	}
	var values []any
	if err = json.Unmarshal(step.Output, &values); err != nil {
		t.Fatal(err)
	}
	return len(values)
}

// This is the T12 case from the design: a plan whose budget was 2 discovers
// more work mid-loop and raises it to 5. Re-resolving the bound per pass is
// what keeps the loop alive; resolving it once before entering would have
// killed the run at pass 2 with work still pending.
func TestWhileBoundRaisedMidLoopExtendsTheLoop(t *testing.T) {
	executor := &planExecutor{budgets: []int{2, 5}, total: 4}
	run, store := runBudgetedLoop(t, `{"$ref":"item.iteration_budget"}`, executor, nil)
	defer store.Close()
	if run.Status != RunSucceeded {
		t.Fatalf("status=%s error=%s", run.Status, run.Error)
	}
	if passes := loopPassCount(t, store); passes != 4 {
		t.Fatalf("loop ran %d passes, want 4 — the raised budget did not take effect", passes)
	}
}

// The complement: a bound that never changes still stops exactly where it
// always did. Re-resolving must not turn a fixed budget into an open one.
func TestWhileFixedBoundStopsExactlyAtTheBound(t *testing.T) {
	// The plan never reports complete, so only the bound can stop the loop.
	executor := &planExecutor{budgets: []int{3}, total: 99}
	run, store := runBudgetedLoop(t, `{"$ref":"item.iteration_budget"}`, executor, nil)
	defer store.Close()
	if run.Status != RunSucceeded {
		t.Fatalf("status=%s error=%s", run.Status, run.Error)
	}
	if passes := loopPassCount(t, store); passes != 3 {
		t.Fatalf("loop ran %d passes, want exactly 3", passes)
	}
}

func TestWhileLiteralBoundIsUnchanged(t *testing.T) {
	executor := &planExecutor{budgets: []int{999}, total: 99}
	run, store := runBudgetedLoop(t, "2", executor, nil)
	defer store.Close()
	if run.Status != RunSucceeded {
		t.Fatalf("status=%s error=%s", run.Status, run.Error)
	}
	if passes := loopPassCount(t, store); passes != 2 {
		t.Fatalf("loop ran %d passes, want 2", passes)
	}
}

// A data-derived bound can be anything the data says, so every unusable value
// must abort the step with an error that names it rather than silently
// clamping or looping forever.
func TestWhileRejectsUnusableResolvedBound(t *testing.T) {
	cases := map[string]struct {
		initialBudget string
		wantFragment  string
	}{
		"zero":            {"0", "outside 1..1000"},
		"negative":        {"-4", "outside 1..1000"},
		"above ceiling":   {"1001", "outside 1..1000"},
		"non-integer":     {"2.5", "must resolve to an integer"},
		"non-numeric":     {`"many"`, "must resolve to an integer"},
		"at ceiling ok++": {"1000", ""}, // 1000 is allowed; the plan stops it
	}
	for name, tc := range cases {
		body := strings.Replace(budgetedLoopFlow(`{"$ref":"item.iteration_budget"}`),
			`"iteration_budget":2`, `"iteration_budget":`+tc.initialBudget, 1)
		doc, err := flow.Decode(strings.NewReader(body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		catalog := flow.NewMemoryCatalog()
		catalog.Activities["activity:test.plan@1"] = activity.Spec{Name: "test.plan", Version: "1", Effect: activity.EffectIdempotent}
		ir, diagnostics := flow.Compile(doc, catalog)
		if diagnostics.HasErrors() {
			t.Fatalf("%s: %+v", name, diagnostics)
		}
		store, _ := Open(filepath.Join(t.TempDir(), "w.db"))
		registry := activity.NewRegistry()
		_ = registry.Register(&planExecutor{budgets: []int{1}, total: 1})
		runtime := &Runtime{Store: store, Activities: registry, Catalog: catalog}
		_, runErr := runtime.Start(context.Background(), ir, StartOptions{RunID: "b", Inputs: map[string]any{}, Policy: DefaultPolicy()})
		store.Close()
		if tc.wantFragment == "" {
			if runErr != nil {
				t.Errorf("%s: unexpected error %v", name, runErr)
			}
			continue
		}
		if runErr == nil || !strings.Contains(runErr.Error(), tc.wantFragment) {
			t.Errorf("%s: err=%v, want it to mention %q", name, runErr, tc.wantFragment)
		}
	}
}
