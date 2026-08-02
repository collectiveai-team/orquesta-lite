package workflow

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/activity"
	"github.com/lionelchamorro/orquestalite/internal/flow"
)

// wholeNumberPlanExecutor emits the loop's carried state verbatim, so a test
// can control the exact JSON encoding of iteration_budget instead of whatever
// json.Marshal picks for a Go value.
type wholeNumberPlanExecutor struct {
	budgetLiteral string
	passes        int
	total         int
}

func (e *wholeNumberPlanExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "test.plan", Version: "1", Effect: activity.EffectIdempotent}
}

func (e *wholeNumberPlanExecutor) Execute(_ context.Context, _ activity.Request) (activity.Result, error) {
	e.passes++
	status := "active"
	if e.passes >= e.total {
		status = "complete"
	}
	raw := `{"status":"` + status + `","iteration_budget":` + e.budgetLiteral + `}`
	return activity.Result{Output: json.RawMessage(raw)}, nil
}

// TestWhileBoundAcceptsWholeNumberWithDecimalPoint pins the contract between
// the pack's schema and the runtime's bound check.
//
// schemas/workflow-state@2.json declares iteration_budget as
// {"type":"number","minimum":1,"maximum":200} — so `4.0` is a schema-valid
// emission, and an LLM planner writing a JSON number with a trailing `.0` is
// entirely ordinary. The runtime disagrees: whileBound -> integerValue routes
// json.Number through Int64(), which is strconv.ParseInt and rejects "4.0"
// outright. The step then aborts with "must resolve to an integer" and the
// whole governed run dies mid-loop over a value its own contract accepted.
//
// Either the schema must demand an integer, or integerValue must accept a
// whole number written with a decimal point. This test passes under either
// fix and fails on the current code.
func TestWhileBoundAcceptsWholeNumberWithDecimalPoint(t *testing.T) {
	// Budget 4.0 with a plan that stays active for 3 passes: the bound is
	// never the thing that should stop this loop.
	executor := &wholeNumberPlanExecutor{budgetLiteral: "4.0", total: 3}
	body := strings.Replace(budgetedLoopFlow(`{"$ref":"item.iteration_budget"}`),
		`"iteration_budget":2`, `"iteration_budget":4.0`, 1)
	doc, err := flow.Decode(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	catalog := newMemoryCatalog()
	catalog.Activities["activity:test.plan@1"] = activity.Spec{Name: "test.plan", Version: "1", Effect: activity.EffectIdempotent}
	ir, diagnostics := flow.Compile(doc, catalog)
	if diagnostics.HasErrors() {
		t.Fatalf("%+v", diagnostics)
	}
	store, err := Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	registry := activity.NewRegistry()
	if err = registry.Register(executor); err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{Store: store, Activities: registry, Catalog: catalog}
	// The run id must be "budgeted": loopPassCount reads that run's aggregate.
	run, runErr := runtime.Start(context.Background(), ir, StartOptions{RunID: "budgeted", Inputs: map[string]any{}, Policy: DefaultPolicy()})
	if runErr != nil {
		t.Fatalf("run failed on a schema-valid iteration_budget of 4.0: %v", runErr)
	}
	if run.Status != RunSucceeded {
		t.Fatalf("status=%s error=%s", run.Status, run.Error)
	}
	if passes := loopPassCount(t, store); passes != 3 {
		t.Fatalf("loop ran %d passes, want 3", passes)
	}
}
