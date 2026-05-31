package eventlog

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
