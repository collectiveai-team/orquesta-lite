package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lionelchamorro/orquesta-lite/internal/tasks"
)

func TestStatus_PrintsTable(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".pyorquesta"), 0o755)
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Title: "first", Status: tasks.StatusDone, Priority: 1},
		{ID: "T002", Title: "second", Status: tasks.StatusPending, Priority: 2},
	}}
	raw, _ := json.MarshalIndent(tl, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, ".pyorquesta", "tasks.json"), raw, 0o644)

	buf := &bytes.Buffer{}
	if err := Status(dir, buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"T001", "T002", "done", "pending", "first", "second"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q: %s", want, out)
		}
	}
}

func TestStatus_HandlesMissingTasksFile(t *testing.T) {
	dir := t.TempDir()
	buf := &bytes.Buffer{}
	if err := Status(dir, buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no tasks") {
		t.Errorf("expected 'no tasks' message, got: %s", buf.String())
	}
}

func TestStatusWatch_RendersMultipleTimesAndStopsOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".pyorquesta"), 0o755)
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Title: "watched", Status: tasks.StatusPending, Priority: 1},
	}}
	raw, _ := json.MarshalIndent(tl, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, ".pyorquesta", "tasks.json"), raw, 0o644)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	buf := &bytes.Buffer{}
	if err := StatusWatch(ctx, dir, buf, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	if strings.Count(out, "watched") < 2 {
		t.Errorf("expected at least 2 renders of the table, got: %d\n%s", strings.Count(out, "watched"), out)
	}
	if !strings.Contains(out, "\x1b[2J") {
		t.Errorf("expected ANSI clear-screen sequence in output")
	}
	if !strings.Contains(out, "Ctrl+C to stop") {
		t.Errorf("expected footer hint in output")
	}
}

func TestStatusWatch_DefaultsIntervalWhenZero(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	buf := &bytes.Buffer{}
	// interval=0 should default to 1s; with a 5ms ctx we'll only see the initial render.
	if err := StatusWatch(ctx, dir, buf, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no tasks") {
		t.Errorf("expected initial render even with zero interval, got: %s", buf.String())
	}
}
