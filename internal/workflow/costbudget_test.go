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

// spendingExecutor charges a fixed amount per invocation, the way agent.invoke
// charges what its provider chain actually burned.
type spendingExecutor struct {
	perCall float64
	calls   int
}

func (e *spendingExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "test.plan", Version: "1", Effect: activity.EffectIdempotent}
}

func (e *spendingExecutor) Execute(_ context.Context, _ activity.Request) (activity.Result, error) {
	e.calls++
	raw, _ := json.Marshal(map[string]any{"status": "active", "iteration_budget": 50})
	return activity.Result{Output: raw, CostUSD: e.perCall}, nil
}

// TestCostBudgetStopsARunawayRun pins the downstream half of the maxCostUSD
// chain: an executor that reports spend really does stop a run.
//
// development@3.json sets maxAttempts and maxAgentAttempts to 0 — deliberately
// unlimited, because any attempt cap is a covert ticket cap — and puts the
// run-wide brake on spend instead. That trade is only sound if the whole chain
// works: activity.Result.CostUSD → attempts.cost_usd → SUM → the policy check.
// The upstream half (agent.invoke actually populating Result.CostUSD from the
// tokens its provider chain burned) is what was missing, and is pinned by
// TestAgentInvokeReportsCostUSD in internal/activity/builtin. Neither test is
// sufficient alone: with the field unset the cap could never fire, leaving
// maxDurationSeconds as the only real brake while the policy file and the
// operator docs both advertised a $250 ceiling.
//
// Here the loop would otherwise run 50 passes; the budget stops it after the
// spend crosses the cap.
func TestCostBudgetStopsARunawayRun(t *testing.T) {
	executor := &spendingExecutor{perCall: 4}
	doc, err := flow.Decode(strings.NewReader(budgetedLoopFlow(`{"$ref":"item.iteration_budget"}`)))
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
	defer store.Close()
	registry := activity.NewRegistry()
	if err = registry.Register(executor); err != nil {
		t.Fatal(err)
	}
	policy := DefaultPolicy()
	policy.MaxAttempts = 0
	policy.MaxAgentAttempts = 0
	policy.MaxCostUSD = 10
	runtime := &Runtime{Store: store, Activities: registry, Catalog: catalog}
	run, runErr := runtime.Start(context.Background(), ir, StartOptions{RunID: "spendy", Inputs: map[string]any{}, Policy: policy})
	if runErr == nil {
		t.Fatal("a run that outspends its budget must fail, not succeed")
	}
	if !strings.Contains(runErr.Error(), "cost budget exhausted") {
		t.Fatalf("err=%v, want the cost budget to be what stopped it", runErr)
	}
	if run != nil && run.Status == RunSucceeded {
		t.Fatalf("status=%s", run.Status)
	}
	// $4 a pass against a $10 cap: the third pass is the one that finds usage
	// already at or past the cap. Anything much larger means the spend is not
	// reaching the store.
	if executor.calls > 4 {
		t.Fatalf("ran %d passes before the $10 budget stopped it at $4/pass", executor.calls)
	}
	usage, err := store.RunUsage(context.Background(), "spendy")
	if err != nil {
		t.Fatal(err)
	}
	if usage.CostUSD <= 0 {
		t.Fatalf("attempts.cost_usd summed to %v; the executor's spend never reached durable state", usage.CostUSD)
	}
}
