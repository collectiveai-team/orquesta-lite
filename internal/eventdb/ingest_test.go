package eventdb

import (
	"compress/gzip"
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
