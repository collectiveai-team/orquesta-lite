package workflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/external"
)

const schemaV4 = `
CREATE TABLE external_attempts (
 id TEXT PRIMARY KEY,
 attempt_id INTEGER NOT NULL UNIQUE REFERENCES attempts(id) ON DELETE CASCADE,
 step_run_id INTEGER NOT NULL REFERENCES step_runs(id) ON DELETE CASCADE,
 run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
 step_id TEXT NOT NULL,
 employee_id TEXT NOT NULL,
 request_json BLOB NOT NULL,
 references_json BLOB,
 result_json BLOB,
 cancellation_json BLOB,
 status TEXT NOT NULL,
 outcome TEXT NOT NULL DEFAULT '',
 decision TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 last_checked_at TEXT
);
CREATE INDEX external_attempts_run ON external_attempts(run_id,step_run_id);
CREATE TABLE external_events (
 attempt_id TEXT NOT NULL REFERENCES external_attempts(id) ON DELETE CASCADE,
 event_id TEXT NOT NULL,
 event_json BLOB NOT NULL,
 disposition TEXT NOT NULL,
 received_at TEXT NOT NULL,
 processed_at TEXT,
 PRIMARY KEY(attempt_id,event_id)
);
CREATE INDEX external_events_pending ON external_events(disposition,attempt_id);
`

// ExternalAttempt is the durable correlation and audit record for one assignment.
// Request and employee resolution are immutable, including across process restarts.
type ExternalAttempt struct {
	ID            string                   `json:"id"`
	AttemptID     int64                    `json:"attemptId"`
	Number        int                      `json:"number"`
	StepRunID     int64                    `json:"stepRunId"`
	RunID         string                   `json:"runId"`
	StepID        string                   `json:"stepId"`
	EmployeeID    string                   `json:"employeeId"`
	Request       external.DispatchRequest `json:"request"`
	References    external.References      `json:"references"`
	Result        *external.Result         `json:"result,omitempty"`
	Cancellation  *external.Cancellation   `json:"cancellation,omitempty"`
	Status        string                   `json:"status"`
	Outcome       string                   `json:"outcome,omitempty"`
	Decision      string                   `json:"decision,omitempty"`
	PendingEvents int                      `json:"pendingEvents"`
	CreatedAt     time.Time                `json:"createdAt"`
	UpdatedAt     time.Time                `json:"updatedAt"`
	LastCheckedAt *time.Time               `json:"lastCheckedAt,omitempty"`
	Usage         *external.Usage          `json:"usage,omitempty"`
	UsageRunCount int                      `json:"usageRunCount,omitempty"`
}

var ErrExternalInactive = errors.New("external step is not active")

const externalSelect = `SELECT e.id,e.attempt_id,a.number,e.step_run_id,e.run_id,e.step_id,e.employee_id,e.request_json,e.references_json,e.result_json,e.cancellation_json,e.status,e.outcome,e.decision,e.created_at,e.updated_at,e.last_checked_at,(SELECT COUNT(*) FROM external_events v WHERE v.attempt_id=e.id AND v.disposition='pending'),(SELECT SUM(cost_usd) FROM external_usage u WHERE u.attempt_id=e.id),(SELECT SUM(input_tokens) FROM external_usage u WHERE u.attempt_id=e.id),(SELECT SUM(output_tokens) FROM external_usage u WHERE u.attempt_id=e.id),(SELECT COUNT(*) FROM external_usage u WHERE u.attempt_id=e.id) FROM external_attempts e JOIN attempts a ON a.id=e.attempt_id`

func scanExternal(row rowScanner) (*ExternalAttempt, error) {
	var e ExternalAttempt
	var request, refs, result, cancellation []byte
	var created, updated string
	var checked sql.NullString
	var cost sql.NullFloat64
	var inputTokens, outputTokens sql.NullInt64
	if err := row.Scan(&e.ID, &e.AttemptID, &e.Number, &e.StepRunID, &e.RunID, &e.StepID, &e.EmployeeID, &request, &refs, &result, &cancellation, &e.Status, &e.Outcome, &e.Decision, &created, &updated, &checked, &e.PendingEvents, &cost, &inputTokens, &outputTokens, &e.UsageRunCount); err != nil {
		return nil, err
	}
	if e.UsageRunCount > 0 {
		e.Usage = &external.Usage{}
		if cost.Valid {
			e.Usage.CostUSD = &cost.Float64
		}
		if inputTokens.Valid {
			e.Usage.InputTokens = &inputTokens.Int64
		}
		if outputTokens.Valid {
			e.Usage.OutputTokens = &outputTokens.Int64
		}
	}
	for _, v := range []struct {
		raw    []byte
		target any
	}{{request, &e.Request}, {refs, &e.References}, {result, &e.Result}, {cancellation, &e.Cancellation}} {
		if len(v.raw) > 0 {
			if err := json.Unmarshal(v.raw, v.target); err != nil {
				return nil, fmt.Errorf("external attempt %s: %w", e.ID, err)
			}
		}
	}
	e.CreatedAt, e.UpdatedAt = parseStamp(created), parseStamp(updated)
	if checked.Valid {
		v := parseStamp(checked.String)
		e.LastCheckedAt = &v
	}
	return &e, nil
}

func (s *Store) GetExternalAttempt(ctx context.Context, id string) (*ExternalAttempt, error) {
	return scanExternal(s.db.QueryRowContext(ctx, externalSelect+` WHERE e.id=?`, id))
}
func (s *Store) LatestExternalAttempt(ctx context.Context, stepRunID int64) (*ExternalAttempt, error) {
	return scanExternal(s.db.QueryRowContext(ctx, externalSelect+` WHERE e.step_run_id=? ORDER BY a.number DESC LIMIT 1`, stepRunID))
}
func (s *Store) ListExternalAttempts(ctx context.Context, runID string) ([]ExternalAttempt, error) {
	query := externalSelect
	var args []any
	if runID != "" {
		query += ` WHERE e.run_id=?`
		args = append(args, runID)
	}
	query += ` ORDER BY e.step_run_id,a.number`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []ExternalAttempt{}
	for rows.Next() {
		e, err := scanExternal(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, *e)
	}
	return records, rows.Err()
}

// Include committed decisions until their run is terminal: a crash may occur
// between the atomic external decision and scheduling its continuation.
func (s *Store) externalRecoveryRuns(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT e.run_id FROM external_attempts e JOIN workflow_runs r ON r.id=e.run_id WHERE r.status IN (?,?) ORDER BY e.run_id`, RunRunning, RunWaitingExternal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// PrepareExternal commits the intent before any remote operation. Recovery must
// reuse its ID and request; only an explicit retry can create another attempt.
func (s *Store) PrepareExternal(ctx context.Context, step *StepRun, activityRef string, request external.DispatchRequest) (*ExternalAttempt, error) {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var runStatus RunStatus
	var stepStatus StepStatus
	if err = tx.QueryRowContext(ctx, `SELECT r.status,s.status FROM workflow_runs r JOIN step_runs s ON s.run_id=r.id WHERE s.id=? AND r.id=?`, step.ID, step.RunID).Scan(&runStatus, &stepStatus); err != nil {
		return nil, err
	}
	if runStatus != RunRunning && runStatus != RunWaitingExternal {
		return nil, ErrExternalInactive
	}
	if stepStatus == StepWaitingExternal || stepStatus == StepRunning {
		existing, loadErr := scanExternal(tx.QueryRowContext(ctx, externalSelect+` WHERE e.step_run_id=? AND e.attempt_id=(SELECT MAX(id) FROM attempts WHERE step_run_id=?) ORDER BY a.number DESC LIMIT 1`, step.ID, step.ID))
		if loadErr == nil && existing.Status != "decided" {
			return existing, nil
		}
		if loadErr != nil && !errors.Is(loadErr, sql.ErrNoRows) {
			return nil, loadErr
		}
		return nil, ErrExternalInactive
	}
	if stepStatus != StepReady && stepStatus != StepPending {
		return nil, ErrExternalInactive
	}
	if request.EmployeeID == "" {
		return nil, fmt.Errorf("external employee must resolve before preparing dispatch")
	}
	var number int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(number),0)+1 FROM attempts WHERE step_run_id=?`, step.ID).Scan(&number); err != nil {
		return nil, err
	}
	rawID := make([]byte, 16)
	if _, err = rand.Read(rawID); err != nil {
		return nil, err
	}
	id := "ext_" + hex.EncodeToString(rawID)
	request.FlowRunID, request.StepID, request.AttemptID = step.RunID, step.StepID, id
	request.IdempotencyKey = "orq-lite/" + id
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	inserted, err := tx.ExecContext(ctx, `INSERT INTO attempts(step_run_id,number,activity_ref,idempotency_key,status,started_at) VALUES(?,?,?,?,?,?)`, step.ID, number, activityRef, request.IdempotencyKey, AttemptRunning, stamp(now))
	if err != nil {
		return nil, err
	}
	attemptID, err := inserted.LastInsertId()
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO external_attempts(id,attempt_id,step_run_id,run_id,step_id,employee_id,request_json,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, attemptID, step.ID, step.RunID, step.StepID, request.EmployeeID, encoded, "prepared", stamp(now), stamp(now)); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE step_runs SET status=?,updated_at=? WHERE id=?`, StepWaitingExternal, stamp(now), step.ID); err != nil {
		return nil, err
	}
	if err = insertEvent(ctx, tx, step.RunID, "external_dispatch_prepared", map[string]any{"step_id": step.StepID, "step_run_id": step.ID, "attempt_id": id, "attempt": number, "employee_id": request.EmployeeID}, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetExternalAttempt(ctx, id)
}

// SaveExternalDispatch may also run after a pause/cancel so an accepted remote
// assignment remains discoverable for cancellation and reconciliation.
func (s *Store) SaveExternalDispatch(ctx context.Context, id string, refs external.References) error {
	if refs.IssueID == "" {
		return fmt.Errorf("external dispatch returned no issue reference")
	}
	now := s.now().UTC()
	raw, err := json.Marshal(refs)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var runID, stepID string
	var previous []byte
	if err = tx.QueryRowContext(ctx, `SELECT run_id,step_id,references_json FROM external_attempts WHERE id=?`, id).Scan(&runID, &stepID, &previous); err != nil {
		return err
	}
	if len(previous) > 0 {
		var existing external.References
		if err = json.Unmarshal(previous, &existing); err != nil {
			return err
		}
		if existing != refs {
			return fmt.Errorf("external dispatch references changed for attempt %s", id)
		}
		return nil
	}
	if _, err = tx.ExecContext(ctx, `UPDATE external_attempts SET references_json=?,status=CASE WHEN status='prepared' THEN 'dispatched' ELSE status END,updated_at=? WHERE id=?`, raw, stamp(now), id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE attempts SET receipt=? WHERE id=(SELECT attempt_id FROM external_attempts WHERE id=?)`, string(raw), id); err != nil {
		return err
	}
	if err = insertEvent(ctx, tx, runID, "external_dispatched", map[string]any{"step_id": stepID, "attempt_id": id, "references": refs}, now); err != nil {
		return err
	}
	return tx.Commit()
}

// ReceiveExternalEvent is a transport-neutral durable inbox. An old attempt is
// acknowledged and audited as ignored, never repurposed for the current attempt.
func (s *Store) ReceiveExternalEvent(ctx context.Context, runID, stepID, attemptID string, event external.Event) (bool, error) {
	if event.Type == "" {
		return false, fmt.Errorf("external event type is required")
	}
	now := s.now().UTC()
	raw, err := json.Marshal(event)
	if err != nil {
		return false, err
	}
	if len(raw) > maxWorkflowValueBytes {
		return false, fmt.Errorf("external event exceeds %d bytes", maxWorkflowValueBytes)
	}
	eventID := event.ID
	if eventID == "" {
		sum := sha256.Sum256(raw)
		eventID = hex.EncodeToString(sum[:])
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var status string
	var stepStatus StepStatus
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT e.status,s.status,e.attempt_id=(SELECT MAX(id) FROM attempts WHERE step_run_id=e.step_run_id) FROM external_attempts e JOIN step_runs s ON s.id=e.step_run_id WHERE e.id=? AND e.run_id=? AND e.step_id=?`, attemptID, runID, stepID).Scan(&status, &stepStatus, &active); err != nil {
		return false, err
	}
	disposition := "pending"
	if !active || status == "decided" || stepStatus != StepWaitingExternal {
		disposition = "ignored"
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO external_events(attempt_id,event_id,event_json,disposition,received_at) VALUES(?,?,?,?,?) ON CONFLICT(attempt_id,event_id) DO NOTHING`, attemptID, eventID, raw, disposition, stamp(now))
	if err != nil {
		return false, err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if count == 1 {
		if err = insertEvent(ctx, tx, runID, "external_event_received", map[string]any{"step_id": stepID, "attempt_id": attemptID, "external_event_id": eventID, "disposition": disposition}, now); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return count == 1 && disposition == "pending", nil
}

func (s *Store) RecordExternalCheck(ctx context.Context, id string, result external.Result, outcome string) error {
	now := s.now().UTC()
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var runID, stepID, status string
	if err = tx.QueryRowContext(ctx, `SELECT run_id,step_id,status FROM external_attempts WHERE id=?`, id).Scan(&runID, &stepID, &status); err != nil {
		return err
	}
	if status == "decided" {
		return nil
	}
	if err = updateExternalReferences(ctx, tx, id, result.References); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE external_attempts SET result_json=?,outcome=?,last_checked_at=?,updated_at=? WHERE id=?`, raw, outcome, stamp(now), stamp(now), id); err != nil {
		return err
	}
	if err = recordExternalUsage(ctx, tx, id, result); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE external_events SET disposition='processed',processed_at=? WHERE attempt_id=? AND disposition='pending'`, stamp(now), id); err != nil {
		return err
	}
	if err = insertEvent(ctx, tx, runID, "external_checked", map[string]any{"step_id": stepID, "attempt_id": id, "outcome": outcome, "decision": "waiting"}, now); err != nil {
		return err
	}
	return tx.Commit()
}

// DecideExternal records the validation decision and logical step transition in
// one transaction, guarded by both the latest attempt and current run control.
func (s *Store) DecideExternal(ctx context.Context, id string, result external.Result, outcome, decision string, output json.RawMessage, status StepStatus) (bool, error) {
	if status != StepSucceeded && status != StepRouted && status != StepFailed && status != StepNeedsHuman {
		return false, fmt.Errorf("invalid external decision status %s", status)
	}
	now := s.now().UTC()
	raw, err := json.Marshal(result)
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var runID, stepID string
	var stepRunID, attemptID int64
	var current string
	var stepStatus StepStatus
	var runStatus RunStatus
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT e.run_id,e.step_id,e.step_run_id,e.attempt_id,e.status,s.status,r.status,e.attempt_id=(SELECT MAX(id) FROM attempts WHERE step_run_id=e.step_run_id) FROM external_attempts e JOIN step_runs s ON s.id=e.step_run_id JOIN workflow_runs r ON r.id=e.run_id WHERE e.id=?`, id).Scan(&runID, &stepID, &stepRunID, &attemptID, &current, &stepStatus, &runStatus, &active); err != nil {
		return false, err
	}
	if current == "decided" || !active || stepStatus != StepWaitingExternal || (runStatus != RunRunning && runStatus != RunWaitingExternal) {
		return false, nil
	}
	if err = updateExternalReferences(ctx, tx, id, result.References); err != nil {
		return false, err
	}
	if err = recordExternalUsage(ctx, tx, id, result); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE external_attempts SET status='decided',result_json=?,outcome=?,decision=?,last_checked_at=?,updated_at=? WHERE id=?`, raw, outcome, decision, stamp(now), stamp(now), id); err != nil {
		return false, err
	}
	attemptStatus := AttemptFailed
	class := activity.ErrorPermanent
	message := outcome
	if status == StepSucceeded {
		attemptStatus = AttemptSucceeded
		class = ""
		message = ""
	}
	if status == StepNeedsHuman {
		class = activity.ErrorNeedsHuman
	}
	if _, err = tx.ExecContext(ctx, `UPDATE attempts SET status=?,error_class=?,error=?,output_json=?,finished_at=? WHERE id=?`, attemptStatus, class, message, []byte(output), stamp(now), attemptID); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE step_runs SET status=?,error_class=?,error=?,output_json=?,updated_at=? WHERE id=?`, status, class, message, []byte(output), stamp(now), stepRunID); err != nil {
		return false, err
	}
	if status == StepNeedsHuman {
		if _, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET status=?,error=?,updated_at=? WHERE id=?`, RunNeedsHuman, outcome, stamp(now), runID); err != nil {
			return false, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE external_events SET disposition='processed',processed_at=? WHERE attempt_id=? AND disposition='pending'`, stamp(now), id); err != nil {
		return false, err
	}
	if err = insertEvent(ctx, tx, runID, "external_decided", map[string]any{"step_id": stepID, "step_run_id": stepRunID, "attempt_id": id, "outcome": outcome, "decision": decision, "status": status}, now); err != nil {
		return false, err
	}
	if status == StepSucceeded {
		if err = insertEvent(ctx, tx, runID, "step_completed", map[string]any{"step_id": stepID, "attempt_id": id, "external": true}, now); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) SaveExternalCancellation(ctx context.Context, id string, cancellation external.Cancellation) error {
	now := s.now().UTC()
	raw, err := json.Marshal(cancellation)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var runID, stepID string
	if err = tx.QueryRowContext(ctx, `SELECT run_id,step_id FROM external_attempts WHERE id=?`, id).Scan(&runID, &stepID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE external_attempts SET cancellation_json=?,updated_at=? WHERE id=?`, raw, stamp(now), id); err != nil {
		return err
	}
	if err = insertEvent(ctx, tx, runID, "external_cancellation_requested", map[string]any{"step_id": stepID, "attempt_id": id, "cancellation": cancellation}, now); err != nil {
		return err
	}
	return tx.Commit()
}

// RetryExternal preserves the prior assignment and explicitly readies a new
// attempt. It cannot revive a successful or cancelled workflow.
func (s *Store) RetryExternal(ctx context.Context, runID, stepID, attemptID string) error {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var stepRunID, dbAttemptID int64
	var status RunStatus
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT e.step_run_id,e.attempt_id,r.status,e.attempt_id=(SELECT MAX(id) FROM attempts WHERE step_run_id=e.step_run_id) FROM external_attempts e JOIN workflow_runs r ON r.id=e.run_id WHERE e.id=? AND e.run_id=? AND e.step_id=?`, attemptID, runID, stepID).Scan(&stepRunID, &dbAttemptID, &status, &active); err != nil {
		return err
	}
	if !active || status == RunSucceeded || status == RunCancelled {
		return ErrExternalInactive
	}
	var stepStatus StepStatus
	if err = tx.QueryRowContext(ctx, `SELECT status FROM step_runs WHERE id=?`, stepRunID).Scan(&stepStatus); err != nil {
		return err
	}
	if stepStatus == StepSucceeded || stepStatus == StepRouted || stepStatus == StepSkipped || stepStatus == StepReady {
		return ErrExternalInactive
	}
	if _, err = tx.ExecContext(ctx, `UPDATE external_attempts SET status='decided',outcome=CASE WHEN outcome='' THEN 'retry_requested' ELSE outcome END,decision=CASE WHEN decision='' THEN 'retry' ELSE decision END,updated_at=? WHERE id=?`, stamp(now), attemptID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE attempts SET status=?,error='retry requested',finished_at=? WHERE id=? AND status NOT IN (?,?)`, AttemptFailed, stamp(now), dbAttemptID, AttemptSucceeded, AttemptFailed); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE step_runs SET status=?,error_class='',error='',output_json=NULL,updated_at=? WHERE id=?`, StepReady, stamp(now), stepRunID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET status=CASE WHEN status=? THEN ? ELSE ? END,error='',finished_at=NULL,updated_at=? WHERE id=?`, RunPaused, RunPaused, RunRunning, stamp(now), runID); err != nil {
		return err
	}
	if err = insertEvent(ctx, tx, runID, "external_retry_requested", map[string]any{"step_id": stepID, "attempt_id": attemptID}, now); err != nil {
		return err
	}
	return tx.Commit()
}

// Inspection can discover the execution run after assignment. The issue remains
// pinned; an external result cannot silently move an attempt to another issue.
func updateExternalReferences(ctx context.Context, tx *sql.Tx, id string, refs external.References) error {
	if refs.IssueID == "" && refs.RunID == "" {
		return nil
	}
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT references_json FROM external_attempts WHERE id=?`, id).Scan(&raw); err != nil {
		return err
	}
	var existing external.References
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &existing); err != nil {
			return err
		}
	}
	if refs.IssueID != "" && existing.IssueID != "" && refs.IssueID != existing.IssueID {
		return fmt.Errorf("external inspection issue differs from pinned assignment")
	}
	if existing.IssueID == "" {
		existing.IssueID = refs.IssueID
	}
	if refs.RunID != "" {
		existing.RunID = refs.RunID
	}
	encoded, err := json.Marshal(existing)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE external_attempts SET references_json=? WHERE id=?`, encoded, id); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE attempts SET receipt=? WHERE id=(SELECT attempt_id FROM external_attempts WHERE id=?)`, string(encoded), id)
	return err
}
