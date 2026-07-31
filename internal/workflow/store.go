package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 3

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, fmt.Errorf("workflow: open %s: %w", path, err)
	}
	// The runtime is a single local process. One SQLite writer connection keeps
	// state transitions deterministic while activities themselves may execute
	// concurrently outside the database.
	db.SetMaxOpenConns(1)
	store := &Store{db: db, now: time.Now}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > schemaVersion {
		return fmt.Errorf("workflow: DB schema %d is newer than supported %d", version, schemaVersion)
	}
	if version == 0 {
		if _, err := s.db.Exec(schemaV1); err != nil {
			return fmt.Errorf("workflow: create schema: %w", err)
		}
		if _, err := s.db.Exec("PRAGMA user_version=1"); err != nil {
			return err
		}
		version = 1
	}
	if version == 1 {
		if _, err := s.db.Exec(schemaV2); err != nil {
			return fmt.Errorf("workflow: migrate schema 1 -> 2: %w", err)
		}
		if _, err := s.db.Exec("PRAGMA user_version=2"); err != nil {
			return err
		}
		version = 2
	}
	if version == 2 {
		if _, err := s.db.Exec(schemaV3); err != nil {
			return fmt.Errorf("workflow: migrate schema 2 -> 3: %w", err)
		}
		if _, err := s.db.Exec("PRAGMA user_version=3"); err != nil {
			return err
		}
	}
	return nil
}

const schemaV1 = `
CREATE TABLE workflow_runs (
 id TEXT PRIMARY KEY, flow_ref TEXT NOT NULL, definition_hash TEXT NOT NULL,
 ir_json BLOB NOT NULL, inputs_json BLOB NOT NULL, policy_json BLOB NOT NULL,
 status TEXT NOT NULL, error TEXT NOT NULL DEFAULT '', started_at TEXT NOT NULL,
 updated_at TEXT NOT NULL, finished_at TEXT
);
CREATE INDEX workflow_runs_status ON workflow_runs(status);
CREATE TABLE step_runs (
 id INTEGER PRIMARY KEY, run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
 scope_path TEXT NOT NULL, step_id TEXT NOT NULL, foreach_key TEXT NOT NULL DEFAULT '',
 activity_ref TEXT NOT NULL DEFAULT '', inputs_json BLOB, output_json BLOB,
 status TEXT NOT NULL, error_class TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 UNIQUE(run_id, scope_path, step_id, foreach_key)
);
CREATE INDEX step_runs_run_status ON step_runs(run_id,status);
CREATE TABLE attempts (
 id INTEGER PRIMARY KEY, step_run_id INTEGER NOT NULL REFERENCES step_runs(id) ON DELETE CASCADE,
 number INTEGER NOT NULL, activity_ref TEXT NOT NULL, idempotency_key TEXT NOT NULL,
 status TEXT NOT NULL, error_class TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '',
 receipt TEXT NOT NULL DEFAULT '', output_json BLOB, started_at TEXT NOT NULL, finished_at TEXT,
 UNIQUE(step_run_id,number)
);
CREATE TABLE artifacts (
 id INTEGER PRIMARY KEY, run_id TEXT NOT NULL, step_run_id INTEGER NOT NULL,
 kind TEXT NOT NULL, uri TEXT NOT NULL, digest TEXT NOT NULL DEFAULT '', metadata_json BLOB,
 UNIQUE(run_id,step_run_id,uri)
);
CREATE TABLE outbox_events (
 event_id TEXT PRIMARY KEY, run_id TEXT NOT NULL, payload_json BLOB NOT NULL,
 created_at TEXT NOT NULL, delivered_at TEXT
);
CREATE INDEX outbox_pending ON outbox_events(delivered_at,created_at);
CREATE TABLE approvals (
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL, step_run_id INTEGER NOT NULL,
 reason TEXT NOT NULL, status TEXT NOT NULL, decision TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL, decided_at TEXT,
 UNIQUE(run_id,step_run_id)
);`

const schemaV2 = `
ALTER TABLE workflow_runs ADD COLUMN source_key TEXT;
CREATE UNIQUE INDEX workflow_runs_source_key ON workflow_runs(source_key) WHERE source_key IS NOT NULL;
`

const schemaV3 = `ALTER TABLE attempts ADD COLUMN cost_usd REAL NOT NULL DEFAULT 0;`

type CreateRunParams struct {
	ID, SourceKey, FlowRef, DefinitionHash string
	IR, Inputs, Policy                     json.RawMessage
}

func (s *Store) CreateRun(ctx context.Context, params CreateRunParams) (*Run, error) {
	run, _, err := s.CreateRunOnce(ctx, params)
	return run, err
}

// CreateRunOnce atomically claims a source key. Repeated delivery of the same
// external trigger returns its original run without creating a second run.
func (s *Store) CreateRunOnce(ctx context.Context, params CreateRunParams) (*Run, bool, error) {
	if params.SourceKey != "" {
		if existing, err := s.GetRunBySourceKey(ctx, params.SourceKey); err == nil {
			return existing, false, nil
		} else if err != sql.ErrNoRows {
			return nil, false, err
		}
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var sourceKey any
	if params.SourceKey != "" {
		sourceKey = params.SourceKey
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_runs(id,source_key,flow_ref,definition_hash,ir_json,inputs_json,policy_json,status,started_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, params.ID, sourceKey, params.FlowRef, params.DefinitionHash, []byte(params.IR), []byte(params.Inputs), []byte(params.Policy), RunRunning, stamp(now), stamp(now))
	if err != nil {
		_ = tx.Rollback()
		if params.SourceKey != "" {
			if existing, lookupErr := s.GetRunBySourceKey(ctx, params.SourceKey); lookupErr == nil {
				return existing, false, nil
			}
		}
		return nil, false, err
	}
	if err := insertEvent(ctx, tx, params.ID, "workflow_started", map[string]any{"flow_ref": params.FlowRef, "definition_hash": params.DefinitionHash, "source_key": params.SourceKey}, now); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	run, err := s.GetRun(ctx, params.ID)
	return run, true, err
}

func (s *Store) GetRun(ctx context.Context, id string) (*Run, error) {
	row := s.db.QueryRowContext(ctx, runSelect+` WHERE id=?`, id)
	return scanRun(row)
}

func (s *Store) GetRunBySourceKey(ctx context.Context, sourceKey string) (*Run, error) {
	row := s.db.QueryRowContext(ctx, runSelect+` WHERE source_key=?`, sourceKey)
	return scanRun(row)
}

const runSelect = `SELECT id,COALESCE(source_key,''),flow_ref,definition_hash,ir_json,inputs_json,policy_json,status,error,started_at,updated_at,finished_at FROM workflow_runs`

type rowScanner interface {
	Scan(...any) error
}

func scanRun(row rowScanner) (*Run, error) {
	var run Run
	var started, updated string
	var finished sql.NullString
	if err := row.Scan(&run.ID, &run.SourceKey, &run.FlowRef, &run.DefinitionHash, &run.IR, &run.Inputs, &run.Policy, &run.Status, &run.Error, &started, &updated, &finished); err != nil {
		return nil, err
	}
	run.StartedAt = parseStamp(started)
	run.UpdatedAt = parseStamp(updated)
	if finished.Valid {
		value := parseStamp(finished.String)
		run.FinishedAt = &value
	}
	return &run, nil
}

// LatestUnfinishedRun returns the most recent run of flowRef that has not
// reached a terminal status, or sql.ErrNoRows when there is none. It exists so
// `flow run` can point an operator at the resume that already exists instead
// of silently starting a second run of work already in flight.
func (s *Store) LatestUnfinishedRun(ctx context.Context, flowRef string) (*Run, error) {
	row := s.db.QueryRowContext(ctx, runSelect+` WHERE flow_ref=? AND status IN (?,?,?) ORDER BY started_at DESC, id DESC LIMIT 1`, flowRef, RunPending, RunRunning, RunNeedsHuman)
	return scanRun(row)
}

func (s *Store) ListRuns(ctx context.Context, limit int) ([]Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, runSelect+` ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []Run
	for rows.Next() {
		var run Run
		var started, updated string
		var finished sql.NullString
		if err := rows.Scan(&run.ID, &run.SourceKey, &run.FlowRef, &run.DefinitionHash, &run.IR, &run.Inputs, &run.Policy, &run.Status, &run.Error, &started, &updated, &finished); err != nil {
			return nil, err
		}
		run.StartedAt = parseStamp(started)
		run.UpdatedAt = parseStamp(updated)
		if finished.Valid {
			v := parseStamp(finished.String)
			run.FinishedAt = &v
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *Store) ListSteps(ctx context.Context, runID string) ([]StepRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,run_id,scope_path,step_id,foreach_key,activity_ref,inputs_json,output_json,status,error_class,error,created_at,updated_at FROM step_runs WHERE run_id=? ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var steps []StepRun
	for rows.Next() {
		var step StepRun
		var inputs, output []byte
		var created, updated string
		if err := rows.Scan(&step.ID, &step.RunID, &step.ScopePath, &step.StepID, &step.ForeachKey, &step.ActivityRef, &inputs, &output, &step.Status, &step.ErrorClass, &step.Error, &created, &updated); err != nil {
			return nil, err
		}
		step.Inputs = json.RawMessage(inputs)
		step.Output = json.RawMessage(output)
		step.CreatedAt = parseStamp(created)
		step.UpdatedAt = parseStamp(updated)
		steps = append(steps, step)
	}
	return steps, rows.Err()
}

func (s *Store) ListApprovals(ctx context.Context, runID string) ([]Approval, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,run_id,step_run_id,reason,status,decision,created_at,decided_at FROM approvals WHERE run_id=? ORDER BY created_at`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var approvals []Approval
	for rows.Next() {
		var a Approval
		var created string
		var decided sql.NullString
		if err := rows.Scan(&a.ID, &a.RunID, &a.StepRunID, &a.Reason, &a.Status, &a.Decision, &created, &decided); err != nil {
			return nil, err
		}
		a.CreatedAt = parseStamp(created)
		if decided.Valid {
			v := parseStamp(decided.String)
			a.DecidedAt = &v
		}
		approvals = append(approvals, a)
	}
	return approvals, rows.Err()
}

type Usage struct {
	Attempts      int
	AgentAttempts int
	CostUSD       float64
}

func (s *Store) RunUsage(ctx context.Context, runID string) (Usage, error) {
	var usage Usage
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN a.activity_ref LIKE 'agent.invoke@%' THEN 1 ELSE 0 END),0), COALESCE(SUM(a.cost_usd),0) FROM attempts a JOIN step_runs s ON s.id=a.step_run_id WHERE s.run_id=?`, runID).Scan(&usage.Attempts, &usage.AgentAttempts, &usage.CostUSD)
	return usage, err
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
func parseStamp(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
