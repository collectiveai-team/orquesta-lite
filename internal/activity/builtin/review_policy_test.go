package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/checks"
	"github.com/collectiveai-team/orquesta-lite/internal/review"
	"github.com/collectiveai-team/orquesta-lite/internal/risk"
)

func reviewPolicyFixture(t *testing.T) (*ReviewPolicyExecutor, checks.Snapshot, review.ClaimsV3) {
	t.Helper()
	repo := checkTestRepo(t)
	root := t.TempDir()
	snapshot, err := checks.Freeze(context.Background(), repo, filepath.Join(root, "snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	store := risk.UnitStore{Root: filepath.Join(root, "units")}
	if _, err := store.Initialize("unit", candidate(snapshot), risk.DefaultPolicy(), risk.DefaultCorrectionLimits()); err != nil {
		t.Fatal(err)
	}
	executor := &ReviewPolicyExecutor{Store: store, Policy: risk.DefaultPolicy(), VerifySnapshot: checks.VerifySnapshot, ResolveProof: func(context.Context, checks.Snapshot, string) (review.Proof, error) {
		return review.Proof{Revision: snapshot.TreeHash, Kind: "snapshot", Verified: true}, nil
	}, ValidateClaims: func(context.Context, checks.Snapshot, []review.ClaimsV3) error { return nil }, PreviousReviews: func(context.Context, string) ([]review.ResultV3, error) { return nil, nil }}
	claims := review.ClaimsV3{Version: 3, Lens: "reliability", Status: "complete", Summary: "review complete", Findings: []review.ClaimV3{}, Limitations: []string{}, Evidence: []string{}, ReviewedRevision: snapshot.TreeHash}
	return executor, snapshot, claims
}

func TestReviewPolicyRequiresRuntimeClaimProvenance(t *testing.T) {
	executor, snapshot, claims := reviewPolicyFixture(t)
	executor.ValidateClaims = func(context.Context, checks.Snapshot, []review.ClaimsV3) error {
		return fmt.Errorf("review was not produced by a read-only role")
	}
	raw, _ := json.Marshal(map[string]any{"unit": "unit", "snapshot": snapshot, "reviews": []review.ClaimsV3{claims}})
	if _, err := executor.Execute(context.Background(), activity.Request{Inputs: raw}); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("untrusted claims accepted: %v", err)
	}
}

func TestReviewPolicyPreservesRuntimeHistoryWhenCallerOmitsIt(t *testing.T) {
	executor, snapshot, claims := reviewPolicyFixture(t)
	priorClaims := claims
	priorClaims.Findings = []review.ClaimV3{{Category: "correctness", Severity: "medium", State: "open", Location: "source.txt:1", Trigger: "input", Impact: "wrong output", ProofRefs: []string{"proof"}, Causal: "unknown"}}
	prior, err := review.EvaluateV3(priorClaims, snapshot.TreeHash, snapshot.BaseRevision, func(string) (review.Proof, error) {
		return review.Proof{Revision: snapshot.TreeHash, Kind: "code", Verified: true}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	executor.PreviousReviews = func(context.Context, string) ([]review.ResultV3, error) { return []review.ResultV3{prior}, nil }
	raw, _ := json.Marshal(map[string]any{"unit": "unit", "snapshot": snapshot, "reviews": []review.ClaimsV3{claims}})
	if _, err := executor.Execute(context.Background(), activity.Request{Inputs: raw}); err == nil || !strings.Contains(err.Error(), "historical") {
		t.Fatalf("lost history accepted: %v", err)
	}
}

func TestReviewPolicyReceiptDeletionInvalidatesResult(t *testing.T) {
	executor, snapshot, claims := reviewPolicyFixture(t)
	raw, _ := json.Marshal(map[string]any{"unit": "unit", "snapshot": snapshot, "reviews": []review.ClaimsV3{claims}})
	request := activity.Request{RunID: "run", StepID: "review", Attempt: 1, Inputs: raw}
	result, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.ValidateEvidence(context.Background(), request, result); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(result.Artifacts[0].URI); err != nil {
		t.Fatal(err)
	}
	if err := executor.ValidateEvidence(context.Background(), request, result); err == nil {
		t.Fatal("missing review receipt accepted")
	}
}
