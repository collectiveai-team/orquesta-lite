package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/execlock"
)

// Parent subflows may already be cached as succeeded when a nested receipt is
// lost. Reconcile approved integrity checkpoints before replaying that cache.
func (r *Runtime) reconcileEvidenceApprovals(ctx context.Context, run *Run) error {
	approvals, err := r.Store.ListApprovals(ctx, run.ID)
	if err != nil {
		return err
	}
	for _, approval := range approvals {
		if approval.Status == "pending" {
			if run.Status == RunRunning {
				_ = r.Store.SetRunStatus(ctx, run.ID, RunNeedsHuman, approval.Reason)
			}
			return ErrNeedsHuman
		}
		if approval.Status == "rejected" {
			return fmt.Errorf("approval %s rejected", approval.ID)
		}
	}
	steps, err := r.Store.ListSteps(ctx, run.ID)
	if err != nil {
		return err
	}
	for i := range steps {
		step := &steps[i]
		if step.Status != StepNeedsHuman {
			continue
		}
		attempt, err := r.Store.LatestAttempt(ctx, step.ID)
		if err != nil || attempt.Status != AttemptSucceeded {
			continue
		}
		executor, ok := r.Activities.Resolve(activity.ExecutorRef(step.ActivityRef))
		if !ok {
			return fmt.Errorf("evidence executor missing")
		}
		validator, ok := executor.(activity.EvidenceValidator)
		if !ok {
			return fmt.Errorf("completed effect has no evidence validator")
		}
		request, result, err := r.Store.EvidenceRequest(ctx, *step)
		if err != nil {
			return err
		}
		if err := validator.ValidateEvidence(ctx, request, result); err != nil {
			if _, persistErr := r.Store.CreateApproval(ctx, step, "evidence remains invalid: "+err.Error()); persistErr != nil {
				return persistErr
			}
			return ErrNeedsHuman
		}
		if err := r.Store.RestoreEvidenceStep(ctx, step); err != nil {
			return err
		}
	}
	return nil
}

// ValidateEvidence inspects an existing trigger without scheduling its work.
func (r *Runtime) ValidateEvidence(ctx context.Context, id string) (*Run, error) {
	release, err := execlock.Acquire(r.Store.executorLock)
	if err != nil {
		return nil, err
	}
	defer release()
	run, err := r.Store.GetRun(ctx, id)
	if err != nil {
		return nil, err
	}
	if run.Status == RunCancelled {
		return run, nil
	}
	err = r.validateRunEvidence(ctx, run)
	latest, loadErr := r.Store.GetRun(ctx, id)
	if err != nil {
		return latest, err
	}
	return latest, loadErr
}

// StepArtifacts returns the proof registry persisted alongside the completed
// result. Revalidation must not silently discard this registry on resume.
func (s *Store) StepArtifacts(ctx context.Context, id int64) ([]activity.Artifact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind,uri,digest,metadata_json FROM artifacts WHERE step_run_id=? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var artifacts []activity.Artifact
	for rows.Next() {
		var artifact activity.Artifact
		var raw []byte
		if err := rows.Scan(&artifact.Kind, &artifact.URI, &artifact.Digest, &raw); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &artifact.Meta); err != nil {
				return nil, err
			}
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, rows.Err()
}

func (s *Store) EvidenceRequest(ctx context.Context, step StepRun) (activity.Request, activity.Result, error) {
	r := activity.Request{RunID: step.RunID, StepID: step.StepID, ScopePath: step.ScopePath, ForeachKey: step.ForeachKey, Inputs: step.Inputs}
	attempt, err := s.LatestAttempt(ctx, step.ID)
	if err != nil {
		return r, activity.Result{}, err
	}
	r.Attempt = attempt.Number
	r.IdempotencyKey = attempt.IdempotencyKey
	artifacts, err := s.StepArtifacts(ctx, step.ID)
	return r, activity.Result{Output: step.Output, Artifacts: artifacts, Receipt: attempt.Receipt, CostUSD: attempt.CostUSD}, err
}

func (s *Store) RestoreEvidenceStep(ctx context.Context, step *StepRun) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	if _, err = tx.ExecContext(ctx, `UPDATE step_runs SET status=?,error_class='',error='',updated_at=? WHERE id=? AND status=?`, StepSucceeded, stamp(now), step.ID, StepNeedsHuman); err != nil {
		return err
	}
	if err = insertEvent(ctx, tx, step.RunID, "evidence_revalidated", map[string]any{"step_id": step.StepID, "scope_path": step.ScopePath, "replayed_effect": false}, now); err != nil {
		return err
	}
	return tx.Commit()
}
