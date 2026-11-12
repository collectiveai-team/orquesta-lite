package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/checks"
	"github.com/collectiveai-team/orquesta-lite/internal/review"
)

// ReviewCompareExecutor executes one already approved check against both trees.
// The comparison proves attribution of that check's failure, not the model's
// broader explanation of a defect. Model-supplied commands are never accepted.
type ReviewCompareExecutor struct {
	ProjectDir   string
	ArtifactRoot string
	Profile      *checks.Resolved
}

// ReviewFailedChecksExecutor selects only normal command failures for causal
// comparison. Infrastructure failures/timeouts are not retried as product tests.
type ReviewFailedChecksExecutor struct {
	ArtifactRoot string
	Profile      *checks.Resolved
}

func (*ReviewFailedChecksExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "review.failed_checks", Version: "1", Effect: activity.EffectPure}
}

func (e *ReviewFailedChecksExecutor) Execute(_ context.Context, request activity.Request) (activity.Result, error) {
	var input struct {
		Checks   ChecksOutput     `json:"checks"`
		Previous *review.ResultV3 `json:"previous,omitempty"`
	}
	if err := strictJSON(request.Inputs, &input); err != nil {
		return activity.Result{}, contractError("review.failed_checks", err)
	}
	if e.Profile == nil || input.Checks.ProfileHash != e.Profile.SourceHash {
		return activity.Result{}, fmt.Errorf("comparison selection profile mismatch")
	}
	raw, _ := json.Marshal(input.Checks)
	if err := verifyOutput(raw, true); err != nil {
		return activity.Result{}, err
	}
	ids := []string{}
	for _, item := range input.Checks.Evidence {
		if !within(e.ArtifactRoot, item.Path) {
			return activity.Result{}, fmt.Errorf("failure receipt outside run")
		}
		raw, err := os.ReadFile(item.Path)
		if err != nil {
			return activity.Result{}, err
		}
		var evidence checks.Evidence
		if err := json.Unmarshal(raw, &evidence); err != nil {
			return activity.Result{}, err
		}
		if evidence.ExitCode > 0 && !evidence.TimedOut && (evidence.Error == "" || strings.HasPrefix(evidence.Error, "exit status ")) {
			ids = append(ids, item.CheckID)
		}
	}
	if input.Previous != nil {
		for _, finding := range input.Previous.Findings {
			if finding.Category != "runtime_check" || finding.State != "open" || slices.Contains(ids, finding.Trigger) {
				continue
			}
			declared := slices.ContainsFunc(e.Profile.Profile.Items, func(c checks.Check) bool { return c.ID == finding.Trigger })
			if !declared {
				return activity.Result{}, fmt.Errorf("historical runtime check is no longer declared")
			}
			ids = append(ids, finding.Trigger)
		}
	}
	raw, _ = json.Marshal(map[string]any{"check_ids": ids})
	return activity.Result{Output: raw}, nil
}

func (*ReviewCompareExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "review.compare", Version: "1", Effect: activity.EffectAtMostOnce}
}

type comparisonInput struct {
	Snapshot checks.Snapshot `json:"snapshot"`
	CheckID  string          `json:"check_id"`
}

type comparisonEvidence struct {
	Version          int             `json:"version"`
	CheckID          string          `json:"check_id"`
	ProfileHash      string          `json:"profile_hash"`
	Candidate        checks.Snapshot `json:"candidate"`
	Base             checks.Snapshot `json:"base"`
	CandidateReceipt CheckEvidence   `json:"candidate_receipt"`
	BaseReceipt      CheckEvidence   `json:"base_receipt"`
	Causal           string          `json:"causal"`
	Reference        string          `json:"reference"`
	Digest           string          `json:"digest"`
}

func (e *ReviewCompareExecutor) Execute(ctx context.Context, request activity.Request) (activity.Result, error) {
	var in comparisonInput
	if err := strictJSON(request.Inputs, &in); err != nil {
		return activity.Result{}, contractError("review.compare", err)
	}
	if e.Profile == nil {
		return activity.Result{}, contractError("review.compare", fmt.Errorf("compiled check profile missing"))
	}
	if !within(e.ArtifactRoot, in.Snapshot.Dir) {
		return activity.Result{}, fmt.Errorf("comparison candidate outside run")
	}
	if err := checks.VerifySnapshot(in.Snapshot); err != nil {
		return activity.Result{}, err
	}
	var selected *checks.Check
	for i := range e.Profile.Profile.Items {
		if e.Profile.Profile.Items[i].ID == in.CheckID {
			selected = &e.Profile.Profile.Items[i]
			break
		}
	}
	if selected == nil {
		return activity.Result{}, contractError("review.compare", fmt.Errorf("unknown approved check %q", in.CheckID))
	}
	identity := checks.Hash([]byte(request.RunID + "\x00" + request.ScopePath + "\x00" + request.StepID + "\x00" + request.ForeachKey))
	directory := filepath.Join(e.ArtifactRoot, "comparisons", identity, fmt.Sprintf("attempt-%d", request.Attempt))
	if err := os.MkdirAll(directory, 0700); err != nil {
		return activity.Result{}, err
	}
	base, err := checks.FreezeRevision(ctx, e.ProjectDir, filepath.Join(directory, "base"), in.Snapshot.BaseRevision)
	if err != nil {
		return activity.Result{}, err
	}
	baseRun, err := checks.Execute(ctx, *selected, base, checks.Execution{Root: e.ArtifactRoot, RunID: request.RunID, Scope: request.ScopePath + "/" + request.StepID + "/" + request.ForeachKey + "/base", Attempt: request.Attempt, ProfileHash: e.Profile.SourceHash})
	if err != nil {
		return activity.Result{}, err
	}
	candidateRun, err := checks.Execute(ctx, *selected, in.Snapshot, checks.Execution{Root: e.ArtifactRoot, RunID: request.RunID, Scope: request.ScopePath + "/" + request.StepID + "/" + request.ForeachKey + "/candidate", Attempt: request.Attempt, ProfileHash: e.Profile.SourceHash})
	if err != nil {
		return activity.Result{}, err
	}
	path := filepath.Join(directory, "comparison.json")
	comparison := comparisonEvidence{Version: 1, CheckID: selected.ID, ProfileHash: e.Profile.SourceHash, Candidate: in.Snapshot, Base: base, BaseReceipt: CheckEvidence{CheckID: selected.ID, Path: baseRun.Receipt, Digest: baseRun.ReceiptHash}, CandidateReceipt: CheckEvidence{CheckID: selected.ID, Path: candidateRun.Receipt, Digest: candidateRun.ReceiptHash}, Causal: comparisonCausal(baseRun, candidateRun), Reference: "compare:" + path}
	raw, _ := json.Marshal(comparison)
	comparison.Digest = checks.Hash(raw)
	raw, _ = json.Marshal(comparison)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return activity.Result{}, err
	}
	_, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil {
		return activity.Result{}, writeErr
	}
	if syncErr != nil {
		return activity.Result{}, syncErr
	}
	if closeErr != nil {
		return activity.Result{}, closeErr
	}
	return activity.Result{Output: raw, Artifacts: []activity.Artifact{{Kind: "check_comparison", URI: path, Digest: checks.Hash(raw)}}}, nil
}

func comparisonCausal(base, candidate checks.Evidence) string {
	// Launch failures, timeouts and source-mutating checks are infrastructure
	// uncertainty, never proof that a product change introduced a regression.
	valid := func(e checks.Evidence) bool {
		return !e.TimedOut && e.ExitCode >= 0 && (e.Error == "" || strings.HasPrefix(e.Error, "exit status "))
	}
	if !valid(base) || !valid(candidate) || candidate.ExitCode == 0 {
		return "unknown"
	}
	if base.ExitCode == 0 {
		return "introduced"
	}
	if base.ExitCode == candidate.ExitCode && base.StdoutHash == candidate.StdoutHash && base.StderrHash == candidate.StderrHash {
		return "preexisting"
	}
	return "unknown"
}

func (e *ReviewCompareExecutor) ValidateEvidence(_ context.Context, request activity.Request, result activity.Result) error {
	var input comparisonInput
	if err := strictJSON(request.Inputs, &input); err != nil {
		return err
	}
	var out comparisonEvidence
	if err := strictJSON(result.Output, &out); err != nil {
		return err
	}
	if e.Profile == nil || out.ProfileHash != e.Profile.SourceHash || out.CheckID != input.CheckID || out.Candidate.TreeHash != input.Snapshot.TreeHash {
		return fmt.Errorf("comparison differs from compiled request")
	}
	if _, err := ResolveComparisonProof(e.ArtifactRoot, input.Snapshot, out.Reference); err != nil {
		return err
	}
	stored, err := os.ReadFile(strings.TrimPrefix(out.Reference, "compare:"))
	if err != nil {
		return err
	}
	if string(stored) != string(result.Output) {
		return fmt.Errorf("comparison output differs from its immutable receipt")
	}
	for _, c := range e.Profile.Profile.Items {
		if c.ID == input.CheckID {
			for _, receipt := range []CheckEvidence{out.BaseReceipt, out.CandidateReceipt} {
				raw, err := os.ReadFile(receipt.Path)
				if err != nil {
					return err
				}
				var evidence checks.Evidence
				if err := json.Unmarshal(raw, &evidence); err != nil {
					return err
				}
				if !reflect.DeepEqual(evidence.Argv, c.Argv) || evidence.CheckID != c.ID || evidence.RunID != request.RunID || evidence.Attempt != request.Attempt {
					return fmt.Errorf("comparison check identity mismatch")
				}
			}
			return nil
		}
	}
	return fmt.Errorf("comparison check is not in compiled profile")
}

// ResolveComparisonProof verifies the immutable receipt and every referenced
// log before exposing causal attribution to review policy. Files cannot escape
// the current run, and a digest cannot be used for a different candidate.
func ResolveComparisonProof(root string, snapshot checks.Snapshot, ref string) (review.Proof, error) {
	if !strings.HasPrefix(ref, "compare:") {
		return review.Proof{}, fmt.Errorf("unsupported comparison reference")
	}
	path := strings.TrimPrefix(ref, "compare:")
	if !within(root, path) {
		return review.Proof{}, fmt.Errorf("comparison outside run")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return review.Proof{}, err
	}
	var c comparisonEvidence
	if err := strictJSON(raw, &c); err != nil {
		return review.Proof{}, err
	}
	if c.Version != 1 || c.Reference != ref || c.Candidate.TreeHash != snapshot.TreeHash || c.Candidate.BaseRevision != snapshot.BaseRevision || c.Base.Commit != snapshot.BaseRevision || c.ProfileHash == "" || c.CheckID == "" {
		return review.Proof{}, fmt.Errorf("comparison identity mismatch")
	}
	digest := c.Digest
	c.Digest = ""
	canonical, _ := json.Marshal(c)
	if checks.Hash(canonical) != digest {
		return review.Proof{}, fmt.Errorf("comparison digest mismatch")
	}
	if !within(root, c.Base.Dir) || !within(root, c.Candidate.Dir) {
		return review.Proof{}, fmt.Errorf("comparison tree outside run")
	}
	if err := checks.VerifySnapshot(c.Base); err != nil {
		return review.Proof{}, err
	}
	if err := checks.VerifySnapshot(c.Candidate); err != nil {
		return review.Proof{}, err
	}
	runs := make([]checks.Evidence, 0, 2)
	for i, item := range []CheckEvidence{c.BaseReceipt, c.CandidateReceipt} {
		if item.CheckID != c.CheckID || !within(root, item.Path) {
			return review.Proof{}, fmt.Errorf("comparison receipt outside run")
		}
		bytes, err := os.ReadFile(item.Path)
		if err != nil || checks.Hash(bytes) != item.Digest {
			return review.Proof{}, fmt.Errorf("comparison receipt corrupt")
		}
		tree := c.Base.TreeHash
		if i == 1 {
			tree = c.Candidate.TreeHash
		}
		if err := checks.VerifyReceipt(item.Path, tree, c.ProfileHash, false); err != nil {
			return review.Proof{}, err
		}
		var evidence checks.Evidence
		if err := json.Unmarshal(bytes, &evidence); err != nil {
			return review.Proof{}, err
		}
		runs = append(runs, evidence)
	}
	if runs[0].CheckID != c.CheckID || runs[1].CheckID != c.CheckID || !reflect.DeepEqual(runs[0].Argv, runs[1].Argv) || comparisonCausal(runs[0], runs[1]) != c.Causal {
		return review.Proof{}, fmt.Errorf("comparison attribution mismatch")
	}
	return review.Proof{Revision: snapshot.TreeHash, Kind: "check_comparison", Causal: c.Causal, BaseRevision: snapshot.BaseRevision, Verified: true, Subject: c.CheckID, CheckPassed: runs[1].ExitCode == 0 && runs[1].Error == "" && !runs[1].TimedOut}, nil
}

// ResolveComparisonReference allows a current review to retain a verified
// historical comparison from this run and the same base. EvaluateV3 still
// requires that historical proof to appear in an actual earlier verdict.
func ResolveComparisonReference(root string, snapshot checks.Snapshot, ref string) (review.Proof, error) {
	if !strings.HasPrefix(ref, "compare:") {
		return review.Proof{}, fmt.Errorf("unsupported comparison reference")
	}
	path := strings.TrimPrefix(ref, "compare:")
	if !within(root, path) {
		return review.Proof{}, fmt.Errorf("comparison outside run")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return review.Proof{}, err
	}
	var comparison comparisonEvidence
	if err := strictJSON(raw, &comparison); err != nil {
		return review.Proof{}, err
	}
	if comparison.Candidate.BaseRevision != snapshot.BaseRevision {
		return review.Proof{}, fmt.Errorf("comparison belongs to another unit base")
	}
	return ResolveComparisonProof(root, comparison.Candidate, ref)
}
