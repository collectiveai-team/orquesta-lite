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

	"github.com/lionelchamorro/orquestalite/internal/workflow"
)

func createStatusRun(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".orquestalite"), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := workflow.Open(filepath.Join(dir, ".orquestalite", "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, err = store.CreateRunOnce(context.Background(), workflow.CreateRunParams{
		ID: "run-123", FlowRef: "development/task-list@1", DefinitionHash: "abc",
		IR: json.RawMessage(`{}`), Inputs: json.RawMessage(`{}`), Policy: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStatusPrintsDurableWorkflowRuns(t *testing.T) {
	dir := t.TempDir()
	createStatusRun(t, dir)
	var out bytes.Buffer
	if err := Status(dir, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"run-123", "running", "development/task-list@1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status output missing %q: %s", want, out.String())
		}
	}
}

func TestStatusHandlesMissingWorkflowDatabase(t *testing.T) {
	var out bytes.Buffer
	if err := Status(t.TempDir(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no workflow runs") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestStatusWatchRendersAndStops(t *testing.T) {
	dir := t.TempDir()
	createStatusRun(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	if err := StatusWatch(ctx, dir, &out, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "run-123") < 2 || !strings.Contains(out.String(), "\x1b[2J") {
		t.Fatalf("watch output = %q", out.String())
	}
}
