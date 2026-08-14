package workflow

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
)

func (s *Store) EnsureStep(ctx context.Context, runID, scope, stepID, foreachKey, activityRef string, inputs json.RawMessage) (*StepRun, error) {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO step_runs(run_id,scope_path,step_id,foreach_key,activity_ref,inputs_json,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(run_id,scope_path,step_id,foreach_key) DO NOTHING`, runID, scope, stepID, foreachKey, activityRef, []byte(inputs), StepReady, stamp(now), stamp(now))
	if err != nil {
		return nil, err
	}
	if inserted, _ := result.RowsAffected(); inserted == 1 {
		if err = insertEvent(ctx, tx, runID, "step_ready", map[string]any{"step_id": stepID, "scope_path": scope, "foreach_key": foreachKey, "activity": activityRef}, now); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetStep(ctx, runID, scope, stepID, foreachKey)
}

func (s *Store) GetStep(ctx context.Context, runID, scope, stepID, foreachKey string) (*StepRun, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,run_id,scope_path,step_id,foreach_key,activity_ref,inputs_json,output_json,status,error_class,error,created_at,updated_at FROM step_runs WHERE run_id=? AND scope_path=? AND step_id=? AND foreach_key=?`, runID, scope, stepID, foreachKey)
	var step StepRun
	var created, updated string
	var inputs, output []byte
	if err := row.Scan(&step.ID, &step.RunID, &step.ScopePath, &step.StepID, &step.ForeachKey, &step.ActivityRef, &inputs, &output, &step.Status, &step.ErrorClass, &step.Error, &created, &updated); err != nil {
		return nil, err
	}
	step.Inputs, step.Output = json.RawMessage(inputs), json.RawMessage(output)
	step.CreatedAt = parseStamp(created)
	step.UpdatedAt = parseStamp(updated)
	return &step, nil
}

func (s *Store) StartAttempt(ctx context.Context, step *StepRun, activityRef, key string) (*Attempt, error) {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var number int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(number),0)+1 FROM attempts WHERE step_run_id=?`, step.ID).Scan(&number); err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO attempts(step_run_id,number,activity_ref,idempotency_key,status,started_at) VALUES(?,?,?,?,?,?)`, step.ID, number, activityRef, key, AttemptRunning, stamp(now))
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	if _, err = tx.ExecContext(ctx, `UPDATE step_runs SET status=?,updated_at=? WHERE id=?`, StepRunning, stamp(now), step.ID); err != nil {
		return nil, err
	}
	if err = insertEvent(ctx, tx, step.RunID, "attempt_started", map[string]any{"step_id": step.StepID, "scope_path": step.ScopePath, "foreach_key": step.ForeachKey, "attempt": number, "activity": activityRef}, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &Attempt{ID: id, StepRunID: step.ID, Number: number, ActivityRef: activityRef, IdempotencyKey: key, Status: AttemptRunning, StartedAt: now}, nil
}

func (s *Store) CompleteAttempt(ctx context.Context, step *StepRun, attempt *Attempt, result activity.Result) error {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE attempts SET status=?,receipt=?,cost_usd=?,output_json=?,finished_at=? WHERE id=?`, AttemptSucceeded, result.Receipt, result.CostUSD, []byte(result.Output), stamp(now), attempt.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE step_runs SET status=?,output_json=?,error_class='',error='',updated_at=? WHERE id=?`, StepSucceeded, []byte(result.Output), stamp(now), step.ID); err != nil {
		return err
	}
	for _, artifact := range result.Artifacts {
		meta, _ := json.Marshal(artifact.Meta)
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO artifacts(run_id,step_run_id,kind,uri,digest,metadata_json) VALUES(?,?,?,?,?,?)`, step.RunID, step.ID, artifact.Kind, artifact.URI, artifact.Digest, meta); err != nil {
			return err
		}
	}
	if err = insertEvent(ctx, tx, step.RunID, "activity_completed", map[string]any{"step_id": step.StepID, "scope_path": step.ScopePath, "foreach_key": step.ForeachKey, "attempt": attempt.Number, "activity": attempt.ActivityRef}, now); err != nil {
		return err
	}
	if err = insertEvent(ctx, tx, step.RunID, "step_completed", map[string]any{"step_id": step.StepID, "scope_path": step.ScopePath, "foreach_key": step.ForeachKey, "attempt": attempt.Number}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SkipStep(ctx context.Context, step *StepRun) error {
	return s.setStepTerminal(ctx, step, StepSkipped, "", "", "step_skipped")
}

func (s *Store) CompleteVirtualStep(ctx context.Context, step *StepRun, output json.RawMessage) error {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE step_runs SET status=?,output_json=?,error_class='',error='',updated_at=? WHERE id=?`, StepSucceeded, []byte(output), stamp(now), step.ID); err != nil {
		return err
	}
	if err = insertEvent(ctx, tx, step.RunID, "step_completed", map[string]any{"step_id": step.StepID, "scope_path": step.ScopePath, "foreach_key": step.ForeachKey, "virtual": true}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ResetStepReady(ctx context.Context, stepID int64) error {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var runID, logicalID, scope, foreachKey string
	if err = tx.QueryRowContext(ctx, `SELECT run_id,step_id,scope_path,foreach_key FROM step_runs WHERE id=?`, stepID).Scan(&runID, &logicalID, &scope, &foreachKey); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE step_runs SET status=?,updated_at=? WHERE id=?`, StepReady, stamp(now), stepID); err != nil {
		return err
	}
	if err = insertEvent(ctx, tx, runID, "step_ready", map[string]any{"step_id": logicalID, "scope_path": scope, "foreach_key": foreachKey, "recovered": true}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FailAttempt(ctx context.Context, step *StepRun, attempt *Attempt, result activity.Result, class activity.ErrorClass, cause error, terminal bool) error {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	status := StepReady
	if terminal {
		status = StepFailed
	}
	if _, err = tx.ExecContext(ctx, `UPDATE attempts SET status=?,error_class=?,error=?,receipt=?,cost_usd=?,output_json=?,finished_at=? WHERE id=?`, AttemptFailed, class, cause.Error(), result.Receipt, result.CostUSD, []byte(result.Output), stamp(now), attempt.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE step_runs SET status=?,output_json=?,error_class=?,error=?,updated_at=? WHERE id=?`, status, []byte(result.Output), class, cause.Error(), stamp(now), step.ID); err != nil {
		return err
	}
	for _, artifact := range result.Artifacts {
		meta, _ := json.Marshal(artifact.Meta)
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO artifacts(run_id,step_run_id,kind,uri,digest,metadata_json) VALUES(?,?,?,?,?,?)`, step.RunID, step.ID, artifact.Kind, artifact.URI, artifact.Digest, meta); err != nil {
			return err
		}
	}
	if err = insertEvent(ctx, tx, step.RunID, "activity_failed", map[string]any{"step_id": step.StepID, "attempt": attempt.Number, "error_class": class, "error": cause.Error(), "terminal": terminal}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) setStepTerminal(ctx context.Context, step *StepRun, status StepStatus, class activity.ErrorClass, message, event string) error {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE step_runs SET status=?,error_class=?,error=?,updated_at=? WHERE id=?`, status, class, message, stamp(now), step.ID); err != nil {
		return err
	}
	if err = insertEvent(ctx, tx, step.RunID, event, map[string]any{"step_id": step.StepID, "scope_path": step.ScopePath, "foreach_key": step.ForeachKey}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetRunStatus(ctx context.Context, runID string, status RunStatus, message string) error {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current RunStatus
	if err = tx.QueryRowContext(ctx, `SELECT status FROM workflow_runs WHERE id=?`, runID).Scan(&current); err != nil {
		return err
	}
	if current == status {
		return tx.Commit()
	}
	if !validRunTransition(current, status) {
		return fmt.Errorf("invalid workflow transition %s -> %s", current, status)
	}
	var finished any = nil
	if status == RunSucceeded || status == RunFailed || status == RunCancelled {
		finished = stamp(now)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET status=?,error=?,updated_at=?,finished_at=? WHERE id=?`, status, message, stamp(now), finished, runID); err != nil {
		return err
	}
	if err = insertEvent(ctx, tx, runID, "workflow_"+string(status), map[string]any{"error": message}, now); err != nil {
		return err
	}
	if status == RunSucceeded || status == RunFailed || status == RunCancelled {
		if err = insertEvent(ctx, tx, runID, "workflow_completed", map[string]any{"status": status, "error": message}, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func validRunTransition(from, to RunStatus) bool {
	switch from {
	case RunPending:
		return to == RunRunning || to == RunCancelled
	case RunRunning:
		return to == RunSucceeded || to == RunFailed || to == RunCancelled || to == RunNeedsHuman
	case RunNeedsHuman:
		return to == RunRunning || to == RunFailed || to == RunCancelled
	case RunFailed:
		return to == RunRunning || to == RunCancelled
	case RunSucceeded, RunCancelled:
		return false
	default:
		return false
	}
}

func insertEvent(ctx context.Context, tx *sql.Tx, runID, event string, fields map[string]any, now time.Time) error {
	id := newEventID()
	payload := map[string]any{"event_id": id, "ts": stamp(now), "event": event, "run_id": runID}
	for key, value := range fields {
		payload[key] = value
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO outbox_events(event_id,run_id,payload_json,created_at) VALUES(?,?,?,?)`, id, runID, raw, stamp(now))
	return err
}
func newEventID() string {
	raw := make([]byte, 8)
	_, _ = rand.Read(raw)
	return "e" + hex.EncodeToString(raw)
}

func (s *Store) LatestAttempt(ctx context.Context, stepRunID int64) (*Attempt, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,step_run_id,number,activity_ref,idempotency_key,status,error_class,error,receipt,cost_usd,output_json,started_at,finished_at FROM attempts WHERE step_run_id=? ORDER BY number DESC LIMIT 1`, stepRunID)
	var a Attempt
	var started string
	var finished sql.NullString
	var output []byte
	if err := row.Scan(&a.ID, &a.StepRunID, &a.Number, &a.ActivityRef, &a.IdempotencyKey, &a.Status, &a.ErrorClass, &a.Error, &a.Receipt, &a.CostUSD, &output, &started, &finished); err != nil {
		return nil, err
	}
	a.Output = json.RawMessage(output)
	a.StartedAt = parseStamp(started)
	if finished.Valid {
		v := parseStamp(finished.String)
		a.FinishedAt = &v
	}
	return &a, nil
}

func (s *Store) MarkAttemptUnknown(ctx context.Context, step *StepRun, attempt *Attempt) error {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	message := "process stopped before the activity outcome was confirmed"
	if _, err = tx.ExecContext(ctx, `UPDATE attempts SET status=?,error_class=?,error=?,finished_at=? WHERE id=?`, AttemptOutcomeUnknown, activity.ErrorOutcomeUnknown, message, stamp(now), attempt.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE step_runs SET status=?,error_class=?,error=?,updated_at=? WHERE id=?`, StepOutcomeUnknown, activity.ErrorOutcomeUnknown, message, stamp(now), step.ID); err != nil {
		return err
	}
	if err = insertEvent(ctx, tx, step.RunID, "activity_outcome_unknown", map[string]any{"step_id": step.StepID, "scope_path": step.ScopePath, "foreach_key": step.ForeachKey, "attempt": attempt.Number}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateApproval(ctx context.Context, step *StepRun, reason string) (*Approval, error) {
	now := s.now().UTC()
	id := "a" + newEventID()[1:]
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO approvals(id,run_id,step_run_id,reason,status,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT(run_id,step_run_id) DO NOTHING`, id, step.RunID, step.ID, reason, "pending", stamp(now))
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE step_runs SET status=?,updated_at=? WHERE id=?`, StepNeedsHuman, stamp(now), step.ID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET status=?,updated_at=? WHERE id=?`, RunNeedsHuman, stamp(now), step.RunID); err != nil {
		return nil, err
	}
	if err = insertEvent(ctx, tx, step.RunID, "approval_requested", map[string]any{"approval_id": id, "step_id": step.StepID, "reason": reason}, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetApprovalForStep(ctx, step.ID)
}

func (s *Store) GetApprovalForStep(ctx context.Context, stepID int64) (*Approval, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,run_id,step_run_id,reason,status,decision,created_at,decided_at FROM approvals WHERE step_run_id=?`, stepID)
	var a Approval
	var created string
	var decided sql.NullString
	if err := row.Scan(&a.ID, &a.RunID, &a.StepRunID, &a.Reason, &a.Status, &a.Decision, &created, &decided); err != nil {
		return nil, err
	}
	a.CreatedAt = parseStamp(created)
	if decided.Valid {
		v := parseStamp(decided.String)
		a.DecidedAt = &v
	}
	return &a, nil
}
func (s *Store) DecideApproval(ctx context.Context, runID, id, decision string) error {
	if decision != "approve" && decision != "reject" {
		return fmt.Errorf("decision must be approve or reject")
	}
	now := s.now().UTC()
	status := "approved"
	if decision == "reject" {
		status = "rejected"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE approvals SET status=?,decision=?,decided_at=? WHERE id=? AND run_id=? AND status='pending'`, status, decision, stamp(now), id, runID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return fmt.Errorf("pending approval %s not found", id)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET status=?,updated_at=? WHERE id=?`, RunRunning, stamp(now), runID); err != nil {
		return err
	}
	if err = insertEvent(ctx, tx, runID, "approval_decided", map[string]any{"approval_id": id, "decision": decision}, now); err != nil {
		return err
	}
	return tx.Commit()
}
