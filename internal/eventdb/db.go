// Package eventdb maintains a sqlite read-model of the JSONL event log
// (.orquestalite/run.log plus rotated run-*.log.gz archives) at
// .orquestalite/orq.db. It backs the dashboard query API and the
// `orq-lite index` command. Pure Go (modernc.org/sqlite) so release
// builds keep CGO_ENABLED=0.
package eventdb

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

const schemaVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS runs (
    run_id      TEXT PRIMARY KEY,
    command     TEXT NOT NULL DEFAULT '',
    args        TEXT NOT NULL DEFAULT '[]',
    status      TEXT NOT NULL DEFAULT 'running',
    started_at  TEXT NOT NULL DEFAULT '',
    finished_at TEXT,
    duration_s  REAL,
    orq_version TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS agent_runs (
    id                  INTEGER PRIMARY KEY,
    ts                  TEXT NOT NULL DEFAULT '',
    run_id              TEXT NOT NULL DEFAULT '',
    role                TEXT NOT NULL DEFAULT '',
    agent               TEXT NOT NULL DEFAULT '',
    task_id             TEXT NOT NULL DEFAULT '',
    cycle               INT  NOT NULL DEFAULT 0,
    attempt             INT  NOT NULL DEFAULT 0,
    provider            TEXT NOT NULL DEFAULT '',
    model               TEXT NOT NULL DEFAULT '',
    duration_s          REAL NOT NULL DEFAULT 0,
    exit_code           INT  NOT NULL DEFAULT 0,
    timed_out           INT  NOT NULL DEFAULT 0,
    rate_limited        INT  NOT NULL DEFAULT 0,
    input_tokens        INT  NOT NULL DEFAULT 0,
    output_tokens       INT  NOT NULL DEFAULT 0,
    cached_input_tokens INT  NOT NULL DEFAULT 0,
    reasoning_tokens    INT  NOT NULL DEFAULT 0,
    artifacts_dir       TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS events (
    id      INTEGER PRIMARY KEY,
    run_id  TEXT NOT NULL DEFAULT '',
    ts      TEXT NOT NULL DEFAULT '',
    type    TEXT NOT NULL DEFAULT '',
    task_id TEXT NOT NULL DEFAULT '',
    raw     TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ingest_state (
    file   TEXT PRIMARY KEY,
    offset INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_run      ON events(run_id);
CREATE INDEX IF NOT EXISTS idx_events_type     ON events(type);
CREATE INDEX IF NOT EXISTS idx_agent_runs_run  ON agent_runs(run_id);
CREATE INDEX IF NOT EXISTS idx_runs_started    ON runs(started_at);
`

// DB is a handle on the read-model. Safe for concurrent use.
type DB struct {
	sql *sql.DB
}

// Open opens (creating if absent) the read-model at path in WAL mode and
// stamps/verifies PRAGMA user_version.
func Open(path string) (*DB, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("eventdb: open %s: %w", path, err)
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("eventdb: user_version %s: %w", path, err)
	}
	switch version {
	case 0:
		if _, err := db.Exec(schema); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("eventdb: create schema %s: %w", path, err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("eventdb: stamp version %s: %w", path, err)
		}
	case schemaVersion:
		// current
	default:
		_ = db.Close()
		return nil, fmt.Errorf("eventdb: %s has schema version %d, this binary supports %d", path, version, schemaVersion)
	}
	return &DB{sql: db}, nil
}

func (d *DB) Close() error { return d.sql.Close() }

// Counts reports row totals, used by `orq-lite index` output.
func (d *DB) Counts() (runs, agentRuns, events int, err error) {
	if err = d.sql.QueryRow("SELECT COUNT(*) FROM runs").Scan(&runs); err != nil {
		return
	}
	if err = d.sql.QueryRow("SELECT COUNT(*) FROM agent_runs").Scan(&agentRuns); err != nil {
		return
	}
	err = d.sql.QueryRow("SELECT COUNT(*) FROM events").Scan(&events)
	return
}
