package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/checks"
	"github.com/collectiveai-team/orquesta-lite/internal/review"
	"github.com/collectiveai-team/orquesta-lite/internal/risk"
)

// UnitCloseExecutor is the only approval boundary of a verified unit. A human
// may accept review uncertainty, but cannot substitute narration for checks.
type UnitCloseExecutor struct {
	ProjectDir     string
	Store          risk.UnitStore
	Profile        *checks.Resolved
	ArtifactRoot   string
	Approved       func(context.Context, activity.Request) (bool, error)
	ValidateReview func(context.Context, review.ResultV3) error
	ValidateChecks func(context.Context, ChecksOutput) error
}

func (*UnitCloseExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "unit.close", Version: "1", Effect: activity.EffectIdempotent}
}

type unitCloseInput struct {
	Unit     string          `json:"unit"`
	Snapshot checks.Snapshot `json:"snapshot"`
	Checks   ChecksOutput    `json:"checks"`
	Review   review.ResultV3 `json:"review"`
}
type unitSeal struct {
	unitCloseInput
	WaivedReview bool   `json:"waived_review"`
	Receipt      string `json:"receipt"`
	Digest       string `json:"digest"`
	Closed       bool   `json:"closed"`
}

func (e *UnitCloseExecutor) verify(ctx context.Context, in unitCloseInput) error {
	if in.Unit == "" || e.Profile == nil {
		return fmt.Errorf("compiled profile and unit required")
	}
	if !within(e.ArtifactRoot, in.Snapshot.Dir) {
		return fmt.Errorf("unit snapshot outside run")
	}
	if in.Snapshot.TreeHash != in.Checks.Snapshot.TreeHash || in.Snapshot.TreeHash != in.Review.ReviewedRevision {
		return fmt.Errorf("unit evidence belongs to another tree")
	}
	raw, _ := json.Marshal(in.Checks)
	if err := VerifyOutput(raw); err != nil {
		return err
	}
	if in.Checks.ProfileHash != e.Profile.SourceHash {
		return fmt.Errorf("unit profile digest mismatch")
	}
	if e.ValidateChecks == nil {
		return fmt.Errorf("runtime checks verifier missing")
	}
	if err := e.ValidateChecks(ctx, in.Checks); err != nil {
		return err
	}
	u, err := e.Store.Load(in.Unit)
	if err != nil {
		return err
	}
	if u.Reservation != "" {
		if err := e.Store.Verify(in.Unit); err != nil {
			return err
		}
		if u.Validation == nil || u.Validation.TreeHash != in.Snapshot.TreeHash {
			return fmt.Errorf("closure does not match validated correction")
		}
	} else if u.Risk.Candidate.TreeHash != in.Snapshot.TreeHash {
		return fmt.Errorf("closure does not match classified candidate")
	}
	if e.ValidateReview == nil {
		return fmt.Errorf("runtime review verifier missing")
	}
	return e.ValidateReview(ctx, in.Review)
}

func (e *UnitCloseExecutor) Execute(ctx context.Context, request activity.Request) (activity.Result, error) {
	var in unitCloseInput
	if err := strictJSON(request.Inputs, &in); err != nil {
		return activity.Result{}, contractError("unit.close", err)
	}
	if err := e.verify(ctx, in); err != nil {
		return activity.Result{}, &activity.ApprovalRequiredError{Reason: "unit evidence cannot close: " + err.Error(), Input: request.Inputs}
	}
	if e.ProjectDir != "" {
		tree, err := checks.CurrentTree(ctx, e.ProjectDir)
		if err != nil {
			return activity.Result{}, err
		}
		if tree != in.Snapshot.TreeHash {
			return activity.Result{}, &activity.ApprovalRequiredError{Reason: "live candidate changed after verification; obtain new runtime evidence", Input: request.Inputs}
		}
	}
	waived := false
	if !in.Review.Approved {
		if e.Approved != nil {
			var err error
			waived, err = e.Approved(ctx, request)
			if err != nil {
				return activity.Result{}, err
			}
		}
		if !waived {
			return activity.Result{}, &activity.ApprovalRequiredError{Reason: "review requires a decision: " + in.Review.Decision + "; " + in.Review.Summary, Input: request.Inputs}
		}
	}
	directory := filepath.Join(e.ArtifactRoot, "units", "closures")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return activity.Result{}, err
	}
	path := filepath.Join(directory, checks.Hash([]byte(in.Unit+"\x00"+in.Snapshot.TreeHash))+".json")
	seal := unitSeal{unitCloseInput: in, WaivedReview: waived, Receipt: path, Closed: true}
	body, _ := json.Marshal(seal)
	seal.Digest = checks.Hash(body)
	raw, _ := json.Marshal(seal)
	if existing, err := os.ReadFile(path); err == nil {
		if string(existing) != string(raw) {
			return activity.Result{}, fmt.Errorf("unit closure already exists with different evidence")
		}
	} else if !os.IsNotExist(err) {
		return activity.Result{}, err
	} else if err = os.WriteFile(path, raw, 0600); err != nil {
		return activity.Result{}, err
	}
	return activity.Result{Output: raw, Artifacts: []activity.Artifact{{Kind: "unit_closure", URI: path, Digest: checks.Hash(raw)}}}, nil
}

func (e *UnitCloseExecutor) ValidateEvidence(ctx context.Context, _ activity.Request, result activity.Result) error {
	var seal unitSeal
	if err := strictJSON(result.Output, &seal); err != nil {
		return err
	}
	if !seal.Closed || !within(e.ArtifactRoot, seal.Receipt) {
		return fmt.Errorf("invalid unit closure")
	}
	raw, err := os.ReadFile(seal.Receipt)
	if err != nil {
		return err
	}
	if string(raw) != string(result.Output) {
		return fmt.Errorf("closure receipt differs from persisted result")
	}
	digest := seal.Digest
	seal.Digest = ""
	body, _ := json.Marshal(seal)
	if checks.Hash(body) != digest {
		return fmt.Errorf("unit closure digest mismatch")
	}
	if err := e.verify(ctx, seal.unitCloseInput); err != nil {
		return err
	}
	if !seal.Review.Approved && !seal.WaivedReview {
		return fmt.Errorf("review lacks approval or recorded waiver")
	}
	return nil
}
