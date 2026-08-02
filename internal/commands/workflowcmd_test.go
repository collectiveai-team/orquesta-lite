package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/workflow"
)

func writeV2CommandFlow(t *testing.T, dir string) string {
	t.Helper()
	flowsDir := filepath.Join(dir, "flows")
	if err := os.MkdirAll(flowsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(flowsDir, "demo.json")
	raw := `{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"demo","version":"1"},"steps":[{"id":"run","uses":"activity:command.run@1","with":{"argv":["/usr/bin/true"]}}],"outputs":{"exit":{"$ref":"steps.run.output.exitCode"}}}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFlowCLIV2ValidateInspectRunStatus(t *testing.T) {
	dir := t.TempDir()
	path := writeV2CommandFlow(t, dir)
	var out bytes.Buffer
	if err := FlowCLI(context.Background(), dir, []string{"validate", path}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "valid demo@1") {
		t.Fatalf("out=%s", out.String())
	}
	out.Reset()
	if err := FlowCLI(context.Background(), dir, []string{"run", path}, &out); err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(out.String())
	var runID string
	for _, field := range fields {
		if strings.HasPrefix(field, "run_id=") {
			runID = strings.TrimPrefix(field, "run_id=")
		}
	}
	if runID == "" {
		t.Fatalf("out=%s", out.String())
	}
	store, err := workflow.Open(filepath.Join(dir, ".orquestalite", "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	run, err := store.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflow.RunSucceeded {
		t.Fatalf("status=%s", run.Status)
	}
}

func TestFlowCLIExecutesInstalledPinnedPack(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join("..", "flow", "testdata", "packs", "development-fixture")
	target := filepath.Join(dir, ".orquestalite", "packs", "development-fixture", "1")
	for _, relative := range []string{"pack.json", "flows/neutral-pipeline@1.json", "schemas/message@1.json"} {
		raw, err := os.ReadFile(filepath.Join(source, relative))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(target, relative)
		if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := FlowCLI(context.Background(), dir, []string{"run", "development-fixture/neutral-pipeline@1", `message="hello"`}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "pack=development-fixture@1:") || !strings.Contains(out.String(), "status=succeeded") {
		t.Fatalf("out=%s", out.String())
	}
	store, err := workflow.Open(filepath.Join(dir, ".orquestalite", "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runs, err := store.ListRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 || !strings.Contains(string(runs[0].IR), `"pack"`) {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
}

// An unpinned pack ref no longer looks for a directory named after the flow's
// version, so the error names the pack without inventing a version it never
// searched for.
func TestFlowCLIMissingPackHasActionableError(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	err := FlowCLI(context.Background(), dir, []string{"run", "development/pr-review@1"}, &out)
	if err == nil || !strings.Contains(err.Error(), "installed pack development is required for flow pr-review") {
		t.Fatalf("err=%v", err)
	}
}

func TestFlowCLIListIgnoresLegacyFlowsJSON(t *testing.T) {
	dir := t.TempDir()
	legacy := []byte(`{"flows":{"legacy-only":{"steps":[]}}}`)
	if err := os.WriteFile(filepath.Join(dir, "flows.json"), legacy, 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := FlowCLI(context.Background(), dir, []string{"list"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "legacy-only") {
		t.Fatalf("flow list exposed legacy flows.json entry:\n%s", out.String())
	}
}
