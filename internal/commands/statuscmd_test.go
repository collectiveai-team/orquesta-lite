package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lionelchamorro/pyorquesta/internal/tasks"
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
