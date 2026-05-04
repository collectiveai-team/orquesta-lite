package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lionelchamorro/orquesta-lite/internal/results"
	"github.com/lionelchamorro/orquesta-lite/internal/tasks"
)

type stubParserCaller struct {
	out results.ParserResult
}

func (s *stubParserCaller) RunParser(ctx context.Context, plan string) (*results.ParserResult, error) {
	return &s.out, nil
}

func TestPlan_WritesTasksJSON(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.md")
	_ = os.WriteFile(planPath, []byte("# build x\nadd login flow"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, ".pyorquesta"), 0o755)

	stub := &stubParserCaller{out: results.ParserResult{Tasks: []results.ParserTask{
		{Title: "scaffold", Description: "set up repo", Priority: 1},
		{Title: "add login route", Description: "POST /login", Priority: 2},
	}}}

	if err := Plan(context.Background(), dir, planPath, false, stub); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, ".pyorquesta", "tasks.json"))
	var tl tasks.TaskList
	if err := json.Unmarshal(raw, &tl); err != nil {
		t.Fatal(err)
	}
	if len(tl.Tasks) != 2 || tl.Tasks[0].ID != "T001" || tl.Tasks[1].ID != "T002" {
		t.Fatalf("got tasks: %+v", tl.Tasks)
	}
	if tl.Tasks[0].Status != tasks.StatusPending {
		t.Errorf("status not initialised")
	}
}

func TestPlan_AppendPreservesExisting(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".pyorquesta"), 0o755)
	prev := &tasks.TaskList{Tasks: []tasks.Task{{ID: "T001", Title: "old", Status: tasks.StatusDone}}}
	raw, _ := json.MarshalIndent(prev, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, ".pyorquesta", "tasks.json"), raw, 0o644)

	planPath := filepath.Join(dir, "plan.md")
	_ = os.WriteFile(planPath, []byte("more"), 0o644)
	stub := &stubParserCaller{out: results.ParserResult{Tasks: []results.ParserTask{{Title: "new", Description: "y", Priority: 1}}}}

	if err := Plan(context.Background(), dir, planPath, true, stub); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(filepath.Join(dir, ".pyorquesta", "tasks.json"))
	var tl tasks.TaskList
	_ = json.Unmarshal(out, &tl)
	if len(tl.Tasks) != 2 || tl.Tasks[1].ID != "T002" {
		t.Fatalf("append failed: %+v", tl.Tasks)
	}
}
