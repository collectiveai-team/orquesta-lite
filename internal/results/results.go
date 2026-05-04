package results

import (
	"encoding/json"
	"fmt"
	"os"
)

type ParserTask struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    int    `json:"priority"`
}

type ParserResult struct {
	Tasks          []ParserTask `json:"tasks"`
	NotesForMemory *string      `json:"notes_for_memory"`
}

type CoderResult struct {
	Status         string   `json:"status"`
	Summary        string   `json:"summary"`
	FilesChanged   []string `json:"files_changed"`
	NotesForMemory *string  `json:"notes_for_memory"`
}

type TestFailure struct {
	Test    string `json:"test"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

type TesterResult struct {
	Status         string        `json:"status"`
	CommandRun     string        `json:"command_run"`
	Failures       []TestFailure `json:"failures"`
	NotesForMemory *string       `json:"notes_for_memory"`
}

type Concern struct {
	Severity   string `json:"severity"`
	Where      string `json:"where"`
	Issue      string `json:"issue"`
	Suggestion string `json:"suggestion"`
}

type CriticResult struct {
	Status         string    `json:"status"`
	Concerns       []Concern `json:"concerns"`
	NotesForMemory *string   `json:"notes_for_memory"`
}

type ReviewerNewTask struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    int    `json:"priority"`
}

type ReviewerResult struct {
	SummaryOfCycle string            `json:"summary_of_cycle"`
	NewTasks       []ReviewerNewTask `json:"new_tasks"`
	ShouldStop     *bool             `json:"should_stop"`
	NotesForMemory *string           `json:"notes_for_memory"`
}

func read(path string, into any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func ParseParser(path string) (*ParserResult, error) {
	var r ParserResult
	if err := read(path, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func ParseCoder(path string) (*CoderResult, error) {
	var r CoderResult
	if err := read(path, &r); err != nil {
		return nil, err
	}
	if r.Status != "completed" && r.Status != "blocked" {
		return nil, fmt.Errorf("coder.status %q invalid", r.Status)
	}
	return &r, nil
}

func ParseTester(path string) (*TesterResult, error) {
	var r TesterResult
	if err := read(path, &r); err != nil {
		return nil, err
	}
	if r.Status != "pass" && r.Status != "fail" {
		return nil, fmt.Errorf("tester.status %q invalid", r.Status)
	}
	if r.CommandRun == "" {
		return nil, fmt.Errorf("tester.command_run required")
	}
	return &r, nil
}

func ParseCritic(path string) (*CriticResult, error) {
	var r CriticResult
	if err := read(path, &r); err != nil {
		return nil, err
	}
	if r.Status != "approved" && r.Status != "rejected" {
		return nil, fmt.Errorf("critic.status %q invalid", r.Status)
	}
	if r.Status == "rejected" && len(r.Concerns) == 0 {
		return nil, fmt.Errorf("rejected critic must list concerns")
	}
	return &r, nil
}

func ParseReviewer(path string) (*ReviewerResult, error) {
	var r ReviewerResult
	if err := read(path, &r); err != nil {
		return nil, err
	}
	if r.ShouldStop == nil {
		return nil, fmt.Errorf("reviewer.should_stop required")
	}
	return &r, nil
}
