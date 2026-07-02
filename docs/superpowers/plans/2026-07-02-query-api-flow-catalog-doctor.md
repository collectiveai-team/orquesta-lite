# Query API + Flow Catalog + Doctor Endpoint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend `orq-lite serve` with a sqlite read-model of the event log (`GET /api/runs`, `/api/runs/{id}`, `/api/runs/{id}/events`, `/api/agent-runs`, `/api/stats/cost`), a flow catalog (`GET /api/flows`), and a doctor endpoint (`GET /api/doctor`), plus an `orq-lite index` command.

**Architecture:** New `internal/eventdb` package (pure-Go sqlite via `modernc.org/sqlite`) ingests `.orquestalite/run.log` + rotated `run-*.log.gz` archives into `.orquestalite/orq.db` with resumable per-file offsets. The web server opens the db lazily and ingests on demand with a 1s debounce (plus a 1s ticker in `Serve` so the db stays warm without traffic — the same freshness the SSE `logTail` gives). Doctor checks move from `internal/commands/doctorcmd.go` into a new `internal/doctor` package shared verbatim by the CLI and the endpoint. Flow catalog reuses `engine.LoadFlows` and a new `Flow.ReferencedRoles()` method (moved from `flowcmd.go`).

**Tech Stack:** Go 1.24.4, stdlib `net/http` (Go 1.22 mux patterns), stdlib `flag` CLI, `modernc.org/sqlite` (the repo's first external dependency).

## Global Constraints

- The response shapes in `features.md` are a CONTRACT (mirrored in the orquesta repo's `docs/orq-lite-query-api.md`) — field names verbatim, no additions or renames.
- Every new endpoint is `GET`, returns JSON, and sets `Cache-Control: no-store` (serve stays strictly read-only).
- `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...` must stay green — `modernc.org/sqlite` only, never `mattn/go-sqlite3`.
- DB at `.orquestalite/orq.db`, WAL mode, `PRAGMA user_version=1`.
- Pagination: default `limit=50`, max 500; default `offset=0`; unparseable/out-of-range values fall back to defaults, never an error. Unknown query params ignored.
- A second ingest of the same file must not duplicate rows; log rotation (truncate + `.log.gz`) must not lose events.
- Doctor endpoint and `orq-lite doctor` CLI must never disagree → both call the same `doctor.Run`.
- Module: `github.com/lionelchamorro/orquestalite`. Verify with `go test ./...` and `go vet ./...` after each task.

## Codebase facts the implementer needs

- Event envelope (from `internal/eventlog/eventlog.go:87-112`): every JSONL line has `"ts"` (RFC3339Nano UTC), `"event"` (type string), and `"run_id"`. `run_start` adds `command`, `args` ([]string or absent), `orq_version`. `run_end` adds `status` (`ok|error|interrupted`), `duration_s` (int seconds), `orq_version`. `agent_run` adds `role`, `agent`, `task_id`, `cycle`, `attempt`, `provider`, `model`, `duration_s` (int), `exit_code`, `timed_out`, `rate_limited`, and conditionally (only when > 0 / non-empty) `input_tokens`, `output_tokens`, `cached_input_tokens`, `reasoning_tokens`, `artifacts_dir`. `input_tokens` already includes cached tokens.
- Rotation (`internal/eventlog/eventlog.go:114-135`): at 50 MiB, `run.log`'s full contents are gzip-copied to `run-<YYYYMMDDTHHMMSSZ>.log.gz` in the same directory, then `run.log` is truncated to 0 in place. Archive names sort lexically by rotation time.
- `run_id` format: `r20260702T193000Z-4f2a` (lexically sortable).
- Cost pricing lives in `internal/cost/prices.go`: unexported `estimateUSD(model string, inputTokens, outputTokens int) (float64, bool)` — exact match then prefix match, `(0, false)` for unknown models.
- Web server: `internal/web/server.go`, struct `Server{Dir string; costMu sync.Mutex; costCached []byte; costFetched time.Time}`. Routes registered in `Handler()` with Go 1.22 patterns (`mux.HandleFunc("GET /api/cost", s.handleCost)`). `statePath(name)` → `filepath.Join(s.Dir, ".orquestalite", name)`. `Serve(ctx, addr, dir)` at line ~441. Tests use `httptest.NewRecorder` + `srv.Handler().ServeHTTP` and a `stateDir(t)` helper that makes a `t.TempDir()` containing `.orquestalite/`.
- Flow engine: `internal/engine/engine.go` — `LoadFlows(path) (*Flows, error)`, `Flows{Flows map[string]Flow}`, `Flow{Description string; Inputs map[string]InputSpec; Steps []Step}`, `InputSpec{Type string; Default any}`, `Step{Type, Agent string; ...; Body []Step}`. `LoadFlows` wraps `os.ReadFile` errors with `%w`, so `errors.Is(err, os.ErrNotExist)` works. `internal/commands/flowcmd.go:87-106` has `flowReferencedRoles(flow *engine.Flow) []string` (to be moved onto `Flow`), called at one site inside `RunFlow`.
- Team config: `internal/config/config.go` — `Load(path) (*Config, error)` (validates), `Config.Roles map[string]Role`, `Role.Prompt` is a project-relative path.
- Doctor: all of `internal/commands/doctorcmd.go` (full source reproduced in Task 12) — statuses `PASS/WARN/FAIL`, `providerHasUsableCredentials` is also called from `internal/commands/runcmd.go:432` and tested in `internal/commands/doctorcmd_test.go`.
- CLI: `cmd/orq-lite/main.go` — stdlib `flag`, manual `switch cmd`. `case "doctor":` at line ~166, `case "serve":` at ~182, `usage()` at ~295.

---

### Task 1: eventdb package — schema and Open

**Files:**
- Create: `internal/eventdb/db.go`
- Test: `internal/eventdb/db_test.go`
- Modify: `go.mod` (add `modernc.org/sqlite`)

**Interfaces:**
- Produces: `eventdb.Open(path string) (*DB, error)`, `(*DB).Close() error`, `(*DB).Counts() (runs, agentRuns, events int, err error)`. `DB` has unexported `sql *sql.DB` used by later tasks in-package.

- [ ] **Step 1: Branch and add the dependency**

```bash
git checkout -b feature/query-api-flow-catalog-doctor
go get modernc.org/sqlite@latest
go mod tidy
```

- [ ] **Step 2: Write the failing test**

`internal/eventdb/db_test.go`:

```go
package eventdb

import (
	"path/filepath"
	"testing"
)

func TestOpen_CreatesSchemaAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orq.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path) // reopen: schema already present, must not error
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var version int
	if err := db.sql.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("user_version = %d, want 1", version)
	}
	for _, table := range []string{"runs", "agent_runs", "events", "ingest_state"} {
		var n int
		if err := db.sql.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
	}
	var mode string
	if err := db.sql.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}

func TestOpen_RejectsUnknownSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orq.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("want error for unknown schema version")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/eventdb/ -v`
Expected: FAIL (package does not compile: `Open` undefined)

- [ ] **Step 4: Write the implementation**

`internal/eventdb/db.go`:

```go
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
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/eventdb/ -v`
Expected: PASS (both tests)

- [ ] **Step 6: Verify the cross-compile constraint immediately (new dependency)**

Run: `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...`
Expected: exits 0, no output

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/eventdb/
git commit -m "feat(eventdb): sqlite read-model schema via modernc.org/sqlite"
```

---

### Task 2: eventdb ingestion — resumable tail of run.log

**Files:**
- Create: `internal/eventdb/ingest.go`
- Test: `internal/eventdb/ingest_test.go`

**Interfaces:**
- Consumes: `DB.sql` from Task 1.
- Produces: `(*DB).Ingest(stateDir string) error` — stateDir is the `.orquestalite` directory. Idempotent; resumes from `ingest_state` offsets; a trailing partial line is left un-consumed until its newline arrives.

- [ ] **Step 1: Write the failing tests**

`internal/eventdb/ingest_test.go`:

```go
package eventdb

import (
	"os"
	"path/filepath"
	"testing"
)

// Realistic lines matching internal/eventlog's envelope (ts, event, run_id).
const (
	lineRunStart = `{"ts":"2026-07-01T10:00:00.000000001Z","event":"run_start","run_id":"r1","command":"run","args":["--feature","F1"],"orq_version":"v0.2.0"}`
	lineAgentRun = `{"ts":"2026-07-01T10:00:05Z","event":"agent_run","run_id":"r1","role":"coder","agent":"sonnet","task_id":"T1","cycle":1,"attempt":1,"provider":"claude","model":"claude-sonnet-4-6","duration_s":42,"exit_code":0,"timed_out":false,"rate_limited":false,"input_tokens":1000,"output_tokens":500,"cached_input_tokens":200,"artifacts_dir":".orquestalite/runs/r1/agents/T1/coder.c1.a1"}`
	lineTaskDone = `{"ts":"2026-07-01T10:00:06Z","event":"task_done","run_id":"r1","task_id":"T1","commit_sha":"abc123"}`
	lineRunEnd   = `{"ts":"2026-07-01T10:00:07Z","event":"run_end","run_id":"r1","status":"ok","duration_s":7,"orq_version":"v0.2.0"}`
)

func newTestDB(t *testing.T) (*DB, string) {
	t.Helper()
	dir := t.TempDir() // stands in for .orquestalite
	db, err := Open(filepath.Join(dir, "orq.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, dir
}

func appendLog(t *testing.T, dir, content string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, "run.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, db *DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.sql.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIngest_ProjectsRunsAgentRunsAndEvents(t *testing.T) {
	db, dir := newTestDB(t)
	appendLog(t, dir, lineRunStart+"\n"+lineAgentRun+"\n"+lineTaskDone+"\n"+lineRunEnd+"\n")

	if err := db.Ingest(dir); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM events"); n != 4 {
		t.Fatalf("events = %d, want 4", n)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM agent_runs"); n != 1 {
		t.Fatalf("agent_runs = %d, want 1", n)
	}
	var status, finished string
	var dur float64
	err := db.sql.QueryRow("SELECT status, finished_at, duration_s FROM runs WHERE run_id = 'r1'").
		Scan(&status, &finished, &dur)
	if err != nil {
		t.Fatal(err)
	}
	if status != "ok" || finished != "2026-07-01T10:00:07Z" || dur != 7 {
		t.Fatalf("run row = %q %q %v", status, finished, dur)
	}
}

func TestIngest_SecondIngestDoesNotDuplicate(t *testing.T) {
	db, dir := newTestDB(t)
	appendLog(t, dir, lineRunStart+"\n"+lineAgentRun+"\n")
	if err := db.Ingest(dir); err != nil {
		t.Fatal(err)
	}
	if err := db.Ingest(dir); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM events"); n != 2 {
		t.Fatalf("events after re-ingest = %d, want 2", n)
	}
	// New lines appended after the first ingest are picked up.
	appendLog(t, dir, lineRunEnd+"\n")
	if err := db.Ingest(dir); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM events"); n != 3 {
		t.Fatalf("events after append = %d, want 3", n)
	}
}

func TestIngest_RunWithoutEndIsRunning(t *testing.T) {
	db, dir := newTestDB(t)
	appendLog(t, dir, lineRunStart+"\n")
	if err := db.Ingest(dir); err != nil {
		t.Fatal(err)
	}
	var status string
	var finished *string
	if err := db.sql.QueryRow("SELECT status, finished_at FROM runs WHERE run_id = 'r1'").Scan(&status, &finished); err != nil {
		t.Fatal(err)
	}
	if status != "running" || finished != nil {
		t.Fatalf("status = %q finished = %v, want running/NULL", status, finished)
	}
}

func TestIngest_PartialLineWaitsForNewline(t *testing.T) {
	db, dir := newTestDB(t)
	appendLog(t, dir, lineRunStart+"\n"+`{"ts":"2026-07-01T10:00:08Z","ev`) // mid-write
	if err := db.Ingest(dir); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM events"); n != 1 {
		t.Fatalf("events = %d, want 1 (partial line must wait)", n)
	}
	appendLog(t, dir, `ent":"task_start","run_id":"r1","task_id":"T2"}`+"\n")
	if err := db.Ingest(dir); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM events WHERE type = 'task_start'"); n != 1 {
		t.Fatalf("task_start events = %d, want 1 (completed line ingests whole)", n)
	}
}

func TestIngest_SkipsCorruptLinesAndMissingLog(t *testing.T) {
	db, dir := newTestDB(t)
	if err := db.Ingest(dir); err != nil { // no run.log at all
		t.Fatal(err)
	}
	appendLog(t, dir, "not json at all\n"+lineRunStart+"\n")
	if err := db.Ingest(dir); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM events"); n != 1 {
		t.Fatalf("events = %d, want 1 (corrupt line skipped)", n)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/eventdb/ -v -run TestIngest`
Expected: FAIL (compile error: `Ingest` undefined)

- [ ] **Step 3: Write the implementation**

`internal/eventdb/ingest.go`:

```go
package eventdb

import (
	"bufio"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const liveLogName = "run.log"

// Ingest tails the event log inside stateDir (the .orquestalite directory)
// into the read-model, resuming from per-file byte offsets in ingest_state.
// Rotation (eventlog copies the whole run.log into run-<ts>.log.gz, then
// truncates run.log in place) is handled by treating the oldest not-yet-seen
// archive as the continuation of the live file: it is ingested starting at
// the live offset, later unseen archives from 0, and the live offset resets.
// A trailing line without its newline is left for a later call.
func (d *DB) Ingest(stateDir string) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	liveOff, err := getOffset(tx, liveLogName)
	if err != nil {
		return err
	}
	if liveOff < 0 {
		liveOff = 0
	}

	gzs, err := filepath.Glob(filepath.Join(stateDir, "run-*.log.gz"))
	if err != nil {
		return err
	}
	sort.Strings(gzs) // rotation timestamps sort lexically
	firstUnseen := true
	for _, gz := range gzs {
		name := filepath.Base(gz)
		off, err := getOffset(tx, name)
		if err != nil {
			return err
		}
		if off >= 0 {
			continue // archive fully ingested on a prior call
		}
		start := int64(0)
		if firstUnseen {
			start = liveOff // skip the prefix already ingested from run.log
		}
		firstUnseen = false
		n, err := ingestGz(tx, gz, start)
		if err != nil {
			return err
		}
		if err := setOffset(tx, name, n); err != nil {
			return err
		}
		liveOff = 0 // run.log was truncated at rotation
	}

	livePath := filepath.Join(stateDir, liveLogName)
	info, err := os.Stat(livePath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// nothing to tail yet
	case err != nil:
		return err
	default:
		if info.Size() < liveOff {
			liveOff = 0 // external truncation without an archive
		}
		n, err := ingestPlain(tx, livePath, liveOff)
		if err != nil {
			return err
		}
		if err := setOffset(tx, liveLogName, n); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// getOffset returns -1 when the file has no ingest_state row yet.
func getOffset(tx *sql.Tx, file string) (int64, error) {
	var off int64
	err := tx.QueryRow("SELECT offset FROM ingest_state WHERE file = ?", file).Scan(&off)
	if errors.Is(err, sql.ErrNoRows) {
		return -1, nil
	}
	return off, err
}

func setOffset(tx *sql.Tx, file string, off int64) error {
	_, err := tx.Exec(`INSERT INTO ingest_state(file, offset) VALUES(?, ?)
		ON CONFLICT(file) DO UPDATE SET offset = excluded.offset`, file, off)
	return err
}

func ingestPlain(tx *sql.Tx, path string, offset int64) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return offset, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	n, err := applyLines(tx, f)
	return offset + n, err
}

func ingestGz(tx *sql.Tx, path string, start int64) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	if start > 0 {
		if _, err := io.CopyN(io.Discard, zr, start); err != nil {
			if errors.Is(err, io.EOF) {
				return start, nil // archive shorter than the recorded offset
			}
			return 0, err
		}
	}
	n, err := applyLines(tx, zr)
	return start + n, err
}

// applyLines applies newline-terminated JSONL lines and returns the number
// of bytes consumed. A trailing partial line (no '\n') is not consumed, so
// a mid-write tail is retried complete on the next Ingest.
func applyLines(tx *sql.Tx, r io.Reader) (int64, error) {
	br := bufio.NewReaderSize(r, 256*1024)
	var consumed int64
	for {
		line, err := br.ReadString('\n')
		if err == nil {
			consumed += int64(len(line))
			if aerr := applyLine(tx, strings.TrimSpace(line)); aerr != nil {
				return consumed, aerr
			}
			continue
		}
		if errors.Is(err, io.EOF) {
			return consumed, nil
		}
		return consumed, err
	}
}

func applyLine(tx *sql.Tx, line string) error {
	if line == "" {
		return nil
	}
	var env struct {
		Ts     string `json:"ts"`
		Event  string `json:"event"`
		RunID  string `json:"run_id"`
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(line), &env); err != nil {
		return nil // corrupt line (torn write): skip rather than poison ingest
	}
	if _, err := tx.Exec(`INSERT INTO events(run_id, ts, type, task_id, raw) VALUES(?,?,?,?,?)`,
		env.RunID, env.Ts, env.Event, env.TaskID, line); err != nil {
		return err
	}

	switch env.Event {
	case "run_start":
		var e struct {
			Command    string   `json:"command"`
			Args       []string `json:"args"`
			OrqVersion string   `json:"orq_version"`
		}
		_ = json.Unmarshal([]byte(line), &e)
		args := []byte("[]")
		if e.Args != nil {
			args, _ = json.Marshal(e.Args)
		}
		_, err := tx.Exec(`INSERT INTO runs(run_id, command, args, status, started_at, orq_version)
			VALUES(?,?,?,'running',?,?)
			ON CONFLICT(run_id) DO UPDATE SET command = excluded.command, args = excluded.args,
				started_at = excluded.started_at, orq_version = excluded.orq_version`,
			env.RunID, e.Command, string(args), env.Ts, e.OrqVersion)
		return err
	case "run_end":
		var e struct {
			Status     string  `json:"status"`
			DurationS  float64 `json:"duration_s"`
			OrqVersion string  `json:"orq_version"`
		}
		_ = json.Unmarshal([]byte(line), &e)
		_, err := tx.Exec(`INSERT INTO runs(run_id, status, started_at, finished_at, duration_s, orq_version)
			VALUES(?,?,?,?,?,?)
			ON CONFLICT(run_id) DO UPDATE SET status = excluded.status,
				finished_at = excluded.finished_at, duration_s = excluded.duration_s`,
			env.RunID, e.Status, env.Ts, env.Ts, e.DurationS, e.OrqVersion)
		return err
	case "agent_run":
		var e struct {
			Role              string  `json:"role"`
			Agent             string  `json:"agent"`
			Cycle             int     `json:"cycle"`
			Attempt           int     `json:"attempt"`
			Provider          string  `json:"provider"`
			Model             string  `json:"model"`
			DurationS         float64 `json:"duration_s"`
			ExitCode          int     `json:"exit_code"`
			TimedOut          bool    `json:"timed_out"`
			RateLimited       bool    `json:"rate_limited"`
			InputTokens       int     `json:"input_tokens"`
			OutputTokens      int     `json:"output_tokens"`
			CachedInputTokens int     `json:"cached_input_tokens"`
			ReasoningTokens   int     `json:"reasoning_tokens"`
			ArtifactsDir      string  `json:"artifacts_dir"`
		}
		_ = json.Unmarshal([]byte(line), &e)
		_, err := tx.Exec(`INSERT INTO agent_runs(ts, run_id, role, agent, task_id, cycle, attempt,
				provider, model, duration_s, exit_code, timed_out, rate_limited,
				input_tokens, output_tokens, cached_input_tokens, reasoning_tokens, artifacts_dir)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			env.Ts, env.RunID, e.Role, e.Agent, env.TaskID, e.Cycle, e.Attempt,
			e.Provider, e.Model, e.DurationS, e.ExitCode, boolToInt(e.TimedOut), boolToInt(e.RateLimited),
			e.InputTokens, e.OutputTokens, e.CachedInputTokens, e.ReasoningTokens, e.ArtifactsDir)
		return err
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/eventdb/ -v`
Expected: PASS (all)

- [ ] **Step 5: Commit**

```bash
git add internal/eventdb/ingest.go internal/eventdb/ingest_test.go
git commit -m "feat(eventdb): resumable idempotent ingestion of run.log"
```

---

### Task 3: eventdb ingestion — rotation (.log.gz) without loss or duplication

**Files:**
- Modify: `internal/eventdb/ingest_test.go` (add tests only — the Task 2 implementation already contains the gz path; these tests prove it)

**Interfaces:**
- Consumes: `(*DB).Ingest` from Task 2.

- [ ] **Step 1: Write the tests**

Append to `internal/eventdb/ingest_test.go`:

```go
// rotate mimics eventlog.rotateLocked: gzip-copy the whole run.log into a
// timestamped archive, then truncate run.log to zero in place.
func rotate(t *testing.T, dir, archiveName string) {
	t.Helper()
	livePath := filepath.Join(dir, "run.log")
	raw, err := os.ReadFile(livePath)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, archiveName))
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(livePath, 0); err != nil {
		t.Fatal(err)
	}
}

func TestIngest_RotationLosesNothingDuplicatesNothing(t *testing.T) {
	db, dir := newTestDB(t)
	// 2 lines ingested live, then 2 more written, THEN rotation, then 1 new line.
	appendLog(t, dir, lineRunStart+"\n"+lineAgentRun+"\n")
	if err := db.Ingest(dir); err != nil {
		t.Fatal(err)
	}
	appendLog(t, dir, lineTaskDone+"\n"+lineRunEnd+"\n") // not yet ingested
	rotate(t, dir, "run-20260701T110000Z.log.gz")        // archive holds all 4
	appendLog(t, dir, `{"ts":"2026-07-01T11:00:01Z","event":"run_start","run_id":"r2","command":"run","orq_version":"v0.2.0"}`+"\n")

	if err := db.Ingest(dir); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM events"); n != 5 {
		t.Fatalf("events = %d, want 5 (2 live + 2 recovered from archive + 1 post-rotation)", n)
	}
	// Idempotent across the archive too.
	if err := db.Ingest(dir); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM events"); n != 5 {
		t.Fatalf("events after re-ingest = %d, want 5", n)
	}
}

func TestIngest_FreshDBIngestsExistingArchivesThenLive(t *testing.T) {
	db, dir := newTestDB(t)
	appendLog(t, dir, lineRunStart+"\n"+lineAgentRun+"\n")
	rotate(t, dir, "run-20260701T100500Z.log.gz")
	appendLog(t, dir, lineTaskDone+"\n"+lineRunEnd+"\n")

	if err := db.Ingest(dir); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM events"); n != 4 {
		t.Fatalf("events = %d, want 4", n)
	}
}

func TestIngest_MultipleRotationsWhileOffline(t *testing.T) {
	db, dir := newTestDB(t)
	appendLog(t, dir, lineRunStart+"\n")
	if err := db.Ingest(dir); err != nil {
		t.Fatal(err)
	}
	appendLog(t, dir, lineAgentRun+"\n")
	rotate(t, dir, "run-20260701T110000Z.log.gz") // holds lines 1-2
	appendLog(t, dir, lineTaskDone+"\n")
	rotate(t, dir, "run-20260701T120000Z.log.gz") // holds line 3
	appendLog(t, dir, lineRunEnd+"\n")

	if err := db.Ingest(dir); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM events"); n != 4 {
		t.Fatalf("events = %d, want 4 across two archives", n)
	}
}
```

Add `"compress/gzip"` to the test file's imports.

- [ ] **Step 2: Run tests**

Run: `go test ./internal/eventdb/ -v -run TestIngest`
Expected: PASS. If any rotation test fails, fix `Ingest` (the archive-continuation logic) — do not weaken the tests; the no-loss/no-duplicate behavior is a spec requirement.

- [ ] **Step 3: Commit**

```bash
git add internal/eventdb/ingest_test.go
git commit -m "test(eventdb): rotation recovery loses nothing, duplicates nothing"
```

---

### Task 4: eventdb queries + exported cost pricing

**Files:**
- Modify: `internal/cost/prices.go` (add exported wrapper at end of file)
- Create: `internal/eventdb/queries.go`
- Test: `internal/eventdb/queries_test.go`, `internal/cost/prices_export_test.go`

**Interfaces:**
- Consumes: tables from Task 1, `Ingest` from Task 2, unexported `estimateUSD` in `internal/cost/prices.go`.
- Produces:
  - `cost.EstimateUSD(model string, inputTokens, outputTokens int) (float64, bool)`
  - `eventdb.RunSummary`, `eventdb.AgentRunRecord`, `eventdb.CostRow` (JSON tags are the contract shapes)
  - `(*DB).Runs(f RunsFilter, limit, offset int) ([]RunSummary, int, error)` — newest-first
  - `(*DB).Run(id string) (*RunSummary, error)` — `(nil, nil)` when unknown
  - `(*DB).Events(runID, typ, taskID string, limit, offset int) ([]json.RawMessage, int, error)` — log order
  - `(*DB).AgentRuns(f AgentRunsFilter, limit, offset int) ([]AgentRunRecord, int, error)` — newest-first
  - `(*DB).CostStats(by string) ([]CostRow, error)` — by ∈ run|agent|task|role, cost_usd descending

- [ ] **Step 1: Export the pricing helper with a test**

`internal/cost/prices_export_test.go`:

```go
package cost

import "testing"

func TestEstimateUSD_ExportedWrapper(t *testing.T) {
	usd, ok := EstimateUSD("claude-sonnet-4-6", 1_000_000, 1_000_000)
	if !ok || usd != 18.00 { // 3.00 input + 15.00 output per million
		t.Fatalf("EstimateUSD = %v, %v; want 18.00, true", usd, ok)
	}
	if _, ok := EstimateUSD("unknown-model-xyz", 100, 100); ok {
		t.Fatal("unknown model must report ok=false")
	}
}
```

Run: `go test ./internal/cost/ -run TestEstimateUSD_Exported -v` → FAIL (undefined `EstimateUSD`).

Append to `internal/cost/prices.go`:

```go
// EstimateUSD prices first-party token counts against the same embedded
// table the cost report uses. Shared with the eventdb query API so
// GET /api/cost and the query endpoints can never disagree on pricing.
// ok is false when the model is unknown or both counts are zero.
func EstimateUSD(model string, inputTokens, outputTokens int) (float64, bool) {
	return estimateUSD(model, inputTokens, outputTokens)
}
```

Run: `go test ./internal/cost/ -v` → PASS.

- [ ] **Step 2: Write the failing query tests**

`internal/eventdb/queries_test.go`:

```go
package eventdb

import (
	"encoding/json"
	"testing"
)

// seedTwoRuns ingests a finished run r1 (one priced agent run, one task done)
// and a still-running run r2 (one agent run with an unknown model).
func seedTwoRuns(t *testing.T) *DB {
	t.Helper()
	db, dir := newTestDB(t)
	appendLog(t, dir,
		lineRunStart+"\n"+lineAgentRun+"\n"+lineTaskDone+"\n"+lineRunEnd+"\n"+
			`{"ts":"2026-07-01T11:00:00Z","event":"run_start","run_id":"r2","command":"factory","args":["features.md"],"orq_version":"v0.2.0"}`+"\n"+
			`{"ts":"2026-07-01T11:00:10Z","event":"agent_run","run_id":"r2","role":"critic","agent":"mystery","task_id":"T9","cycle":1,"attempt":1,"provider":"other","model":"mystery-model","duration_s":5,"exit_code":0,"timed_out":false,"rate_limited":false,"input_tokens":10,"output_tokens":10}`+"\n"+
			`{"ts":"2026-07-01T11:00:11Z","event":"task_failed","run_id":"r2","task_id":"T9","failure_reason":"max_iterations","attempts":3}`+"\n")
	if err := db.Ingest(dir); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRuns_NewestFirstWithAggregates(t *testing.T) {
	db := seedTwoRuns(t)
	runs, total, err := db.Runs(RunsFilter{}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(runs) != 2 {
		t.Fatalf("total=%d len=%d, want 2/2", total, len(runs))
	}
	if runs[0].RunID != "r2" || runs[1].RunID != "r1" {
		t.Fatalf("order = %s,%s; want r2,r1", runs[0].RunID, runs[1].RunID)
	}
	r1 := runs[1]
	if r1.Status != "ok" || r1.Command != "run" || len(r1.Args) != 2 {
		t.Fatalf("r1 = %+v", r1)
	}
	if r1.AgentRuns != 1 || r1.InputTokens != 1000 || r1.OutputTokens != 500 {
		t.Fatalf("r1 aggregates = %+v", r1)
	}
	// claude-sonnet-4-6: 1000/1M*3.00 + 500/1M*15.00 = 0.0105
	if r1.CostUSD < 0.0104 || r1.CostUSD > 0.0106 {
		t.Fatalf("r1 cost = %v, want ~0.0105", r1.CostUSD)
	}
	if r1.TasksDone != 1 || r1.TasksFailed != 0 {
		t.Fatalf("r1 tasks = %d/%d", r1.TasksDone, r1.TasksFailed)
	}
	r2 := runs[0]
	if r2.Status != "running" || r2.FinishedAt != nil || r2.DurationS != nil {
		t.Fatalf("r2 = %+v", r2)
	}
	if r2.CostUSD != 0 { // unknown model prices to zero, not an error
		t.Fatalf("r2 cost = %v, want 0", r2.CostUSD)
	}
	if r2.TasksFailed != 1 {
		t.Fatalf("r2 tasks_failed = %d, want 1", r2.TasksFailed)
	}
}

func TestRuns_ActiveFilterAndPagination(t *testing.T) {
	db := seedTwoRuns(t)
	active := true
	runs, total, err := db.Runs(RunsFilter{Active: &active}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(runs) != 1 || runs[0].RunID != "r2" {
		t.Fatalf("active filter: total=%d runs=%+v", total, runs)
	}
	runs, total, err = db.Runs(RunsFilter{}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(runs) != 1 || runs[0].RunID != "r1" {
		t.Fatalf("pagination: total=%d runs=%+v", total, runs)
	}
}

func TestRun_ByIDAndUnknown(t *testing.T) {
	db := seedTwoRuns(t)
	run, err := db.Run("r1")
	if err != nil || run == nil || run.RunID != "r1" || run.TasksDone != 1 {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	run, err = db.Run("nope")
	if err != nil || run != nil {
		t.Fatalf("unknown id: run=%+v err=%v", run, err)
	}
}

func TestEvents_FiltersAndLogOrder(t *testing.T) {
	db := seedTwoRuns(t)
	events, total, err := db.Events("r1", "", "", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || len(events) != 4 {
		t.Fatalf("total=%d len=%d, want 4/4", total, len(events))
	}
	var first struct {
		Event string `json:"event"`
	}
	if err := json.Unmarshal(events[0], &first); err != nil || first.Event != "run_start" {
		t.Fatalf("first event = %s (err %v), want run_start first (log order)", events[0], err)
	}
	events, total, err = db.Events("r1", "agent_run", "T1", 50, 0)
	if err != nil || total != 1 || len(events) != 1 {
		t.Fatalf("filtered: total=%d err=%v", total, err)
	}
}

func TestAgentRuns_FiltersCostNewestFirst(t *testing.T) {
	db := seedTwoRuns(t)
	recs, total, err := db.AgentRuns(AgentRunsFilter{}, 50, 0)
	if err != nil || total != 2 {
		t.Fatalf("total=%d err=%v", total, err)
	}
	if recs[0].RunID != "r2" { // newest first
		t.Fatalf("order: first = %+v", recs[0])
	}
	recs, _, err = db.AgentRuns(AgentRunsFilter{Role: "coder"}, 50, 0)
	if err != nil || len(recs) != 1 || recs[0].TaskID != "T1" {
		t.Fatalf("role filter: %+v err=%v", recs, err)
	}
	if recs[0].CostUSD < 0.0104 || recs[0].CostUSD > 0.0106 {
		t.Fatalf("cost = %v", recs[0].CostUSD)
	}
	if recs[0].TimedOut || recs[0].RateLimited {
		t.Fatalf("bools decoded wrong (fixture has both false): %+v", recs[0])
	}
}

func TestCostStats_GroupsAndSortsDescending(t *testing.T) {
	db := seedTwoRuns(t)
	rows, err := db.CostStats("role")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Key != "coder" { // coder cost > critic (unknown model = 0)
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].AgentRuns != 1 || rows[0].InputTokens != 1000 {
		t.Fatalf("rows[0] = %+v", rows[0])
	}
	if _, err := db.CostStats("run"); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/eventdb/ -v -run 'TestRuns|TestRun_|TestEvents_|TestAgentRuns_|TestCostStats'`
Expected: FAIL (compile: types undefined)

- [ ] **Step 4: Write the implementation**

`internal/eventdb/queries.go`:

```go
package eventdb

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/lionelchamorro/orquestalite/internal/cost"
)

// RunSummary is the contract shape served by GET /api/runs (features.md /
// orquesta docs/orq-lite-query-api.md). Do not rename fields.
type RunSummary struct {
	RunID       string   `json:"run_id"`
	Command     string   `json:"command"`
	Args        []string `json:"args"`
	Status      string   `json:"status"`
	StartedAt   string   `json:"started_at"`
	FinishedAt  *string  `json:"finished_at"`
	DurationS   *float64 `json:"duration_s"`
	OrqVersion  string   `json:"orq_version"`
	CostUSD     float64  `json:"cost_usd"`
	InputTokens int      `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	AgentRuns   int      `json:"agent_runs"`
	TasksDone   int      `json:"tasks_done"`
	TasksFailed int      `json:"tasks_failed"`
}

// AgentRunRecord is the contract shape served by GET /api/agent-runs.
type AgentRunRecord struct {
	Ts                string  `json:"ts"`
	RunID             string  `json:"run_id"`
	Role              string  `json:"role"`
	Agent             string  `json:"agent"`
	TaskID            string  `json:"task_id"`
	Cycle             int     `json:"cycle"`
	Attempt           int     `json:"attempt"`
	Provider          string  `json:"provider"`
	Model             string  `json:"model"`
	DurationS         float64 `json:"duration_s"`
	ExitCode          int     `json:"exit_code"`
	TimedOut          bool    `json:"timed_out"`
	RateLimited       bool    `json:"rate_limited"`
	InputTokens       int     `json:"input_tokens"`
	OutputTokens      int     `json:"output_tokens"`
	CachedInputTokens int     `json:"cached_input_tokens"`
	ReasoningTokens   int     `json:"reasoning_tokens"`
	CostUSD           float64 `json:"cost_usd"`
	ArtifactsDir      string  `json:"artifacts_dir"`
}

// CostRow is one row of GET /api/stats/cost.
type CostRow struct {
	Key          string  `json:"key"`
	CostUSD      float64 `json:"cost_usd"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	AgentRuns    int     `json:"agent_runs"`
}

type RunsFilter struct {
	Active *bool // nil = all; true = status running; false = not running
}

type AgentRunsFilter struct {
	RunID  string
	TaskID string
	Role   string
	Agent  string
}

// Runs lists runs newest-first with cost/token/task aggregates.
func (d *DB) Runs(f RunsFilter, limit, offset int) ([]RunSummary, int, error) {
	where, args := "", []any{}
	if f.Active != nil {
		if *f.Active {
			where = "WHERE status = 'running'"
		} else {
			where = "WHERE status != 'running'"
		}
	}
	var total int
	if err := d.sql.QueryRow("SELECT COUNT(*) FROM runs "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := d.sql.Query(`SELECT run_id, command, args, status, started_at, finished_at, duration_s, orq_version
		FROM runs `+where+` ORDER BY started_at DESC, run_id DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []RunSummary{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := d.decorateRuns(out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// Run returns one run's summary, or (nil, nil) when the id is unknown.
func (d *DB) Run(id string) (*RunSummary, error) {
	row := d.sql.QueryRow(`SELECT run_id, command, args, status, started_at, finished_at, duration_s, orq_version
		FROM runs WHERE run_id = ?`, id)
	r, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rs := []RunSummary{r}
	if err := d.decorateRuns(rs); err != nil {
		return nil, err
	}
	return &rs[0], nil
}

type rowScanner interface{ Scan(dest ...any) error }

func scanRun(row rowScanner) (RunSummary, error) {
	var r RunSummary
	var argsJSON string
	var finished sql.NullString
	var dur sql.NullFloat64
	if err := row.Scan(&r.RunID, &r.Command, &argsJSON, &r.Status, &r.StartedAt, &finished, &dur, &r.OrqVersion); err != nil {
		return r, err
	}
	r.Args = []string{}
	_ = json.Unmarshal([]byte(argsJSON), &r.Args)
	if r.Args == nil {
		r.Args = []string{}
	}
	if finished.Valid {
		r.FinishedAt = &finished.String
	}
	if dur.Valid {
		r.DurationS = &dur.Float64
	}
	return r, nil
}

// decorateRuns fills cost/token/agent-run/task aggregates for a page of runs
// with two grouped queries (no per-run N+1).
func (d *DB) decorateRuns(rs []RunSummary) error {
	if len(rs) == 0 {
		return nil
	}
	idx := make(map[string]*RunSummary, len(rs))
	placeholders := make([]string, 0, len(rs))
	ids := make([]any, 0, len(rs))
	for i := range rs {
		idx[rs[i].RunID] = &rs[i]
		placeholders = append(placeholders, "?")
		ids = append(ids, rs[i].RunID)
	}
	in := strings.Join(placeholders, ",")

	rows, err := d.sql.Query(`SELECT run_id, model, SUM(input_tokens), SUM(output_tokens), COUNT(*)
		FROM agent_runs WHERE run_id IN (`+in+`) GROUP BY run_id, model`, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var runID, model string
		var input, output, n int
		if err := rows.Scan(&runID, &model, &input, &output, &n); err != nil {
			return err
		}
		r := idx[runID]
		r.InputTokens += input
		r.OutputTokens += output
		r.AgentRuns += n
		if usd, ok := cost.EstimateUSD(model, input, output); ok {
			r.CostUSD += usd
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	trows, err := d.sql.Query(`SELECT run_id, type, COUNT(*)
		FROM events WHERE run_id IN (`+in+`)
		AND type IN ('task_done', 'task_done_no_commit', 'task_failed')
		GROUP BY run_id, type`, ids...)
	if err != nil {
		return err
	}
	defer trows.Close()
	for trows.Next() {
		var runID, typ string
		var n int
		if err := trows.Scan(&runID, &typ, &n); err != nil {
			return err
		}
		r := idx[runID]
		if typ == "task_failed" {
			r.TasksFailed += n
		} else {
			r.TasksDone += n
		}
	}
	return trows.Err()
}

// Events returns a run's raw events in log order, optionally filtered.
func (d *DB) Events(runID, typ, taskID string, limit, offset int) ([]json.RawMessage, int, error) {
	where, args := "WHERE run_id = ?", []any{runID}
	if typ != "" {
		where += " AND type = ?"
		args = append(args, typ)
	}
	if taskID != "" {
		where += " AND task_id = ?"
		args = append(args, taskID)
	}
	var total int
	if err := d.sql.QueryRow("SELECT COUNT(*) FROM events "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := d.sql.Query("SELECT raw FROM events "+where+" ORDER BY id LIMIT ? OFFSET ?",
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, 0, err
		}
		out = append(out, json.RawMessage(raw))
	}
	return out, total, rows.Err()
}

// AgentRuns lists agent invocations newest-first with per-run cost.
func (d *DB) AgentRuns(f AgentRunsFilter, limit, offset int) ([]AgentRunRecord, int, error) {
	where, args := "WHERE 1=1", []any{}
	for col, v := range map[string]string{"run_id": f.RunID, "task_id": f.TaskID, "role": f.Role, "agent": f.Agent} {
		if v != "" {
			where += " AND " + col + " = ?"
			args = append(args, v)
		}
	}
	var total int
	if err := d.sql.QueryRow("SELECT COUNT(*) FROM agent_runs "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := d.sql.Query(`SELECT ts, run_id, role, agent, task_id, cycle, attempt, provider, model,
			duration_s, exit_code, timed_out, rate_limited,
			input_tokens, output_tokens, cached_input_tokens, reasoning_tokens, artifacts_dir
		FROM agent_runs `+where+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []AgentRunRecord{}
	for rows.Next() {
		var r AgentRunRecord
		var timedOut, rateLimited int
		if err := rows.Scan(&r.Ts, &r.RunID, &r.Role, &r.Agent, &r.TaskID, &r.Cycle, &r.Attempt,
			&r.Provider, &r.Model, &r.DurationS, &r.ExitCode, &timedOut, &rateLimited,
			&r.InputTokens, &r.OutputTokens, &r.CachedInputTokens, &r.ReasoningTokens, &r.ArtifactsDir); err != nil {
			return nil, 0, err
		}
		r.TimedOut = timedOut != 0
		r.RateLimited = rateLimited != 0
		if usd, ok := cost.EstimateUSD(r.Model, r.InputTokens, r.OutputTokens); ok {
			r.CostUSD = usd
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// costStatsColumns maps the by= parameter to a grouping column. Fixed map —
// never interpolate request input into SQL.
var costStatsColumns = map[string]string{
	"run":   "run_id",
	"agent": "agent",
	"task":  "task_id",
	"role":  "role",
}

// CostStats aggregates cost/tokens grouped by run|agent|task|role, sorted by
// cost_usd descending (key ascending on ties, for stable output).
func (d *DB) CostStats(by string) ([]CostRow, error) {
	col, ok := costStatsColumns[by]
	if !ok {
		col = "run_id"
	}
	rows, err := d.sql.Query(`SELECT ` + col + `, model, SUM(input_tokens), SUM(output_tokens), COUNT(*)
		FROM agent_runs GROUP BY ` + col + `, model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	acc := map[string]*CostRow{}
	for rows.Next() {
		var key, model string
		var input, output, n int
		if err := rows.Scan(&key, &model, &input, &output, &n); err != nil {
			return nil, err
		}
		row := acc[key]
		if row == nil {
			row = &CostRow{Key: key}
			acc[key] = row
		}
		row.InputTokens += input
		row.OutputTokens += output
		row.AgentRuns += n
		if usd, ok := cost.EstimateUSD(model, input, output); ok {
			row.CostUSD += usd
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]CostRow, 0, len(acc))
	for _, r := range acc {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CostUSD != out[j].CostUSD {
			return out[i].CostUSD > out[j].CostUSD
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/eventdb/ ./internal/cost/ -v`
Expected: PASS (all)

- [ ] **Step 6: Commit**

```bash
git add internal/eventdb/queries.go internal/eventdb/queries_test.go internal/cost/prices.go internal/cost/prices_export_test.go
git commit -m "feat(eventdb): contract-shaped queries with first-party cost pricing"
```

---

### Task 5: `orq-lite index [--rebuild]` command

**Files:**
- Create: `internal/commands/indexcmd.go`
- Test: `internal/commands/indexcmd_test.go`
- Modify: `cmd/orq-lite/main.go` (new `case "index":` after `case "doctor":` at ~line 166; new usage line)

**Interfaces:**
- Consumes: `eventdb.Open`, `(*DB).Ingest`, `(*DB).Counts`.
- Produces: `commands.Index(projectDir string, rebuild bool, out io.Writer) error`.

- [ ] **Step 1: Write the failing test**

`internal/commands/indexcmd_test.go`:

```go
package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIndex_BuildsAndRebuildsDB(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, ".orquestalite")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	log := `{"ts":"2026-07-01T10:00:00Z","event":"run_start","run_id":"r1","command":"run","orq_version":"v0.2.0"}` + "\n"
	if err := os.WriteFile(filepath.Join(state, "run.log"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := Index(dir, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 runs") {
		t.Fatalf("output = %q, want '1 runs'", out.String())
	}
	if _, err := os.Stat(filepath.Join(state, "orq.db")); err != nil {
		t.Fatalf("orq.db missing: %v", err)
	}

	// --rebuild starts from scratch and lands on the same counts.
	out.Reset()
	if err := Index(dir, true, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 runs") || !strings.Contains(out.String(), "1 events") {
		t.Fatalf("rebuild output = %q", out.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/commands/ -run TestIndex_ -v`
Expected: FAIL (undefined `Index`)

- [ ] **Step 3: Write the implementation**

`internal/commands/indexcmd.go`:

```go
package commands

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lionelchamorro/orquestalite/internal/eventdb"
)

// Index builds or tops up the sqlite read-model (.orquestalite/orq.db) from
// run.log and rotated archives — the headless counterpart of the ingestion
// `orq-lite serve` performs continuously. rebuild deletes the db first and
// re-ingests the full history.
func Index(projectDir string, rebuild bool, out io.Writer) error {
	stateDir := filepath.Join(projectDir, ".orquestalite")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	dbPath := filepath.Join(stateDir, "orq.db")
	if rebuild {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if err := os.Remove(dbPath + suffix); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	db, err := eventdb.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Ingest(stateDir); err != nil {
		return err
	}
	runs, agentRuns, events, err := db.Counts()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "indexed: %d runs, %d agent runs, %d events -> %s\n", runs, agentRuns, events, dbPath)
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/commands/ -run TestIndex_ -v`
Expected: PASS

- [ ] **Step 5: Wire the CLI**

In `cmd/orq-lite/main.go`, directly after the `case "doctor":` block (~line 168), add:

```go
	case "index":
		fs := flag.NewFlagSet("index", flag.ExitOnError)
		rebuild := fs.Bool("rebuild", false, "delete orq.db and re-ingest the full log history")
		_ = fs.Parse(args)
		exit(commands.Index(".", *rebuild, os.Stdout))
```

In `usage()`, after the `doctor` line, add:

```
  index [--rebuild]     build the sqlite read-model (.orquestalite/orq.db) from run.log
```

- [ ] **Step 6: Verify end-to-end**

```bash
go build ./... && go test ./internal/commands/ -run TestIndex_ -v
```
Expected: build clean, test PASS

- [ ] **Step 7: Commit**

```bash
git add internal/commands/indexcmd.go internal/commands/indexcmd_test.go cmd/orq-lite/main.go
git commit -m "feat(cli): orq-lite index builds the eventdb read-model headlessly"
```

---

### Task 6: serve wiring + `GET /api/runs` and `GET /api/runs/{id}`

**Files:**
- Modify: `internal/web/server.go` (Server struct fields, `Handler()` routes, `Serve()` ticker, `writeJSON` helper, `eventDB()` method)
- Create: `internal/web/query.go`
- Test: `internal/web/query_test.go`

**Interfaces:**
- Consumes: `eventdb.Open/Ingest/Runs/Run` (Tasks 1–4).
- Produces: `(*Server).eventDB() (*eventdb.DB, error)` and `writeJSON(w, status, v)` — both reused by Tasks 7, 8, 11, 13.

- [ ] **Step 1: Write the failing tests**

`internal/web/query_test.go` (uses the existing `stateDir(t)` helper from `server_test.go`; note: `eventDB` debounces ingestion to once per second, so tests write the full log before the first request or use a fresh `Server` per request):

```go
package web

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func writeRunLog(t *testing.T, dir, content string) {
	t.Helper()
	path := filepath.Join(dir, ".orquestalite", "run.log")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const queryFixtureLog = `{"ts":"2026-07-01T10:00:00Z","event":"run_start","run_id":"r1","command":"run","args":["--feature","F1"],"orq_version":"v0.2.0"}
{"ts":"2026-07-01T10:00:05Z","event":"agent_run","run_id":"r1","role":"coder","agent":"sonnet","task_id":"T1","cycle":1,"attempt":1,"provider":"claude","model":"claude-sonnet-4-6","duration_s":42,"exit_code":0,"timed_out":false,"rate_limited":false,"input_tokens":1000,"output_tokens":500,"cached_input_tokens":200,"artifacts_dir":".orquestalite/runs/r1/a"}
{"ts":"2026-07-01T10:00:06Z","event":"task_done","run_id":"r1","task_id":"T1","commit_sha":"abc"}
{"ts":"2026-07-01T10:00:07Z","event":"run_end","run_id":"r1","status":"ok","duration_s":7,"orq_version":"v0.2.0"}
{"ts":"2026-07-01T11:00:00Z","event":"run_start","run_id":"r2","command":"factory","orq_version":"v0.2.0"}
`

func getJSON(t *testing.T, srv *Server, url string, into any) int {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%s: Cache-Control = %q, want no-store", url, rec.Header().Get("Cache-Control"))
	}
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("%s: bad JSON %q: %v", url, rec.Body.String(), err)
	}
	return rec.Code
}

func TestAPIRuns_NewestFirstWithActiveFilter(t *testing.T) {
	dir := stateDir(t)
	writeRunLog(t, dir, queryFixtureLog)
	srv := &Server{Dir: dir}

	var resp struct {
		Runs []struct {
			RunID   string   `json:"run_id"`
			Status  string   `json:"status"`
			Args    []string `json:"args"`
			CostUSD float64  `json:"cost_usd"`
		} `json:"runs"`
		Total int `json:"total"`
	}
	if code := getJSON(t, srv, "/api/runs", &resp); code != 200 {
		t.Fatalf("code = %d", code)
	}
	if resp.Total != 2 || len(resp.Runs) != 2 || resp.Runs[0].RunID != "r2" {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Runs[1].CostUSD == 0 {
		t.Fatalf("r1 cost missing: %+v", resp.Runs[1])
	}

	if code := getJSON(t, srv, "/api/runs?active=true", &resp); code != 200 {
		t.Fatalf("code = %d", code)
	}
	if resp.Total != 1 || resp.Runs[0].RunID != "r2" || resp.Runs[0].Status != "running" {
		t.Fatalf("active resp = %+v", resp)
	}

	// Garbage limit/offset fall back to defaults, never error.
	if code := getJSON(t, srv, "/api/runs?limit=banana&offset=-3", &resp); code != 200 || resp.Total != 2 {
		t.Fatalf("garbage pagination: code=%d resp=%+v", code, resp)
	}
}

func TestAPIRun_ByIDAnd404(t *testing.T) {
	dir := stateDir(t)
	writeRunLog(t, dir, queryFixtureLog)
	srv := &Server{Dir: dir}

	var run struct {
		RunID     string `json:"run_id"`
		TasksDone int    `json:"tasks_done"`
	}
	if code := getJSON(t, srv, "/api/runs/r1", &run); code != 200 || run.RunID != "r1" || run.TasksDone != 1 {
		t.Fatalf("code=%d run=%+v", code, run)
	}
	var errResp struct {
		Error string `json:"error"`
	}
	if code := getJSON(t, srv, "/api/runs/nope", &errResp); code != 404 || errResp.Error == "" {
		t.Fatalf("code=%d resp=%+v", code, errResp)
	}
}

func TestAPIRuns_EmptyStateServesEmptyList(t *testing.T) {
	srv := &Server{Dir: stateDir(t)} // .orquestalite exists but no run.log
	var resp struct {
		Runs  []any `json:"runs"`
		Total int   `json:"total"`
	}
	if code := getJSON(t, srv, "/api/runs", &resp); code != 200 {
		t.Fatalf("code = %d", code)
	}
	if resp.Runs == nil || resp.Total != 0 {
		t.Fatalf("resp = %+v, want empty non-null runs", resp)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/web/ -run TestAPIRuns -v`
Expected: FAIL (404 from mux / compile errors)

- [ ] **Step 3: Modify `internal/web/server.go`**

Add fields to the `Server` struct (after the cost cache fields):

```go
	dbMu       sync.Mutex
	db         *eventdb.DB
	lastIngest time.Time

	doctorMu      sync.Mutex
	doctorCached  []byte
	doctorFetched time.Time
```

Add the import `"github.com/lionelchamorro/orquestalite/internal/eventdb"` (plus `"log"`, `"path/filepath"` if not present).

Add routes in `Handler()` next to the existing `/api/*` registrations:

```go
	mux.HandleFunc("GET /api/runs", s.handleRuns)
	mux.HandleFunc("GET /api/runs/{id}", s.handleRun)
	mux.HandleFunc("GET /api/runs/{id}/events", s.handleRunEvents)
	mux.HandleFunc("GET /api/agent-runs", s.handleAgentRuns)
	mux.HandleFunc("GET /api/stats/cost", s.handleCostStats)
```

(`handleRunEvents`, `handleAgentRuns`, `handleCostStats` are added in Tasks 7–8; to keep this task compiling, register only `handleRuns`/`handleRun` now and add the other three lines in their tasks.)

Add below `statePath`:

```go
// eventDB lazily opens the read-model and tops it up from the log at most
// once per second. Query endpoints call it per request, so responses are at
// most ~1s stale — the same freshness the SSE logTail gives — and the Serve
// ticker calls it too so ingestion continues without traffic.
func (s *Server) eventDB() (*eventdb.DB, error) {
	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	stateDir := filepath.Join(s.Dir, ".orquestalite")
	if s.db == nil {
		if err := os.MkdirAll(stateDir, 0o755); err != nil {
			return nil, err
		}
		db, err := eventdb.Open(filepath.Join(stateDir, "orq.db"))
		if err != nil {
			return nil, err
		}
		s.db = db
	}
	if time.Since(s.lastIngest) >= time.Second {
		if err := s.db.Ingest(stateDir); err != nil {
			log.Printf("web: eventdb ingest: %v", err)
		}
		s.lastIngest = time.Now()
	}
	return s.db, nil
}

// writeJSON marshals v with the headers every JSON API response carries.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	raw, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
```

In `Serve()`, after `s := &Server{Dir: dir}`, add the 1s ingestion ticker:

```go
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_, _ = s.eventDB()
			}
		}
	}()
```

- [ ] **Step 4: Create `internal/web/query.go`**

```go
package web

import (
	"net/http"
	"strconv"

	"github.com/lionelchamorro/orquestalite/internal/eventdb"
)

// pageParams parses limit/offset with the contract defaults: limit=50
// (max 500), offset=0. Unparseable or out-of-range values fall back to the
// defaults rather than erroring.
func pageParams(r *http.Request) (limit, offset int) {
	limit, offset = 50, 0
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
		if limit > 500 {
			limit = 500
		}
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v > 0 {
		offset = v
	}
	return limit, offset
}

func (s *Server) queryDB(w http.ResponseWriter) (*eventdb.DB, bool) {
	db, err := s.eventDB()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return nil, false
	}
	return db, true
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	db, ok := s.queryDB(w)
	if !ok {
		return
	}
	limit, offset := pageParams(r)
	var active *bool
	switch r.URL.Query().Get("active") {
	case "true":
		v := true
		active = &v
	case "false":
		v := false
		active = &v
	}
	runs, total, err := db.Runs(eventdb.RunsFilter{Active: active}, limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs, "total": total})
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	db, ok := s.queryDB(w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	run, err := db.Run(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if run == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown run id: " + id})
		return
	}
	writeJSON(w, http.StatusOK, run)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/web/ -v`
Expected: PASS (new tests plus every pre-existing web test — the struct change must not break them)

- [ ] **Step 6: Commit**

```bash
git add internal/web/server.go internal/web/query.go internal/web/query_test.go
git commit -m "feat(serve): eventdb-backed GET /api/runs and /api/runs/{id}"
```

---

### Task 7: `GET /api/runs/{id}/events` and `GET /api/agent-runs`

**Files:**
- Modify: `internal/web/query.go`, `internal/web/server.go` (register the two routes)
- Test: `internal/web/query_test.go` (append)

**Interfaces:**
- Consumes: `db.Events`, `db.AgentRuns` (Task 4); `queryDB`, `pageParams`, `writeJSON` (Task 6).

- [ ] **Step 1: Write the failing tests**

Append to `internal/web/query_test.go`:

```go
func TestAPIRunEvents_LogOrderAndFilters(t *testing.T) {
	dir := stateDir(t)
	writeRunLog(t, dir, queryFixtureLog)
	srv := &Server{Dir: dir}

	var resp struct {
		Events []map[string]any `json:"events"`
		Total  int              `json:"total"`
	}
	if code := getJSON(t, srv, "/api/runs/r1/events", &resp); code != 200 {
		t.Fatalf("code = %d", code)
	}
	if resp.Total != 4 || resp.Events[0]["event"] != "run_start" {
		t.Fatalf("resp = %+v", resp)
	}
	if code := getJSON(t, srv, "/api/runs/r1/events?type=agent_run&task_id=T1", &resp); code != 200 || resp.Total != 1 {
		t.Fatalf("filtered: code=%d resp=%+v", code, resp)
	}
}

func TestAPIAgentRuns_FiltersAndCost(t *testing.T) {
	dir := stateDir(t)
	writeRunLog(t, dir, queryFixtureLog)
	srv := &Server{Dir: dir}

	var resp struct {
		AgentRuns []struct {
			Role    string  `json:"role"`
			TaskID  string  `json:"task_id"`
			CostUSD float64 `json:"cost_usd"`
			TimedOut bool   `json:"timed_out"`
		} `json:"agent_runs"`
		Total int `json:"total"`
	}
	if code := getJSON(t, srv, "/api/agent-runs?run_id=r1&role=coder", &resp); code != 200 {
		t.Fatalf("code = %d", code)
	}
	if resp.Total != 1 || resp.AgentRuns[0].TaskID != "T1" || resp.AgentRuns[0].CostUSD == 0 {
		t.Fatalf("resp = %+v", resp)
	}
	if code := getJSON(t, srv, "/api/agent-runs?role=nonexistent", &resp); code != 200 || resp.Total != 0 || resp.AgentRuns == nil {
		t.Fatalf("empty filter: code=%d resp=%+v", code, resp)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/web/ -run 'TestAPIRunEvents|TestAPIAgentRuns_Filters' -v`
Expected: FAIL (404: routes unregistered)

- [ ] **Step 3: Implement**

Register in `Handler()`:

```go
	mux.HandleFunc("GET /api/runs/{id}/events", s.handleRunEvents)
	mux.HandleFunc("GET /api/agent-runs", s.handleAgentRuns)
```

Append to `internal/web/query.go`:

```go
func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	db, ok := s.queryDB(w)
	if !ok {
		return
	}
	limit, offset := pageParams(r)
	q := r.URL.Query()
	events, total, err := db.Events(r.PathValue("id"), q.Get("type"), q.Get("task_id"), limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "total": total})
}

func (s *Server) handleAgentRuns(w http.ResponseWriter, r *http.Request) {
	db, ok := s.queryDB(w)
	if !ok {
		return
	}
	limit, offset := pageParams(r)
	q := r.URL.Query()
	recs, total, err := db.AgentRuns(eventdb.AgentRunsFilter{
		RunID:  q.Get("run_id"),
		TaskID: q.Get("task_id"),
		Role:   q.Get("role"),
		Agent:  q.Get("agent"),
	}, limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_runs": recs, "total": total})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/web/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/web/query.go internal/web/query_test.go internal/web/server.go
git commit -m "feat(serve): GET /api/runs/{id}/events and /api/agent-runs"
```

---

### Task 8: `GET /api/stats/cost`

**Files:**
- Modify: `internal/web/query.go`, `internal/web/server.go` (register route)
- Test: `internal/web/query_test.go` (append)

**Interfaces:**
- Consumes: `db.CostStats` (Task 4).

- [ ] **Step 1: Write the failing test**

Append to `internal/web/query_test.go`:

```go
func TestAPIStatsCost_GroupsByAndEchoes(t *testing.T) {
	dir := stateDir(t)
	writeRunLog(t, dir, queryFixtureLog)
	srv := &Server{Dir: dir}

	var resp struct {
		By   string `json:"by"`
		Rows []struct {
			Key       string  `json:"key"`
			CostUSD   float64 `json:"cost_usd"`
			AgentRuns int     `json:"agent_runs"`
		} `json:"rows"`
	}
	if code := getJSON(t, srv, "/api/stats/cost?by=role", &resp); code != 200 {
		t.Fatalf("code = %d", code)
	}
	if resp.By != "role" || len(resp.Rows) != 1 || resp.Rows[0].Key != "coder" {
		t.Fatalf("resp = %+v", resp)
	}
	// Default and unknown by both fall back to run.
	if code := getJSON(t, srv, "/api/stats/cost?by=bogus", &resp); code != 200 || resp.By != "run" {
		t.Fatalf("fallback: code=%d by=%q", code, resp.By)
	}
	if code := getJSON(t, srv, "/api/stats/cost", &resp); code != 200 || resp.By != "run" || resp.Rows[0].Key != "r1" {
		t.Fatalf("default: code=%d resp=%+v", code, resp)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/web/ -run TestAPIStatsCost -v`
Expected: FAIL (404)

- [ ] **Step 3: Implement**

Register in `Handler()`:

```go
	mux.HandleFunc("GET /api/stats/cost", s.handleCostStats)
```

Append to `internal/web/query.go`:

```go
func (s *Server) handleCostStats(w http.ResponseWriter, r *http.Request) {
	db, ok := s.queryDB(w)
	if !ok {
		return
	}
	by := r.URL.Query().Get("by")
	switch by {
	case "run", "agent", "task", "role":
	default:
		by = "run"
	}
	rows, err := db.CostStats(by)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"by": by, "rows": rows})
}
```

- [ ] **Step 4: Run tests, then the full suite**

Run: `go test ./internal/web/ -v && go test ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/web/query.go internal/web/query_test.go internal/web/server.go
git commit -m "feat(serve): GET /api/stats/cost grouped cost rollups"
```

---

### Task 9: `docs/query-api.md`

**Files:**
- Create: `docs/query-api.md`

- [ ] **Step 1: Write the doc**

`docs/query-api.md` — document every endpoint with example responses. Content:

````markdown
# Query API

`orq-lite serve` (default `127.0.0.1:4173`) exposes read-only JSON endpoints
over a sqlite read-model of `.orquestalite/run.log` (db at
`.orquestalite/orq.db`, built automatically by serve on a 1-second cadence, or
headlessly via `orq-lite index [--rebuild]`). Every endpoint is `GET`, returns
`Cache-Control: no-store`, and the shapes below are a contract shared with the
orquesta control plane (`docs/orq-lite-query-api.md` in that repo) — change
them in both repos or not at all.

Pagination everywhere: `limit` (default 50, max 500) and `offset` (default 0).
Values that fail to parse fall back to the defaults; unknown query params are
ignored.

## GET /api/runs?limit=&offset=&active=true|false

Run history, newest first. `active=true` filters to `status == "running"`
(for correlating companion-app launch records with `run_id`); `active=false`
to finished runs.

```json
{
  "runs": [
    {
      "run_id": "r20260701T120000Z-4f2a",
      "command": "factory",
      "args": ["features.md"],
      "status": "ok",
      "started_at": "2026-07-01T12:00:00.123456789Z",
      "finished_at": "2026-07-01T12:41:07.5Z",
      "duration_s": 2467,
      "orq_version": "v0.2.0",
      "cost_usd": 1.0421,
      "input_tokens": 291042,
      "output_tokens": 48211,
      "agent_runs": 14,
      "tasks_done": 5,
      "tasks_failed": 1
    }
  ],
  "total": 1
}
```

`status` is one of `running` (no `run_end` yet — `finished_at` and
`duration_s` are `null`), `ok`, `error`, `interrupted`. `tasks_done` counts
`task_done` and `task_done_no_commit` events; `tasks_failed` counts
`task_failed`. `cost_usd` prices first-party token counts with the same table
`GET /api/cost` uses; agent runs with unknown models contribute tokens but
zero cost.

## GET /api/runs/{id}

One `RunSummary` (same shape as above). Unknown id:

```json
HTTP 404
{"error": "unknown run id: r20990101T000000Z-dead"}
```

## GET /api/runs/{id}/events?type=&task_id=&limit=&offset=

The run's raw JSONL events, parsed, in log order. `type` and `task_id`
filter; omitted means all.

```json
{
  "events": [
    {"ts": "2026-07-01T12:00:00Z", "event": "run_start", "run_id": "r20260701T120000Z-4f2a", "command": "factory", "args": ["features.md"], "orq_version": "v0.2.0"},
    {"ts": "2026-07-01T12:00:41Z", "event": "task_start", "run_id": "r20260701T120000Z-4f2a", "task_id": "T001", "title": "…", "attempt": 1}
  ],
  "total": 2
}
```

## GET /api/agent-runs?run_id=&task_id=&role=&agent=&limit=&offset=

Individual agent invocations, newest first. All filters optional and ANDed.

```json
{
  "agent_runs": [
    {
      "ts": "2026-07-01T12:05:41Z",
      "run_id": "r20260701T120000Z-4f2a",
      "role": "coder",
      "agent": "sonnet",
      "task_id": "T001",
      "cycle": 1,
      "attempt": 1,
      "provider": "claude",
      "model": "claude-sonnet-4-6",
      "duration_s": 42,
      "exit_code": 0,
      "timed_out": false,
      "rate_limited": false,
      "input_tokens": 18042,
      "output_tokens": 2211,
      "cached_input_tokens": 15020,
      "reasoning_tokens": 0,
      "cost_usd": 0.0873,
      "artifacts_dir": ".orquestalite/runs/r20260701T120000Z-4f2a/agents/T001/coder.c1.a1"
    }
  ],
  "total": 1
}
```

`input_tokens` includes cached input tokens (mirroring the `agent_run`
event). Missing token fields on old events read as 0.

## GET /api/stats/cost?by=run|agent|task|role

Cost rollup grouped by the given dimension (default `run`; unknown values
fall back to `run`), sorted by `cost_usd` descending.

```json
{
  "by": "role",
  "rows": [
    {"key": "coder",  "cost_usd": 0.7311, "input_tokens": 201042, "output_tokens": 31200, "agent_runs": 8},
    {"key": "critic", "cost_usd": 0.3110, "input_tokens": 90000,  "output_tokens": 17011, "agent_runs": 6}
  ]
}
```

## GET /api/flows

The workspace's `flows.json` parsed with the engine's own parser, for
building a launch form without filesystem access. Empty or missing
`flows.json` → `{"flows": []}`. Ordered by name.

```json
{
  "flows": [
    {
      "name": "factory",
      "description": "decompose features.md and build each feature",
      "inputs": {
        "features_file": {"type": "string", "default": null, "required": true},
        "max_cycles": {"type": "number", "default": 3, "required": false}
      },
      "roles": ["coder", "critic", "parser"],
      "preflight": {"coder": "ok", "critic": "missing_prompt", "parser": "missing_role"}
    }
  ]
}
```

`required` means the flow declares no default for that input. `roles` is the
sorted set of agent roles referenced by any agent step, recursively through
`loop`/`retry_until` bodies. `preflight` per role: `ok` (role exists in
`team.json` and its prompt file exists), `missing_role`, or
`missing_prompt`.

## GET /api/doctor

The `orq-lite doctor` preflight as JSON — same check functions, so CLI and
endpoint can never disagree. Cached server-side for 30s.

```json
{
  "ok": false,
  "checks": [
    {"name": "git",             "status": "ok",    "detail": "repository present, tree clean"},
    {"name": "eventdb",         "status": "warn",  "detail": "orq.db not found — run `orq-lite index` or `orq-lite serve` to build it"},
    {"name": "team.json",       "status": "error", "detail": "read team.json: no such file"},
    {"name": "provider:claude", "status": "ok",    "detail": "on PATH"},
    {"name": "binary:gh",       "status": "warn",  "detail": "not on PATH — factory --pr disabled"}
  ]
}
```

`ok` is false iff any check has status `error`. Statuses map to the CLI's
PASS/WARN/FAIL.
````

- [ ] **Step 2: Commit**

```bash
git add docs/query-api.md
git commit -m "docs: query API reference for serve endpoints"
```

---

### Task 10: engine — `InputSpec.HasDefault` and `Flow.ReferencedRoles`

**Files:**
- Modify: `internal/engine/engine.go` (InputSpec + UnmarshalJSON + ReferencedRoles; add `"sort"` import)
- Modify: `internal/commands/flowcmd.go` (delete `flowReferencedRoles` at lines 87–106; replace its one call site inside `RunFlow` with `flow.ReferencedRoles()`)
- Test: `internal/engine/engine_extra_test.go` (append)

**Interfaces:**
- Produces: `InputSpec.HasDefault bool` (true iff the flow declared a `default` key, `"default": null` included) and `(*Flow).ReferencedRoles() []string` (sorted, deduplicated, recursive through `Body`). Task 11's handler consumes both.

- [ ] **Step 1: Write the failing tests**

Append to `internal/engine/engine_extra_test.go`:

```go
func TestInputSpec_HasDefaultDistinguishesDeclaredNull(t *testing.T) {
	var f Flows
	raw := `{"flows":{"x":{"steps":[],"inputs":{
		"no_default":   {"type":"string"},
		"null_default": {"type":"string","default":null},
		"real_default": {"type":"number","default":3}
	}}}}`
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		t.Fatal(err)
	}
	in := f.Flows["x"].Inputs
	if in["no_default"].HasDefault {
		t.Fatal("no_default: HasDefault = true, want false")
	}
	if !in["null_default"].HasDefault || in["null_default"].Default != nil {
		t.Fatalf("null_default: %+v", in["null_default"])
	}
	if !in["real_default"].HasDefault || in["real_default"].Default != 3.0 {
		t.Fatalf("real_default: %+v", in["real_default"])
	}
}

func TestFlowReferencedRoles_RecursesAndSorts(t *testing.T) {
	flow := Flow{Steps: []Step{
		{Type: "agent", Agent: "parser"},
		{Type: "loop", Iterator: "{xs}", As: "x", Body: []Step{
			{Type: "retry_until", Condition: "{ok} == true", Body: []Step{
				{Type: "agent", Agent: "coder"},
				{Type: "agent", Agent: "critic"},
			}},
			{Type: "agent", Agent: "coder"}, // duplicate
		}},
		{Type: "command", Command: "true"},
	}}
	got := flow.ReferencedRoles()
	want := []string{"coder", "critic", "parser"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("roles = %v, want %v", got, want)
	}
}
```

(Add `"encoding/json"` to the test imports if absent.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/engine/ -run 'TestInputSpec_HasDefault|TestFlowReferencedRoles' -v`
Expected: FAIL (`HasDefault`, `ReferencedRoles` undefined)

- [ ] **Step 3: Implement in `internal/engine/engine.go`**

Replace the `InputSpec` declaration with:

```go
type InputSpec struct {
	Type    string `json:"type,omitempty"`
	Default any    `json:"default,omitempty"`
	// HasDefault records whether the flow declared a "default" key at all
	// ("default": null counts as declared). The engine seeds undeclared
	// inputs with nil either way; the flow catalog endpoint uses this to
	// mark inputs required.
	HasDefault bool `json:"-"`
}

func (s *InputSpec) UnmarshalJSON(b []byte) error {
	var aux struct {
		Type    string          `json:"type"`
		Default json.RawMessage `json:"default"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	s.Type = aux.Type
	s.HasDefault = aux.Default != nil
	s.Default = nil
	if aux.Default != nil {
		if err := json.Unmarshal(aux.Default, &s.Default); err != nil {
			return err
		}
	}
	return nil
}
```

Add next to `LoadFlows`:

```go
// ReferencedRoles returns the sorted set of agent roles referenced by any
// agent step, recursively through loop/retry_until bodies. Shared by the
// flow-run preflight and the GET /api/flows catalog.
func (f *Flow) ReferencedRoles() []string {
	seen := map[string]bool{}
	var walk func(steps []Step)
	walk = func(steps []Step) {
		for _, s := range steps {
			if s.Agent != "" {
				seen[s.Agent] = true
			}
			if len(s.Body) > 0 {
				walk(s.Body)
			}
		}
	}
	walk(f.Steps)
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}
```

In `internal/commands/flowcmd.go`: delete the `flowReferencedRoles` function (lines 87–106) and change its single call site inside `RunFlow` from `flowReferencedRoles(&flow)` to `flow.ReferencedRoles()`.

- [ ] **Step 4: Run the engine and commands suites (regression: shipped flows.json must still load)**

Run: `go test ./internal/engine/ ./internal/commands/ -v`
Expected: PASS, including `TestShippedFlowsJSON_FactoryFast` and `TestLiveFlowsJSONValid`

- [ ] **Step 5: Commit**

```bash
git add internal/engine/engine.go internal/engine/engine_extra_test.go internal/commands/flowcmd.go
git commit -m "feat(engine): InputSpec.HasDefault and Flow.ReferencedRoles for the flow catalog"
```

---

### Task 11: `GET /api/flows`

**Files:**
- Create: `internal/web/flows.go`
- Modify: `internal/web/server.go` (register route)
- Test: `internal/web/flows_test.go`

**Interfaces:**
- Consumes: `engine.LoadFlows`, `Flow.ReferencedRoles`, `InputSpec.HasDefault` (Task 10); `config.Load`; `writeJSON` (Task 6).

- [ ] **Step 1: Write the failing tests**

`internal/web/flows_test.go`:

```go
package web

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAPIFlows_EmptyWhenMissingOrInvalid(t *testing.T) {
	srv := &Server{Dir: stateDir(t)} // no flows.json
	var resp struct {
		Flows []any `json:"flows"`
	}
	if code := getJSON(t, srv, "/api/flows", &resp); code != 200 {
		t.Fatalf("code = %d", code)
	}
	if resp.Flows == nil || len(resp.Flows) != 0 {
		t.Fatalf("resp = %+v, want empty non-null flows", resp)
	}

	dir := stateDir(t)
	if err := os.WriteFile(filepath.Join(dir, "flows.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv = &Server{Dir: dir}
	if code := getJSON(t, srv, "/api/flows", &resp); code != 200 || len(resp.Flows) != 0 {
		t.Fatalf("invalid flows.json: code=%d resp=%+v", code, resp)
	}
}

func TestAPIFlows_CatalogWithPreflight(t *testing.T) {
	dir := stateDir(t)
	// coder exists with a prompt on disk; critic exists but its prompt file
	// is missing; parser is not declared at all.
	flowsJSON := `{"flows":{"build":{
		"description":"build a thing",
		"inputs":{
			"features_file":{"type":"string"},
			"max_cycles":{"type":"number","default":3}
		},
		"steps":[
			{"type":"agent","agent":"parser"},
			{"type":"loop","iterator":"{xs}","as":"x","body":[
				{"type":"retry_until","condition":"{ok} == true","body":[
					{"type":"agent","agent":"coder"},
					{"type":"agent","agent":"critic"}
				]}
			]}
		]}}}`
	teamJSON := `{
		"agents":{"sonnet":{"provider":"claude","model":"claude-sonnet-4-6"}},
		"roles":{
			"coder":{"agents":["sonnet"],"prompt":"prompts/coder.md","result_path":".orquestalite/results/coder.json","timeout_seconds":60},
			"critic":{"agents":["sonnet"],"prompt":"prompts/critic.md","result_path":".orquestalite/results/critic.json","timeout_seconds":60}
		},
		"limits":{"max_review_cycles":1,"max_fix_iterations":1},
		"rate_limit_backoff":{"initial_seconds":1,"factor":2,"max_seconds":2}
	}`
	if err := os.WriteFile(filepath.Join(dir, "flows.json"), []byte(flowsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "team.json"), []byte(teamJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "coder.md"), []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &Server{Dir: dir}
	var resp struct {
		Flows []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Inputs      map[string]struct {
				Type     string `json:"type"`
				Default  any    `json:"default"`
				Required bool   `json:"required"`
			} `json:"inputs"`
			Roles     []string          `json:"roles"`
			Preflight map[string]string `json:"preflight"`
		} `json:"flows"`
	}
	if code := getJSON(t, srv, "/api/flows", &resp); code != 200 {
		t.Fatalf("code = %d", code)
	}
	if len(resp.Flows) != 1 || resp.Flows[0].Name != "build" {
		t.Fatalf("resp = %+v", resp)
	}
	f := resp.Flows[0]
	if !f.Inputs["features_file"].Required || f.Inputs["features_file"].Default != nil {
		t.Fatalf("features_file = %+v", f.Inputs["features_file"])
	}
	if f.Inputs["max_cycles"].Required || f.Inputs["max_cycles"].Default != 3.0 {
		t.Fatalf("max_cycles = %+v", f.Inputs["max_cycles"])
	}
	want := []string{"coder", "critic", "parser"}
	if len(f.Roles) != 3 || f.Roles[0] != want[0] || f.Roles[1] != want[1] || f.Roles[2] != want[2] {
		t.Fatalf("roles = %v", f.Roles)
	}
	if f.Preflight["coder"] != "ok" || f.Preflight["critic"] != "missing_prompt" || f.Preflight["parser"] != "missing_role" {
		t.Fatalf("preflight = %v", f.Preflight)
	}
}
```

If `config.Load` rejects the fixture team.json, align the `limits`/`rate_limit_backoff` field names with the json tags in `internal/config/config.go` (the check semantics of the test stay the same).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/web/ -run TestAPIFlows -v`
Expected: FAIL (404: route unregistered)

- [ ] **Step 3: Implement**

Register in `Handler()`:

```go
	mux.HandleFunc("GET /api/flows", s.handleFlows)
```

`internal/web/flows.go`:

```go
package web

import (
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/lionelchamorro/orquestalite/internal/config"
	"github.com/lionelchamorro/orquestalite/internal/engine"
)

type flowInput struct {
	Type     string `json:"type"`
	Default  any    `json:"default"`
	Required bool   `json:"required"`
}

type flowEntry struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Inputs      map[string]flowInput `json:"inputs"`
	Roles       []string             `json:"roles"`
	Preflight   map[string]string    `json:"preflight"`
}

// handleFlows serves the workspace's flows.json parsed with the same loader
// `orq-lite flow run` uses, so a companion app can build a launch form
// without filesystem access. Anything the parser rejects degrades to an
// empty catalog with a log line — never an error response.
func (s *Server) handleFlows(w http.ResponseWriter, r *http.Request) {
	entries := []flowEntry{}
	flows, err := engine.LoadFlows(filepath.Join(s.Dir, "flows.json"))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("web: flows.json unreadable, serving empty catalog: %v", err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"flows": entries})
		return
	}
	cfg, cfgErr := config.Load(filepath.Join(s.Dir, "team.json"))

	names := make([]string, 0, len(flows.Flows))
	for name := range flows.Flows {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		flow := flows.Flows[name]
		inputs := map[string]flowInput{}
		for in, spec := range flow.Inputs {
			inputs[in] = flowInput{Type: spec.Type, Default: spec.Default, Required: !spec.HasDefault}
		}
		roles := flow.ReferencedRoles()
		preflight := map[string]string{}
		for _, role := range roles {
			preflight[role] = rolePreflight(s.Dir, cfg, cfgErr, role)
		}
		entries = append(entries, flowEntry{
			Name:        name,
			Description: flow.Description,
			Inputs:      inputs,
			Roles:       roles,
			Preflight:   preflight,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"flows": entries})
}

// rolePreflight classifies launch readiness for one referenced role. An
// unloadable team.json means no role can be resolved, so every role reports
// missing_role.
func rolePreflight(dir string, cfg *config.Config, cfgErr error, role string) string {
	if cfgErr != nil {
		return "missing_role"
	}
	r, ok := cfg.Roles[role]
	if !ok {
		return "missing_role"
	}
	if _, err := os.Stat(filepath.Join(dir, r.Prompt)); err != nil {
		return "missing_prompt"
	}
	return "ok"
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/web/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/web/flows.go internal/web/flows_test.go internal/web/server.go
git commit -m "feat(serve): GET /api/flows catalog with role preflight"
```

---

### Task 12: extract `internal/doctor` package (shared by CLI and endpoint)

**Files:**
- Create: `internal/doctor/doctor.go`, `internal/doctor/doctor_test.go`
- Modify: `internal/commands/doctorcmd.go` (becomes a thin formatter), `internal/commands/runcmd.go:432` (call `doctor.ProviderHasUsableCredentials`), `internal/commands/doctorcmd_test.go` (move credential tests to the new package)

**Interfaces:**
- Produces: `doctor.Status` (`"ok" | "warn" | "error"` — these strings are the endpoint contract), `doctor.Check{Name, Status, Detail}` (json tags `name`, `status`, `detail`), `doctor.Run(ctx context.Context, dir string) []Check`, `doctor.ProviderHasUsableCredentials(provider string) bool`.
- Check names: `git`, `eventdb`, `team.json`, `prompts`, `provider:<name>`, `credentials:<name>`, `conventions_file`, `full_test_command`, `binary:agtop`, `binary:gh`, `binary:agent-browser`.

- [ ] **Step 1: Write the failing test**

`internal/doctor/doctor_test.go`:

```go
package doctor

import (
	"context"
	"strings"
	"testing"
)

func TestRun_EmptyDirReportsTeamJSONError(t *testing.T) {
	checks := Run(context.Background(), t.TempDir())
	byName := map[string]Check{}
	for _, c := range checks {
		byName[c.Name] = c
	}
	team, ok := byName["team.json"]
	if !ok || team.Status != StatusError {
		t.Fatalf("team.json check = %+v", team)
	}
	ev, ok := byName["eventdb"]
	if !ok || ev.Status != StatusWarn || !strings.Contains(ev.Detail, "orq-lite index") {
		t.Fatalf("eventdb check = %+v", ev)
	}
	for _, c := range checks {
		switch c.Status {
		case StatusOK, StatusWarn, StatusError:
		default:
			t.Fatalf("check %q has invalid status %q", c.Name, c.Status)
		}
	}
}

// Credential probing (moved from internal/commands/doctorcmd_test.go —
// keep the original assertions, they exercise ProviderHasUsableCredentials
// with t.Setenv / fake HOME).
func TestProviderHasUsableCredentials_UnknownProviderAssumedUsable(t *testing.T) {
	if !ProviderHasUsableCredentials("mystery-provider") {
		t.Fatal("unknown provider must be assumed usable")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/doctor/ -v`
Expected: FAIL (package doesn't exist)

- [ ] **Step 3: Create `internal/doctor/doctor.go`**

Move the body of `internal/commands/doctorcmd.go` (reproduced below as it exists today: `credentialPaths`, `runDoctorChecks`, `missingPromptFiles`, `usedProviders`, `providerHasUsableCredentials`, `firstExistingHomeFile`) into the new package with these mechanical changes — check logic stays byte-for-byte identical:

1. Package clause `package doctor`; keep imports (`errors`, `fmt`, `os`, `os/exec`, `path/filepath`, `sort`, `strings`, `internal/config`, `internal/gitx`) and add `context` plus `internal/eventdb`.
2. Replace the level type:

```go
// Status of one check. These exact strings are the GET /api/doctor contract;
// the CLI maps them to PASS/WARN/FAIL for display.
type Status string

const (
	StatusOK    Status = "ok"
	StatusWarn  Status = "warn"
	StatusError Status = "error"
)

type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
}
```

3. Rename `runDoctorChecks(dir string) []check` → `Run(ctx context.Context, dir string) []Check` and inside it substitute `checkPass`→`StatusOK`, `checkWarn`→`StatusWarn`, `checkFail`→`StatusError`, `check{...}`→`Check{...}`. The `ctx` parameter is the budget for checks that shell out — callers pass a ~2s timeout and such checks must degrade to `StatusWarn` on `ctx.Err()` rather than block; today every check is `exec.LookPath` + stat + env reads, so none consult ctx yet. Document that on the function.
4. Rename check names at the `add(...)` call sites: `provider+" CLI"` → `"provider:"+provider`; `provider+" auth"` → `"credentials:"+provider`; `"agtop"` → `"binary:agtop"`; `"gh"` → `"binary:gh"`; `"agent-browser"` → `"binary:agent-browser"`. Keep `git`/`git repo`/`git tree`, `team.json`, `prompts`, `conventions_file`, `full_test_command` as they are.
5. Export `providerHasUsableCredentials` → `ProviderHasUsableCredentials` (same body and doc comment).
6. Add the new eventdb check right after the git block (before the team.json early-return, so it always runs):

```go
	// eventdb: the sqlite read-model behind the query API
	dbPath := filepath.Join(dir, ".orquestalite", "orq.db")
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		add(StatusWarn, "eventdb", "orq.db not found — run `orq-lite index` or `orq-lite serve` to build it")
	} else if db, err := eventdb.Open(dbPath); err != nil {
		add(StatusError, "eventdb", err.Error())
	} else {
		_ = db.Close()
		add(StatusOK, "eventdb", dbPath)
	}
```

- [ ] **Step 4: Rewrite `internal/commands/doctorcmd.go` as the thin CLI formatter**

Replace the entire file with:

```go
package commands

import (
	"context"
	"fmt"
	"io"

	"github.com/lionelchamorro/orquestalite/internal/doctor"
)

// Doctor preflights the whole setup — git state, team.json, prompts, agent
// CLIs, credentials, test command, optional tooling — before any money is
// spent on agent calls. Misconfiguration is the top source of wasted runs
// (see tasks/orq-lite-fastapi-sse-test-findings.md). Returns an error iff
// any error-level check fires. The checks themselves live in
// internal/doctor, shared verbatim with GET /api/doctor.
func Doctor(projectDir string, out io.Writer) error {
	checks := doctor.Run(context.Background(), projectDir)
	label := map[doctor.Status]string{
		doctor.StatusOK:    "PASS",
		doctor.StatusWarn:  "WARN",
		doctor.StatusError: "FAIL",
	}
	failed := 0
	for _, c := range checks {
		fmt.Fprintf(out, "[%s] %-22s %s\n", label[c.Status], c.Name, c.Detail)
		if c.Status == doctor.StatusError {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("doctor: %d check(s) failed", failed)
	}
	fmt.Fprintln(out, "\nall checks passed")
	return nil
}
```

Then: in `internal/commands/runcmd.go:432` change `providerHasUsableCredentials(ag.Provider)` to `doctor.ProviderHasUsableCredentials(ag.Provider)` (add the import). Move the remaining `providerHasUsableCredentials` tests from `internal/commands/doctorcmd_test.go` into `internal/doctor/doctor_test.go` (adjusting the call name), and delete `internal/commands/doctorcmd_test.go` if nothing else remains in it.

- [ ] **Step 5: Run the suites**

Run: `go test ./internal/doctor/ ./internal/commands/ -v && go build ./...`
Expected: PASS, build clean

- [ ] **Step 6: Commit**

```bash
git add internal/doctor/ internal/commands/doctorcmd.go internal/commands/runcmd.go internal/commands/doctorcmd_test.go
git commit -m "refactor(doctor): extract checks into internal/doctor for reuse by serve"
```

---

### Task 13: `GET /api/doctor` with 30s cache

**Files:**
- Create: `internal/web/doctor.go`
- Modify: `internal/web/server.go` (register route; the cache fields were added in Task 6)
- Test: `internal/web/doctor_test.go`

**Interfaces:**
- Consumes: `doctor.Run`, `doctor.StatusError` (Task 12); `Server.doctorMu/doctorCached/doctorFetched` (Task 6).

- [ ] **Step 1: Write the failing test**

`internal/web/doctor_test.go`:

```go
package web

import (
	"testing"
)

func TestAPIDoctor_ReportsChecksAndCaches(t *testing.T) {
	srv := &Server{Dir: stateDir(t)} // empty project: team.json check errors

	var resp struct {
		OK     bool `json:"ok"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if code := getJSON(t, srv, "/api/doctor", &resp); code != 200 {
		t.Fatalf("code = %d", code)
	}
	if resp.OK {
		t.Fatal("ok = true for empty project, want false (team.json missing)")
	}
	found := false
	for _, c := range resp.Checks {
		if c.Name == "team.json" && c.Status == "error" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no team.json error check in %+v", resp.Checks)
	}

	// Second request within 30s serves the cached bytes.
	first := srv.doctorCached
	if code := getJSON(t, srv, "/api/doctor", &resp); code != 200 {
		t.Fatalf("code = %d", code)
	}
	if string(srv.doctorCached) != string(first) || srv.doctorCached == nil {
		t.Fatal("cache not reused within TTL")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/web/ -run TestAPIDoctor -v`
Expected: FAIL (404)

- [ ] **Step 3: Implement**

Register in `Handler()`:

```go
	mux.HandleFunc("GET /api/doctor", s.handleDoctor)
```

`internal/web/doctor.go`:

```go
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/doctor"
)

// handleDoctor exposes the CLI's preflight checks so a companion app can
// gate launches on a red preflight. Same pattern as the cost cache: results
// are cached for 30s because a UI may poll this. Checks run under a 2s
// budget — anything slower degrades to warn inside doctor.Run rather than
// blocking the request.
func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")

	s.doctorMu.Lock()
	defer s.doctorMu.Unlock()
	if time.Since(s.doctorFetched) < 30*time.Second && s.doctorCached != nil {
		_, _ = w.Write(s.doctorCached)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	checks := doctor.Run(ctx, s.Dir)
	ok := true
	for _, c := range checks {
		if c.Status == doctor.StatusError {
			ok = false
			break
		}
	}
	raw, err := json.Marshal(map[string]any{"ok": ok, "checks": checks})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.doctorCached = raw
	s.doctorFetched = time.Now()
	_, _ = w.Write(raw)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/web/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/web/doctor.go internal/web/doctor_test.go internal/web/server.go
git commit -m "feat(serve): GET /api/doctor with 30s cache"
```

---

### Task 14: final verification sweep

**Files:** none new.

- [ ] **Step 1: Full test suite, vet, release cross-compile**

```bash
go test ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...
CGO_ENABLED=0 go build -trimpath ./cmd/orq-lite
```
Expected: all PASS / exit 0.

- [ ] **Step 2: Manual smoke test against this repo's own state**

```bash
./orq-lite index
./orq-lite serve --addr 127.0.0.1:4199 &
sleep 2
curl -s 127.0.0.1:4199/api/runs | head -c 400; echo
curl -s 127.0.0.1:4199/api/flows | head -c 400; echo
curl -s 127.0.0.1:4199/api/doctor | head -c 400; echo
curl -s 127.0.0.1:4199/api/stats/cost?by=role | head -c 400; echo
kill %1
```
Expected: JSON from every endpoint; `/api/runs` shows real run history if `.orquestalite/run.log` exists (empty lists otherwise); no panics in serve output.

- [ ] **Step 3: Contract re-read**

Re-read `features.md` shape blocks against `internal/eventdb/queries.go` JSON tags and the handler response envelopes (`runs`/`total`, `events`/`total`, `agent_runs`/`total`, `by`/`rows`, `flows`, `ok`/`checks`) — field-for-field. Fix any drift now.

- [ ] **Step 4: Commit any fixes, update tasks/todo.md (mark Task 11b done), final commit**

```bash
git add -A
git commit -m "chore: verification sweep for query API, flow catalog, doctor endpoint"
```

---

## Self-Review Notes

- **Spec coverage:** sqlite read-model + tables (Tasks 1–3), ingestion ticker + `orq-lite index` (Tasks 5–6), five query endpoints (Tasks 6–8), `docs/query-api.md` (Task 9), cross-compile constraint (Tasks 1 & 14), `/api/flows` incl. required-input semantics, recursive roles, preflight, empty-file behavior, name ordering (Tasks 10–11), `/api/doctor` incl. shared check functions, 2s budget, 30s cache (Tasks 12–13).
- **Known judgment calls** (surface to the user if they object): (a) serve ingests on-demand with a 1s debounce *plus* a 1s ticker in `Serve` — the ticker satisfies the spec wording, the debounce keeps `Handler()`-only tests fresh; (b) `tasks_done` counts both `task_done` and `task_done_no_commit`; (c) doctor check names adopt the spec's namespaced style (`provider:claude`, `binary:gh`), which slightly changes CLI output labels; (d) corrupt (non-JSON) log lines are skipped, not stored, so `/api/runs/{id}/events` can always return parsed JSON.
- **Type consistency:** `eventDB()`/`writeJSON` defined in Task 6, consumed in 7, 8, 11, 13; `doctor.Run(ctx, dir) []Check` defined in Task 12, consumed in 13; `Flow.ReferencedRoles()`/`InputSpec.HasDefault` defined in Task 10, consumed in 11; `cost.EstimateUSD` defined in Task 4, consumed in eventdb only.
