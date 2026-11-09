package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/checks"
	"github.com/collectiveai-team/orquesta-lite/internal/review"
	"github.com/collectiveai-team/orquesta-lite/internal/risk"
)

func validationFixture(t *testing.T) (risk.UnitStore, checks.Snapshot, activity.Request, risk.Validation) {
	t.Helper()
	store := risk.UnitStore{Root: t.TempDir()}
	snapshot := checks.Snapshot{TreeHash: "candidate", BaseRevision: "base", Paths: []string{"source.txt"}, ChangedLines: 10}
	if _, err := store.Initialize("unit", candidate(snapshot), risk.DefaultPolicy(), risk.DefaultCorrectionLimits()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginCorrection("unit", "reserve"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimInvocation("unit", "correction-task"); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"unit": "unit", "snapshot": snapshot, "checks": ChecksOutput{}, "review": review.ResultV3{}})
	proof := filepath.Join(t.TempDir(), "validation.json")
	body := []byte(`{"runtime":"proof"}`)
	if err := os.WriteFile(proof, body, 0600); err != nil {
		t.Fatal(err)
	}
	validation := risk.Validation{TreeHash: snapshot.TreeHash, Readable: true, Passed: true, ChangedLines: 2, Tokens: 10, ToolCalls: 1, EvidencePath: proof, EvidenceDigest: checks.Hash(body)}
	return store, snapshot, activity.Request{Inputs: raw}, validation
}

func TestCorrectionValidationRetriesOnlyReadsAndReplaysVerified(t *testing.T) {
	store, _, request, validation := validationFixture(t)
	measures := 0
	executor := &CorrectionValidateExecutor{Store: store, Policy: risk.DefaultPolicy(), Measure: func(context.Context, risk.Unit, checks.Snapshot, ChecksOutput, review.ResultV3) (risk.Validation, error) {
		measures++
		if measures <= 2 {
			return risk.Validation{Readable: false}, nil
		}
		return validation, nil
	}}
	if executor.Spec().Effect != activity.EffectIdempotent {
		t.Fatal("read-only validation cannot retry infrastructure failures")
	}
	for attempt := 0; attempt < 2; attempt++ {
		_, err := executor.Execute(context.Background(), request)
		if activity.Classify(err) != activity.ErrorTransient {
			t.Fatalf("unreadable attempt %d: %v", attempt, err)
		}
	}
	if _, err := executor.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	u, err := store.Load("unit")
	if err != nil {
		t.Fatal(err)
	}
	if u.Status != "verified" || u.Inconclusive != 2 || u.Reservation != "reserve" || u.CorrectionTask != "correction-task" {
		t.Fatalf("validation recoveries changed correction reservation: %+v", u)
	}
	if _, err := executor.Execute(context.Background(), request); err != nil {
		t.Fatal("verified replay failed", err)
	}
	if measures != 3 {
		t.Fatalf("verified replay measured again: %d", measures)
	}
	if err := os.Remove(validation.EvidencePath); err != nil {
		t.Fatal(err)
	}
	var approval *activity.ApprovalRequiredError
	if _, err := executor.Execute(context.Background(), request); !errors.As(err, &approval) {
		t.Fatalf("lost verified proof did not need decision: %v", err)
	}
	if measures != 3 {
		t.Fatal("lost proof restarted measurement or correction")
	}
}

func TestCorrectionValidationUnreadableRecoveryIsBounded(t *testing.T) {
	store, _, request, _ := validationFixture(t)
	measures := 0
	executor := &CorrectionValidateExecutor{Store: store, Policy: risk.DefaultPolicy(), Measure: func(context.Context, risk.Unit, checks.Snapshot, ChecksOutput, review.ResultV3) (risk.Validation, error) {
		measures++
		return risk.Validation{Readable: false}, nil
	}}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := executor.Execute(context.Background(), request); activity.Classify(err) != activity.ErrorTransient {
			t.Fatalf("attempt %d err=%v", attempt, err)
		}
	}
	var approval *activity.ApprovalRequiredError
	if _, err := executor.Execute(context.Background(), request); !errors.As(err, &approval) {
		t.Fatalf("recovery budget did not require decision: %v", err)
	}
	if _, err := executor.Execute(context.Background(), request); !errors.As(err, &approval) {
		t.Fatalf("exhausted replay did not preserve decision: %v", err)
	}
	if measures != 3 {
		t.Fatalf("exhausted replay invoked validator: %d", measures)
	}
	u, err := store.Load("unit")
	if err != nil {
		t.Fatal(err)
	}
	if u.Status != "needs_decision" || u.CorrectionTask != "correction-task" {
		t.Fatalf("recovery budget changed correction identity: %+v", u)
	}
}
