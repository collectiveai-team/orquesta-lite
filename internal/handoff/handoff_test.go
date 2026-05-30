package handoff

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

func makeFullTask() *tasks.Task {
	reason := tasks.ReasonAgentRepeatedFail
	return &tasks.Task{
		ID:                   "T042",
		Title:                "implement feature X",
		Description:          "Add feature X to the system with acceptance criteria A and B.",
		Status:               tasks.StatusNeedsHuman,
		Priority:             1,
		CreatedInReviewCycle: 2,
		Attempts:             3,
		FailureReason:        &reason,
		FailureDetails: &tasks.FailureDetails{
			Reason:      tasks.ReasonAgentRepeatedFail,
			TaskSuspect: true,
			ModelSuspect: false,
			ConfigSuspect: false,
			LastStderrTail: "Error: sandbox violation\nexiting",
			AgentChain: []tasks.AgentRun{
				{Agent: "claude_sonnet", Duration: 42, Status: "result_missing"},
				{Agent: "codex_gpt5_5", Duration: 71, Status: "completed"},
			},
		},
	}
}

// TestWrite_FullPath asserts that a handoff file is written with all sections
// populated when FailureDetails is fully set.
func TestWrite_FullPath(t *testing.T) {
	dir := t.TempDir()
	task := makeFullTask()

	path, err := Write(dir, task)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	wantPath := filepath.Join(dir, "tasks", "handoff-T042.md")
	if path != wantPath {
		t.Errorf("returned path = %q, want %q", path, wantPath)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	s := string(content)

	for _, want := range []string{
		"T042",
		"implement feature X",
		"agent_repeated_failure",
		"claude_sonnet",
		"codex_gpt5_5",
		"result_missing",
		"42s",
		"71s",
		"sandbox violation",
		"Task suspect: yes",
		"Config suspect: no",
		"Model suspect: no",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("handoff file missing %q\n--- content ---\n%s", want, s)
		}
	}
}

// TestWrite_NilFailureDetails asserts that Write does not panic and still
// writes a minimal file when FailureDetails is nil.
func TestWrite_NilFailureDetails(t *testing.T) {
	dir := t.TempDir()
	task := &tasks.Task{
		ID:          "T001",
		Title:       "a simple task",
		Description: "do something",
		Status:      tasks.StatusNeedsHuman,
	}

	path, err := Write(dir, task)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	s := string(content)

	if !strings.Contains(s, "T001") {
		t.Errorf("file missing task ID; content:\n%s", s)
	}
	if !strings.Contains(s, "a simple task") {
		t.Errorf("file missing title; content:\n%s", s)
	}
}

// TestWrite_EmptyAgentChain asserts that an empty AgentChain renders the
// "(no agent run data captured)" placeholder instead of an empty table.
func TestWrite_EmptyAgentChain(t *testing.T) {
	dir := t.TempDir()
	reason := tasks.ReasonAgentRepeatedFail
	task := &tasks.Task{
		ID:            "T002",
		Title:         "task with no chain",
		Description:   "desc",
		Status:        tasks.StatusNeedsHuman,
		FailureReason: &reason,
		FailureDetails: &tasks.FailureDetails{
			Reason:      tasks.ReasonAgentRepeatedFail,
			TaskSuspect: true,
			AgentChain:  nil,
		},
	}

	path, err := Write(dir, task)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	s := string(content)

	if !strings.Contains(s, "(no agent run data captured)") {
		t.Errorf("expected placeholder for empty agent chain; content:\n%s", s)
	}
}

// TestWrite_CreatesTasksDirIfMissing asserts that Write creates the tasks/
// subdirectory if it does not exist.
func TestWrite_CreatesTasksDirIfMissing(t *testing.T) {
	dir := t.TempDir()
	// Verify tasks/ does not exist yet.
	tasksDir := filepath.Join(dir, "tasks")
	if _, err := os.Stat(tasksDir); !os.IsNotExist(err) {
		t.Fatalf("tasks/ should not exist before Write; stat err: %v", err)
	}

	task := &tasks.Task{
		ID:    "T003",
		Title: "orphan task",
	}

	if _, err := Write(dir, task); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if _, err := os.Stat(tasksDir); err != nil {
		t.Errorf("tasks/ not created: %v", err)
	}
}
