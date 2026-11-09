package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/checks"
	"github.com/collectiveai-team/orquesta-lite/internal/review"
	"github.com/collectiveai-team/orquesta-lite/internal/risk"
)

type ReviewPolicyExecutor struct {
	Store           risk.UnitStore
	Policy          risk.Policy
	ResolveProof    func(context.Context, checks.Snapshot, string) (review.Proof, error)
	VerifySnapshot  func(checks.Snapshot) error
	ValidateClaims  func(context.Context, checks.Snapshot, []review.ClaimsV3) error
	PreviousReviews func(context.Context, string) ([]review.ResultV3, error)
}

func (*ReviewPolicyExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "review.evaluate", Version: "1", Effect: activity.EffectPure}
}
func (e *ReviewPolicyExecutor) Execute(ctx context.Context, r activity.Request) (activity.Result, error) {
	var in struct {
		Unit     string            `json:"unit"`
		Snapshot checks.Snapshot   `json:"snapshot"`
		Reviews  []review.ClaimsV3 `json:"reviews"`
		Previous []review.ResultV3 `json:"previous,omitempty"`
	}
	if err := strictJSON(r.Inputs, &in); err != nil {
		return activity.Result{}, contractError("review.evaluate", err)
	}
	u, err := e.Store.Load(in.Unit)
	if err != nil {
		return activity.Result{}, err
	}
	if err = risk.CheckScope(u.Risk, candidate(in.Snapshot), e.Policy); err != nil {
		return activity.Result{}, &activity.ApprovalRequiredError{Reason: err.Error(), Input: r.Inputs}
	}
	if e.VerifySnapshot == nil || e.ResolveProof == nil || e.ValidateClaims == nil || e.PreviousReviews == nil {
		return activity.Result{}, fmt.Errorf("review.evaluate requires trusted snapshot/proof/claim/history resolvers")
	}
	if err = e.VerifySnapshot(in.Snapshot); err != nil {
		return activity.Result{}, &activity.ApprovalRequiredError{Reason: "review tree unavailable: " + err.Error(), Input: r.Inputs}
	}
	if len(in.Reviews) == 0 {
		return activity.Result{}, contractError("review.evaluate", fmt.Errorf("at least one structural or lens review required"))
	}
	if err := e.ValidateClaims(ctx, in.Snapshot, in.Reviews); err != nil {
		return activity.Result{}, contractError("review.evaluate claims", err)
	}
	previous, err := e.PreviousReviews(ctx, in.Unit)
	if err != nil {
		return activity.Result{}, err
	}
	// Flow inputs may carry history for reviewer context, but cannot replace
	// the runtime's complete durable history or invent a prior verdict.
	for _, supplied := range in.Previous {
		if !slices.ContainsFunc(previous, func(actual review.ResultV3) bool { return actual.Digest != "" && actual.Digest == supplied.Digest }) {
			return activity.Result{}, contractError("review.evaluate history", fmt.Errorf("supplied historical verdict was not produced by runtime"))
		}
	}
	combined := review.ClaimsV3{Version: 3, Lens: "structural", Status: "complete", Summary: "", Findings: []review.ClaimV3{}, Limitations: []string{}, ReviewedRevision: in.Snapshot.TreeHash, Evidence: []string{}}
	required := u.Risk.Lenses
	if len(required) == 0 {
		required = []string{"structural"}
	}
	seen := map[string]bool{}
	for _, claims := range in.Reviews {
		if err = claims.Validate(); err != nil {
			return activity.Result{}, contractError("review.evaluate", err)
		}
		if !slices.Contains(required, claims.Lens) || seen[claims.Lens] {
			return activity.Result{}, contractError("review.evaluate", fmt.Errorf("unexpected or duplicate lens %q", claims.Lens))
		}
		seen[claims.Lens] = true
		if claims.ReviewedRevision != in.Snapshot.TreeHash && (claims.Status == "complete" || claims.Status == "not_applicable") {
			return activity.Result{}, contractError("review.evaluate", fmt.Errorf("review revision mismatch"))
		}
		if claims.Status != "complete" && claims.Status != "not_applicable" {
			combined.Status = "partial"
		}
		combined.Summary += claims.Summary + "\n"
		combined.Limitations = append(combined.Limitations, claims.Limitations...)
		combined.Evidence = append(combined.Evidence, claims.Evidence...)
		if claims.Visual != nil {
			if combined.Visual != nil {
				return activity.Result{}, contractError("review.evaluate", fmt.Errorf("multiple visual reports require explicit reconciliation"))
			}
			combined.Visual = claims.Visual
		}
		for _, f := range claims.Findings {
			i := slices.IndexFunc(combined.Findings, func(old review.ClaimV3) bool {
				return old.Category == f.Category && old.Location == f.Location && strings.Join(strings.Fields(old.Trigger), " ") == strings.Join(strings.Fields(f.Trigger), " ")
			})
			if i < 0 {
				combined.Findings = append(combined.Findings, f)
				continue
			}
			old := &combined.Findings[i]
			rank := map[string]int{"low": 0, "medium": 1, "high": 2, "critical": 3}
			if rank[f.Severity] > rank[old.Severity] {
				old.Severity = f.Severity
			}
			if f.State == "open" {
				old.State = "open"
				old.Resolution = ""
			}
			for _, ref := range f.ProofRefs {
				if !slices.Contains(old.ProofRefs, ref) {
					old.ProofRefs = append(old.ProofRefs, ref)
				}
			}
		}
	}
	for _, lens := range required {
		if !seen[lens] {
			combined.Status = "partial"
			combined.Limitations = append(combined.Limitations, "required lens missing: "+lens)
		}
	}
	result, err := review.EvaluateV3(combined, in.Snapshot.TreeHash, in.Snapshot.BaseRevision, func(ref string) (review.Proof, error) { return e.ResolveProof(ctx, in.Snapshot, ref) }, previous)
	if err != nil {
		return activity.Result{}, contractError("review.evaluate", err)
	}
	repairs := []review.FindingV3{}
	for _, f := range result.Findings {
		if f.State == "open" && f.Causal == "introduced" && f.Severity != "low" {
			repairs = append(repairs, f)
		}
	}
	raw, _ := json.Marshal(map[string]any{"unit": in.Unit, "review": result, "findings": result.Findings, "repair_findings": repairs, "needs_repair": len(repairs) > 0, "needs_decision": len(result.NeedsDecision) > 0 || result.Status == "partial" || result.Status == "unavailable", "approved": result.Approved, "ready": result.Status == "complete" || result.Status == "not_applicable"})
	key := checks.Hash([]byte(r.RunID + "\x00" + r.ScopePath + "\x00" + r.StepID + "\x00" + r.ForeachKey + fmt.Sprint(r.Attempt)))
	dir := filepath.Join(e.Store.Root, "reviews")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return activity.Result{}, err
	}
	receipt := filepath.Join(dir, key+".json")
	f, err := os.OpenFile(receipt, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		old, readErr := os.ReadFile(receipt)
		if readErr != nil || string(old) != string(raw) {
			return activity.Result{}, fmt.Errorf("immutable review receipt differs")
		}
	} else if err != nil {
		return activity.Result{}, err
	} else {
		_, err = f.Write(raw)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return activity.Result{}, err
		}
		if closeErr != nil {
			return activity.Result{}, closeErr
		}
	}
	return activity.Result{Output: raw, Artifacts: []activity.Artifact{{Kind: "review_evidence", URI: receipt, Digest: checks.Hash(raw)}}}, nil
}

func (e *ReviewPolicyExecutor) ValidateEvidence(ctx context.Context, request activity.Request, result activity.Result) error {
	var in struct {
		Snapshot checks.Snapshot `json:"snapshot"`
	}
	if err := json.Unmarshal(request.Inputs, &in); err != nil {
		return err
	}
	if e.VerifySnapshot == nil {
		return fmt.Errorf("snapshot verifier missing")
	}
	if err := e.VerifySnapshot(in.Snapshot); err != nil {
		return err
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Kind != "review_evidence" || !within(e.Store.Root, result.Artifacts[0].URI) || result.Artifacts[0].Digest != checks.Hash(result.Output) {
		return fmt.Errorf("review receipt does not match output")
	}
	var output struct {
		Review review.ResultV3 `json:"review"`
	}
	if err := json.Unmarshal(result.Output, &output); err != nil {
		return err
	}
	digest := output.Review.Digest
	output.Review.Digest = ""
	body, _ := json.Marshal(output.Review)
	if digest == "" || checks.Hash(body) != digest {
		return fmt.Errorf("runtime review digest mismatch")
	}
	if e.ResolveProof == nil {
		return fmt.Errorf("proof resolver missing")
	}
	refs := slices.Clone(output.Review.Evidence)
	for _, finding := range output.Review.Findings {
		refs = append(refs, finding.ProofRefs...)
	}
	for _, ref := range refs {
		proof, err := e.ResolveProof(ctx, in.Snapshot, ref)
		if err != nil {
			return err
		}
		if !proof.Verified || proof.Kind == "memory" {
			return fmt.Errorf("review proof became unavailable")
		}
	}
	return verifyArtifacts(result.Artifacts)
}
