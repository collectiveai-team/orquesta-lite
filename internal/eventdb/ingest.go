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
		EventID string `json:"event_id"`
		Ts      string `json:"ts"`
		Event   string `json:"event"`
		RunID   string `json:"run_id"`
		TaskID  string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(line), &env); err != nil {
		return nil // corrupt line (torn write): skip rather than poison ingest
	}
	var eventID any
	if env.EventID != "" {
		eventID = env.EventID
	}
	result, err := tx.Exec(`INSERT OR IGNORE INTO events(event_id, run_id, ts, type, task_id, raw) VALUES(?,?,?,?,?,?)`,
		eventID, env.RunID, env.Ts, env.Event, env.TaskID, line)
	if err != nil {
		return err
	}
	if env.EventID != "" {
		if inserted, _ := result.RowsAffected(); inserted == 0 {
			return nil
		}
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
