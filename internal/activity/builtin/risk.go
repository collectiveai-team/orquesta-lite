package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/checks"
	"github.com/collectiveai-team/orquesta-lite/internal/review"
	"github.com/collectiveai-team/orquesta-lite/internal/risk"
)

type RiskExecutor struct {
	Store  risk.UnitStore
	Policy risk.Policy
	Limits risk.CorrectionLimits
	Checks *checks.Resolved
}

func (*RiskExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "risk.classify", Version: "1", Effect: activity.EffectIdempotent}
}
func candidate(s checks.Snapshot) risk.Candidate {
	return risk.Candidate{TreeHash: s.TreeHash, BaseRevision: s.BaseRevision, Paths: s.Paths, ChangedLines: s.ChangedLines}
}
func (e *RiskExecutor) Execute(_ context.Context, r activity.Request) (activity.Result, error) {
	var input struct {
		Unit     string          `json:"unit"`
		Snapshot checks.Snapshot `json:"snapshot"`
	}
	if err := strictJSON(r.Inputs, &input); err != nil {
		return activity.Result{}, contractError("risk.classify", err)
	}
	if err := checks.VerifySnapshot(input.Snapshot); err != nil {
		return activity.Result{}, &activity.ApprovalRequiredError{Reason: "risk tree unavailable: " + err.Error(), Input: r.Inputs}
	}
	c := candidate(input.Snapshot)
	if e.Checks != nil {
		for _, check := range e.Checks.Profile.Items {
			if check.Kind == "unit" || check.Kind == "integration" || check.Kind == "e2e" {
				if len(check.Paths) > 0 {
					c.CoverageKnown = true
				}
			}
		}
		for _, file := range c.Paths {
			covered := false
			for _, check := range e.Checks.Profile.Items {
				if check.Kind != "unit" && check.Kind != "integration" && check.Kind != "e2e" {
					continue
				}
				for _, pattern := range check.Paths {
					covered = covered || checks.Match(pattern, file)
				}
			}
			if !covered {
				c.UncoveredPaths = append(c.UncoveredPaths, file)
			}
		}
	}
	u, err := e.Store.Initialize(input.Unit, c, e.Policy, e.Limits)
	if err != nil {
		return activity.Result{}, contractError("risk.classify", err)
	}
	tasks := u.Risk.Lenses
	if len(tasks) == 0 {
		tasks = []string{"structural"}
	}
	raw, _ := json.Marshal(map[string]any{"unit": u.ID, "decision": u.Risk, "review_tasks": tasks, "patch_lines": u.PatchLines, "status": u.Status})
	return activity.Result{Output: raw, Artifacts: []activity.Artifact{{Kind: "risk_unit", URI: e.Store.Path(u.ID)}}}, nil
}

func (e *RiskExecutor) ValidateEvidence(_ context.Context, request activity.Request, result activity.Result) error {
	var in struct {
		Unit     string          `json:"unit"`
		Snapshot checks.Snapshot `json:"snapshot"`
	}
	if err := strictJSON(request.Inputs, &in); err != nil {
		return err
	}
	if err := checks.VerifySnapshot(in.Snapshot); err != nil {
		return err
	}
	var out struct {
		Unit     string        `json:"unit"`
		Decision risk.Decision `json:"decision"`
	}
	if err := json.Unmarshal(result.Output, &out); err != nil {
		return err
	}
	u, err := e.Store.Load(in.Unit)
	if err != nil {
		return err
	}
	if out.Unit != u.ID || !reflect.DeepEqual(out.Decision, u.Risk) {
		return fmt.Errorf("risk decision differs from pinned unit")
	}
	return nil
}

type CorrectionReserveExecutor struct{ Store risk.UnitStore }

func (*CorrectionReserveExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "correction.reserve", Version: "1", Effect: activity.EffectIdempotent}
}
func (e *CorrectionReserveExecutor) Execute(_ context.Context, r activity.Request) (activity.Result, error) {
	var in struct {
		Unit string `json:"unit"`
	}
	if err := strictJSON(r.Inputs, &in); err != nil {
		return activity.Result{}, contractError("correction.reserve", err)
	}
	u, err := e.Store.BeginCorrection(in.Unit, r.IdempotencyKey)
	if errors.Is(err, risk.ErrNeedsDecision) {
		return activity.Result{}, &activity.ApprovalRequiredError{Reason: err.Error(), Input: r.Inputs}
	}
	if err != nil {
		return activity.Result{}, err
	}
	raw, _ := json.Marshal(u)
	return activity.Result{Output: raw}, nil
}

type CorrectionValidateExecutor struct {
	Store   risk.UnitStore
	Policy  risk.Policy
	Measure func(context.Context, risk.Unit, checks.Snapshot, ChecksOutput, review.ResultV3) (risk.Validation, error)
}

func (*CorrectionValidateExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "correction.validate", Version: "1", Effect: activity.EffectIdempotent}
}
func (e *CorrectionValidateExecutor) Execute(ctx context.Context, r activity.Request) (activity.Result, error) {
	var in struct {
		Unit     string          `json:"unit"`
		Snapshot checks.Snapshot `json:"snapshot"`
		Checks   ChecksOutput    `json:"checks"`
		Review   review.ResultV3 `json:"review"`
	}
	if err := strictJSON(r.Inputs, &in); err != nil {
		return activity.Result{}, contractError("correction.validate", err)
	}
	u, err := e.Store.Load(in.Unit)
	if err != nil {
		return activity.Result{}, err
	}
	if u.Status == "verified" {
		raw, _ := json.Marshal(u)
		out := activity.Result{Output: raw}
		if err := e.Store.Verify(in.Unit); err != nil {
			return out, &activity.ApprovalRequiredError{Reason: err.Error(), Input: r.Inputs}
		}
		if u.Validation.TreeHash != in.Snapshot.TreeHash {
			return out, &activity.ApprovalRequiredError{Reason: "validation replay refers to another candidate", Input: r.Inputs}
		}
		return out, nil
	}
	if u.Status == "needs_decision" {
		raw, _ := json.Marshal(u)
		return activity.Result{Output: raw}, &activity.ApprovalRequiredError{Reason: "correction exhausted; a human decision is required", Input: r.Inputs}
	}
	if e.Measure == nil {
		return activity.Result{}, fmt.Errorf("correction.validate requires runtime measurement callback")
	}
	v, err := e.Measure(ctx, u, in.Snapshot, in.Checks, in.Review)
	if err != nil {
		return activity.Result{}, err
	}
	u, err = e.Store.FinishValidation(in.Unit, v, candidate(in.Snapshot), e.Policy)
	raw, _ := json.Marshal(u)
	out := activity.Result{Output: raw}
	if errors.Is(err, risk.ErrNeedsDecision) {
		return out, &activity.ApprovalRequiredError{Reason: err.Error(), Input: r.Inputs}
	}
	if err != nil {
		return out, err
	}
	if u.Status == "correcting" {
		return out, &activity.Error{Class: activity.ErrorTransient, Op: "correction.validate", Err: fmt.Errorf("tree unreadable; retry validation without another correction")}
	}
	return out, nil
}
