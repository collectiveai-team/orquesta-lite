package workflow

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/external"
)

func TestExternalUsageDeduplicatesRunsAndRetainsCumulativeSpending(t *testing.T) {
	store, step, request := externalStoreFixture(t)
	ctx := context.Background()
	record, err := store.PrepareExternal(ctx, step, "external.dispatch@1", request)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveExternalDispatch(ctx, record.ID, external.References{IssueID: "issue", RunID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	for _, observation := range []external.Result{
		{References: external.References{RunID: "run-1"}, CostUSD: 0.5, InputTokens: 10},
		{References: external.References{RunID: "run-1"}, CostUSD: 0.5, InputTokens: 10},
		{References: external.References{RunID: "run-1"}, CostUSD: 0.25, InputTokens: 5},
		{References: external.References{RunID: "run-1"}},
		{References: external.References{RunID: "run-2"}, CostUSD: 0.75, InputTokens: 20},
	} {
		if err := store.RecordExternalCheck(ctx, record.ID, observation, "waiting"); err != nil {
			t.Fatal(err)
		}
	}
	updated, err := store.GetExternalAttempt(ctx, record.ID)
	if err != nil || updated.Usage == nil || updated.Usage.CostUSD == nil || *updated.Usage.CostUSD != 1.25 || updated.Usage.InputTokens == nil || *updated.Usage.InputTokens != 30 || updated.Usage.OutputTokens != nil || updated.UsageRunCount != 2 {
		t.Fatalf("accounting=%+v err=%v", updated, err)
	}
	usage, err := store.RunUsage(ctx, step.RunID)
	if err != nil || usage.CostUSD != 1.25 {
		t.Fatalf("budget spending=%+v %v", usage, err)
	}
	// A failure claim cannot manufacture a credit that releases more budget.
	if err := store.RecordExternalCheck(ctx, record.ID, external.Result{CostUSD: -10}, "invalid"); err != nil {
		t.Fatal(err)
	}
	usage, err = store.RunUsage(ctx, step.RunID)
	if err != nil || usage.CostUSD != 1.25 {
		t.Fatalf("invalid spending reduced budget=%+v %v", usage, err)
	}
}

func TestExternalUsageDistinguishesReportedZeroFromUnavailable(t *testing.T) {
	store, step, request := externalStoreFixture(t)
	ctx := context.Background()
	record, err := store.PrepareExternal(ctx, step, "external.dispatch@1", request)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordExternalCheck(ctx, record.ID, external.Result{}, "unavailable"); err != nil {
		t.Fatal(err)
	}
	unknown, _ := store.GetExternalAttempt(ctx, record.ID)
	if unknown.Usage != nil {
		t.Fatalf("invented usage: %+v", unknown.Usage)
	}
	zero := 0.0
	if err := store.RecordExternalCheck(ctx, record.ID, external.Result{Usage: &external.Usage{CostUSD: &zero}}, "waiting"); err != nil {
		t.Fatal(err)
	}
	known, _ := store.GetExternalAttempt(ctx, record.ID)
	raw, _ := json.Marshal(known.Usage)
	if string(raw) != `{"costUSD":0}` {
		t.Fatalf("unknown metric became zero or zero was lost: %s", raw)
	}
}

func TestExternalUsageMigrationPreservesV4ReportedCost(t *testing.T) {
	store, step, request := externalStoreFixture(t)
	ctx := context.Background()
	record, err := store.PrepareExternal(ctx, step, "external.dispatch@1", request)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveExternalDispatch(ctx, record.ID, external.References{IssueID: "issue", RunID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordExternalCheck(ctx, record.ID, external.Result{CostUSD: 0.5, InputTokens: 5}, "waiting"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE external_usage`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version=4`); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(store.executorLock), "workflows.db")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	updated, err := migrated.GetExternalAttempt(ctx, record.ID)
	if err != nil || updated.Usage == nil || updated.Usage.CostUSD == nil || *updated.Usage.CostUSD != 0.5 || updated.UsageRunCount != 1 {
		t.Fatalf("lost v4 usage: %+v %v", updated, err)
	}
}
