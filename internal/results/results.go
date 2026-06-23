package results

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// archiveNameRe matches the cycle/attempt/rerun suffix of an archived result
// file, e.g. "coder.c2.a3.json" or "coder.c2.a3.r2.json".
var archiveNameRe = regexp.MustCompile(`\.c(\d+)\.a(\d+)(?:\.r(\d+))?\.json$`)

// LatestByTask returns the most recent archived result bytes for (taskID, role)
// from the by-task archive, choosing the highest (cycle, attempt, rerun). This
// recovers a finished task's last accepted result — used to show summaries for
// previously implemented tasks. ok is false when no archive exists.
func LatestByTask(dir, taskID, role string) ([]byte, bool) {
	archiveDir := filepath.Join(dir, ".orquestalite", "results", "by-task", taskID)
	entries, err := os.ReadDir(archiveDir)
	if err != nil {
		return nil, false
	}
	prefix := role + "."
	best := ""
	bc, ba, br := -1, -1, -1
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) {
			continue
		}
		m := archiveNameRe.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		c, _ := strconv.Atoi(m[1])
		a, _ := strconv.Atoi(m[2])
		r := 0
		if m[3] != "" {
			r, _ = strconv.Atoi(m[3])
		}
		if c > bc || (c == bc && (a > ba || (a == ba && r > br))) {
			bc, ba, br, best = c, a, r, name
		}
	}
	if best == "" {
		return nil, false
	}
	raw, err := os.ReadFile(filepath.Join(archiveDir, best))
	if err != nil {
		return nil, false
	}
	return raw, true
}

// PlannerFeature is one vertical-slice feature emitted by the factory planner:
// an independently shippable unit cutting through every layer it needs.
type PlannerFeature struct {
	Title              string   `json:"title"`
	Plan               string   `json:"plan"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	FilesLikelyTouched []string `json:"files_likely_touched"`
	// Visual marks a slice with user-facing UI, so the factory runs a
	// browser-driven visual verification pass at feature close.
	Visual bool `json:"visual"`
}

// PlannerResult is the factory planner's output: the vertical slices to queue.
type PlannerResult struct {
	Features       []PlannerFeature `json:"features"`
	NotesForMemory *string          `json:"notes_for_memory"`
}

func (r PlannerResult) MemoryNote() *string { return r.NotesForMemory }

type ParserTask struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    int    `json:"priority"`
}

type ParserResult struct {
	Tasks          []ParserTask `json:"tasks"`
	NotesForMemory *string      `json:"notes_for_memory"`
}

func (r ParserResult) MemoryNote() *string { return r.NotesForMemory }

type CoderResult struct {
	Status         string   `json:"status"`
	Summary        string   `json:"summary"`
	FilesChanged   []string `json:"files_changed"`
	NotesForMemory *string  `json:"notes_for_memory"`
}

func (r CoderResult) MemoryNote() *string { return r.NotesForMemory }

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

func (r TesterResult) MemoryNote() *string { return r.NotesForMemory }

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

func (r CriticResult) MemoryNote() *string { return r.NotesForMemory }

// VerifyCheck is one black-box check the verifier performed against the
// running software (an HTTP call, a CLI invocation, a rendered page, ...).
type VerifyCheck struct {
	Name     string `json:"name"`
	Action   string `json:"action"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Passed   bool   `json:"passed"`
}

type VerifierResult struct {
	Status         string        `json:"status"`
	Checks         []VerifyCheck `json:"checks"`
	NotesForMemory *string       `json:"notes_for_memory"`
}

func (r VerifierResult) MemoryNote() *string { return r.NotesForMemory }

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

func (r ReviewerResult) MemoryNote() *string { return r.NotesForMemory }

// CompactorResult is the memory librarian's output: a rewritten, deduplicated
// project memory that keeps durable facts and drops stale per-task chatter.
type CompactorResult struct {
	Memory    string `json:"memory"`
	KeptNotes int    `json:"kept_notes"`
}

// MemoryNote always returns nil: the compactor must never append a note, which
// would re-grow the memory it just shrank.
func (r CompactorResult) MemoryNote() *string { return nil }

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

// Archive writes an immutable copy of a role's raw result for one run context.
func Archive(dir, role, taskID string, cycle, attempt int, raw []byte) error {
	switch role {
	case "parser":
		taskID = "_plan"
	case "reviewer":
		taskID = "_review"
	}

	basePath := filepath.Join(
		dir,
		".orquestalite",
		"results",
		"by-task",
		taskID,
		fmt.Sprintf("%s.c%d.a%d.json", role, cycle, attempt),
	)
	if err := os.MkdirAll(filepath.Dir(basePath), 0o755); err != nil {
		return fmt.Errorf("create archive dir: %w", err)
	}

	path := basePath
	for rerun := 1; ; rerun++ {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			if _, err := f.Write(raw); err != nil {
				_ = f.Close()
				return fmt.Errorf("write archive %s: %w", path, err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("close archive %s: %w", path, err)
			}
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create archive %s: %w", path, err)
		}
		path = rerunArchivePath(basePath, rerun+1)
	}
}

func rerunArchivePath(basePath string, rerun int) string {
	ext := filepath.Ext(basePath)
	stem := basePath[:len(basePath)-len(ext)]
	return fmt.Sprintf("%s.r%d%s", stem, rerun, ext)
}

func ParseParser(path string) (*ParserResult, error) {
	var r ParserResult
	if err := read(path, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func ParsePlanner(path string) (*PlannerResult, error) {
	var r PlannerResult
	if err := read(path, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func ParseCompactor(path string) (*CompactorResult, error) {
	var r CompactorResult
	if err := read(path, &r); err != nil {
		return nil, err
	}
	if strings.TrimSpace(r.Memory) == "" {
		return nil, fmt.Errorf("compactor.memory required")
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

func ParseVerifier(path string) (*VerifierResult, error) {
	var r VerifierResult
	if err := read(path, &r); err != nil {
		return nil, err
	}
	if r.Status != "pass" && r.Status != "fail" {
		return nil, fmt.Errorf("verifier.status %q invalid", r.Status)
	}
	if len(r.Checks) == 0 {
		return nil, fmt.Errorf("verifier.checks required: at least one black-box check must be performed")
	}
	if r.Status == "fail" {
		anyFailed := false
		for _, c := range r.Checks {
			if !c.Passed {
				anyFailed = true
				break
			}
		}
		if !anyFailed {
			return nil, fmt.Errorf("verifier.status is fail but no check has passed=false")
		}
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
