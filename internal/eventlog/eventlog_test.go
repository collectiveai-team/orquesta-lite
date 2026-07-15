package eventlog

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetRunID_StampsEvents verifies that once SetRunID is called, every
// subsequent Log() persists run_id into the JSONL record (alongside ts/event).
func TestSetRunID_StampsEvents(t *testing.T) {
	dir := t.TempDir()
	pretty := &bytes.Buffer{}
	l, err := Open(filepath.Join(dir, "run.log"), pretty)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	l.SetRunID("r20260701T193000Z-4f2a")
	l.Log(Event{Type: "task_start", Fields: map[string]any{"task_id": "T003"}})

	raw, _ := os.ReadFile(filepath.Join(dir, "run.log"))
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &got); err != nil {
		t.Fatalf("invalid JSONL: %v\n%s", err, raw)
	}
	if got["run_id"] != "r20260701T193000Z-4f2a" {
		t.Errorf("run_id not stamped: %v\n%s", got["run_id"], raw)
	}
	if got["event"] != "task_start" {
		t.Errorf("event field clobbered: %v\n%s", got["event"], raw)
	}
}

func TestLog_WritesJSONLAndPretty(t *testing.T) {
	dir := t.TempDir()
	pretty := &bytes.Buffer{}
	l, err := Open(filepath.Join(dir, "run.log"), pretty)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	l.Log(Event{Type: "task_start", Fields: map[string]any{"task_id": "T003", "priority": 2}})

	raw, _ := os.ReadFile(filepath.Join(dir, "run.log"))
	if !strings.Contains(string(raw), `"event":"task_start"`) {
		t.Errorf("jsonl missing event: %s", raw)
	}
	if !strings.Contains(string(raw), `"task_id":"T003"`) {
		t.Errorf("jsonl missing field: %s", raw)
	}

	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &got); err != nil {
		t.Fatalf("invalid JSONL: %v", err)
	}

	if !strings.Contains(pretty.String(), "task_start") {
		t.Errorf("pretty stdout missing event: %q", pretty.String())
	}
}

func TestLog_PrettyOutputSanitisesLongMultilineFields(t *testing.T) {
	dir := t.TempDir()
	pretty := &bytes.Buffer{}
	l, err := Open(filepath.Join(dir, "run.log"), pretty)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	long := "first line\n" + strings.Repeat("x", 400)
	l.Log(Event{Type: "agent_run", Fields: map[string]any{"stdout_tail": long}})

	out := pretty.String()
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("pretty output should only contain the trailing log newline, got %q", out)
	}
	if !strings.Contains(out, `\n`) {
		t.Fatalf("pretty output should include escaped newline marker, got %q", out)
	}
	if len(out) > 380 {
		t.Fatalf("pretty output too long: len=%d out=%q", len(out), out)
	}
}

func TestRotateAtThreshold(t *testing.T) {
	dir := t.TempDir()
	pretty := &bytes.Buffer{}
	l, _ := Open(filepath.Join(dir, "run.log"), pretty)
	l.RotateBytes = 200 // tiny threshold for test
	defer l.Close()

	for i := 0; i < 50; i++ {
		l.Log(Event{Type: "noop", Fields: map[string]any{"i": i, "padding": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}})
	}

	entries, _ := os.ReadDir(dir)
	rotated := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "run-") && strings.HasSuffix(e.Name(), ".log.gz") {
			rotated++
		}
	}
	if rotated < 1 {
		t.Errorf("expected at least one rotated file, found 0")
	}
}

// TestHumanFormat_SummarisesAgentRun verifies that FormatHuman produces a
// one-line "role/agent → outcome (Ns)" headline on stdout, followed by an
// indented preview of what the agent produced, while the JSONL file still
// contains the full structured fields.
func TestHumanFormat_SummarisesAgentRun(t *testing.T) {
	dir := t.TempDir()
	pretty := &bytes.Buffer{}
	l, err := OpenWithFormat(filepath.Join(dir, "run.log"), pretty, FormatHuman)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// final_text_tail present -> it is preferred for the preview.
	l.Log(Event{Type: "agent_run", Fields: map[string]any{
		"role":            "coder",
		"agent":           "claude_sonnet",
		"duration_s":      12,
		"fallback_reason": "",
		"final_text_tail": "Implemented T001.\nlots of detail we don't want on the headline",
		"stdout_tail":     "raw stdout we ignore when final_text is present",
	}})
	// No final_text -> fall back to stdout_tail for the preview.
	l.Log(Event{Type: "agent_run", Fields: map[string]any{
		"role":            "parser",
		"agent":           "claude_opus",
		"duration_s":      36,
		"fallback_reason": "",
		"stdout_tail":     "Parsed the plan into 11 atomic tasks.\nmore noise below",
	}})
	// Failure with nothing to preview -> headline only.
	l.Log(Event{Type: "agent_run", Fields: map[string]any{
		"role":            "coder",
		"agent":           "codex_gpt5",
		"duration_s":      1,
		"fallback_reason": "result_missing",
	}})

	out := pretty.String()
	if !strings.Contains(out, "coder/claude_sonnet → ok (12s)") {
		t.Errorf("human pretty output missing success headline:\n%s", out)
	}
	if !strings.Contains(out, "↳ Implemented T001.") {
		t.Errorf("human pretty output missing final_text preview:\n%s", out)
	}
	if !strings.Contains(out, "↳ Parsed the plan into 11 atomic tasks.") {
		t.Errorf("human pretty output missing stdout fallback preview:\n%s", out)
	}
	if !strings.Contains(out, "coder/codex_gpt5 → no-result (1s)") {
		t.Errorf("human pretty output missing failure headline:\n%s", out)
	}
	// Only the first line of each tail is previewed; trailing detail is dropped.
	if strings.Contains(out, "lots of detail") || strings.Contains(out, "more noise below") {
		t.Errorf("human preview must show first line only, got:\n%s", out)
	}
	// Field keys are never printed in human mode.
	if strings.Contains(out, "stdout_tail") || strings.Contains(out, "final_text_tail") {
		t.Errorf("human pretty output must not print field keys, got:\n%s", out)
	}
}

// TestVerboseFormat_StableOrderAndElidesEmpty verifies the verbose renderer
// produces a deterministic key order (role/agent first, tails last) and omits
// empty-string fields, while keeping zero/false scalars for pipe consumers.
func TestVerboseFormat_StableOrderAndElidesEmpty(t *testing.T) {
	dir := t.TempDir()
	pretty := &bytes.Buffer{}
	l, err := OpenWithFormat(filepath.Join(dir, "run.log"), pretty, FormatVerbose)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	l.Log(Event{Type: "agent_run", Fields: map[string]any{
		"agent":           "claude_opus",
		"role":            "parser",
		"model":           "", // empty string -> elided
		"provider":        "",
		"session_id":      "",
		"exit_code":       0,     // zero scalar -> kept
		"timed_out":       false, // false scalar -> kept
		"duration_s":      36,
		"stdout_tail":     "summary text",
		"fallback_reason": "",
	}})

	out := pretty.String()
	if strings.Contains(out, "model=") || strings.Contains(out, "session_id=") || strings.Contains(out, "provider=") {
		t.Errorf("verbose output should elide empty-string fields, got:\n%s", out)
	}
	if !strings.Contains(out, "exit_code=0") || !strings.Contains(out, "timed_out=false") {
		t.Errorf("verbose output should keep zero/false scalars, got:\n%s", out)
	}
	// role and agent come first; stdout_tail comes last.
	roleIdx := strings.Index(out, "role=")
	agentIdx := strings.Index(out, "agent=")
	durIdx := strings.Index(out, "duration_s=")
	tailIdx := strings.Index(out, "stdout_tail=")
	if !(roleIdx < agentIdx && agentIdx < durIdx && durIdx < tailIdx) {
		t.Errorf("verbose field order unexpected (want role<agent<duration_s<stdout_tail):\n%s", out)
	}
}

func TestAppendPreservesOutboxEnvelope(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.log")
	logger, err := Open(path, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"event_id":"e1","ts":"2026-07-14T00:00:00Z","event":"workflow_started","run_id":"r1"}`)
	if err := logger.Append(raw); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw)+"\n" {
		t.Fatalf("got %q", got)
	}
}
