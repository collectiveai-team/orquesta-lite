package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRunLog(t *testing.T, dir string, lines ...string) {
	t.Helper()
	logDir := filepath.Join(dir, ".orquestalite")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(logDir, "run.log"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const (
	logParser = `{"ts":"2026-06-03T17:00:57Z","event":"agent_run","role":"parser","agent":"claude_opus","duration_s":36,"final_text_tail":"","stdout_tail":"Parsed the plan into 11 atomic tasks.\nmore detail"}`
	logPlan   = `{"ts":"2026-06-03T17:00:57Z","event":"plan_written","tasks_count":11,"path":".orquestalite/tasks.json"}`
	logCoder  = `{"ts":"2026-06-03T17:02:56Z","event":"agent_run","role":"coder","agent":"codex_gpt5_5","duration_s":102,"final_text_tail":"Implemented T001.","stdout_tail":"raw"}`
)

func TestLog_RendersTimeline(t *testing.T) {
	dir := t.TempDir()
	writeRunLog(t, dir, logParser, logPlan, logCoder)

	var buf bytes.Buffer
	if err := Log(dir, &buf, LogViewOptions{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	for _, want := range []string{
		"#1", "#2", "#3",
		"parser/claude_opus → ok (36s)",
		"↳ Parsed the plan into 11 atomic tasks.",
		"plan_written tasks=11",
		"coder/codex_gpt5_5 → ok (102s)",
		"↳ Implemented T001.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("timeline missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "more detail") {
		t.Errorf("compact view should preview first line only, got:\n%s", out)
	}
}

func TestLog_RoleFilter(t *testing.T) {
	dir := t.TempDir()
	writeRunLog(t, dir, logParser, logPlan, logCoder)

	var buf bytes.Buffer
	if err := Log(dir, &buf, LogViewOptions{Role: "coder"}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "coder/codex_gpt5_5") {
		t.Errorf("role filter dropped the matching event:\n%s", out)
	}
	if strings.Contains(out, "parser/claude_opus") {
		t.Errorf("role filter leaked another role:\n%s", out)
	}
	if strings.Contains(out, "plan_written") {
		t.Errorf("role filter should drop non-agent_run events:\n%s", out)
	}
}

func TestLog_EventFilter(t *testing.T) {
	dir := t.TempDir()
	writeRunLog(t, dir, logParser, logPlan, logCoder)

	var buf bytes.Buffer
	if err := Log(dir, &buf, LogViewOptions{Event: "plan_written"}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "plan_written") {
		t.Errorf("event filter dropped the matching event:\n%s", out)
	}
	if strings.Contains(out, "claude_opus") || strings.Contains(out, "codex_gpt5_5") {
		t.Errorf("event filter leaked agent_run events:\n%s", out)
	}
}

func TestLog_Expand(t *testing.T) {
	dir := t.TempDir()
	writeRunLog(t, dir, logParser, logPlan, logCoder)

	var buf bytes.Buffer
	if err := Log(dir, &buf, LogViewOptions{Expand: 1}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	// Expand prints raw field keys and the full multi-line tail.
	if !strings.Contains(out, "stdout_tail") {
		t.Errorf("expand should print full field keys, got:\n%s", out)
	}
	if !strings.Contains(out, "more detail") {
		t.Errorf("expand should print the full multi-line tail, got:\n%s", out)
	}
}

func TestLog_ExpandOutOfRange(t *testing.T) {
	dir := t.TempDir()
	writeRunLog(t, dir, logParser)

	var buf bytes.Buffer
	if err := Log(dir, &buf, LogViewOptions{Expand: 9}); err == nil {
		t.Errorf("expected error for out-of-range expand, got output:\n%s", buf.String())
	}
}

func TestLog_NoFileIsFriendly(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := Log(dir, &buf, LogViewOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no run.log") {
		t.Errorf("want friendly message, got %q", buf.String())
	}
}
