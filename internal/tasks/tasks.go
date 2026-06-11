package tasks

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

type Status string

const (
	StatusPending            Status = "pending"
	StatusInProgress         Status = "in_progress"
	StatusDone               Status = "done"
	StatusFailed             Status = "failed"
	StatusDecomposed         Status = "decomposed"
	StatusNeedsHuman         Status = "needs_human"
	StatusNeedsClarification Status = "needs_clarification"
)

type FailureReason string

const (
	ReasonMaxIterations      FailureReason = "max_iterations"
	ReasonAgentRepeatedFail  FailureReason = "agent_repeated_failure"
	ReasonRateLimitExhausted FailureReason = "rate_limit_exhausted"
	ReasonCommitRejected     FailureReason = "commit_rejected"
	ReasonFullSuiteFailed    FailureReason = "full_suite_failed"
	ReasonAgentCrashed       FailureReason = "agent_crashed"
	ReasonInvalidResultJSON  FailureReason = "invalid_result_json"
)

// AgentRun captures a single agent execution within a fix attempt.
type AgentRun struct {
	Agent    string `json:"agent"`
	Duration int    `json:"duration_s"`
	// Status is one of: "completed", "result_missing", "agent_crashed", "rate_limit", "all_agents_failed"
	Status string `json:"status"`
	Stderr string `json:"stderr_tail,omitempty"`
}

// FailureDetails holds richer context about why a task failed, including
// hypothesis booleans to guide human triage.
type FailureDetails struct {
	Reason         FailureReason `json:"reason"`
	AgentChain     []AgentRun    `json:"agent_chain,omitempty"`
	ConfigSuspect  bool          `json:"config_suspect"`
	ModelSuspect   bool          `json:"model_suspect"`
	TaskSuspect    bool          `json:"task_suspect"`
	LastStderrTail string        `json:"last_stderr_tail,omitempty"`
	HandoffPath    string        `json:"handoff_path,omitempty"`
}

// VerifyState reports the verification outcome of a task independently of its
// work status. A task can be "done" (work completed) while VerifyState is
// "commit_skipped" (e.g. no git repo) or "tests_failed". Splitting these makes
// `orq-lite status` honest: shipped-but-flagged is no longer rendered as a
// hard failure.
type VerifyState string

const (
	VerifyUnknown        VerifyState = ""
	VerifyTestsPass      VerifyState = "tests_pass"
	VerifyTestsFail      VerifyState = "tests_fail"
	VerifyTestsSkipped   VerifyState = "tests_skipped"
	VerifyCommitOK       VerifyState = "commit_ok"
	VerifyCommitSkipped  VerifyState = "commit_skipped"
	VerifyCommitRejected VerifyState = "commit_rejected"
)

type Task struct {
	ID                   string          `json:"id"`
	Title                string          `json:"title"`
	Description          string          `json:"description"`
	Status               Status          `json:"status"`
	Priority             int             `json:"priority"`
	CreatedInReviewCycle int             `json:"created_in_review_cycle"`
	Attempts             int             `json:"attempts"`
	LastFeedback         *string         `json:"last_feedback"`
	FailureReason        *FailureReason  `json:"failure_reason,omitempty"`
	DecomposedIntoIDs    []string        `json:"decomposed_into_ids,omitempty"`
	FailureDetails       *FailureDetails `json:"failure_details,omitempty"`
	// VerifyState captures the verification outcome (tests + commit) separately
	// from work status. Empty for tasks that never reached the verify phase.
	VerifyState VerifyState `json:"verify_state,omitempty"`
	// LastAgent records which agent produced the most recent accepted result
	// (or attempted the most recent failed call) for this task. Useful for
	// debugging which model worked or didn't.
	LastAgent string `json:"last_agent,omitempty"`
	// DecompositionDepth counts how many decomposition generations produced
	// this task (0 = from the plan or reviewer). Caps recursive decomposition.
	DecompositionDepth int `json:"decomposition_depth,omitempty"`
}

type TaskList struct {
	Tasks []Task `json:"tasks"`
}

func Load(path string) (*TaskList, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read tasks.json: %w", err)
	}
	var tl TaskList
	if err := json.Unmarshal(raw, &tl); err != nil {
		return nil, fmt.Errorf("parse tasks.json: %w", err)
	}
	return &tl, nil
}

func Save(path string, tl *TaskList) error {
	raw, err := json.MarshalIndent(tl, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func (tl *TaskList) AnyPending() bool {
	for _, t := range tl.Tasks {
		if t.Status == StatusPending {
			return true
		}
	}
	return false
}

func (tl *TaskList) NextPending() *Task {
	pending := []int{}
	for i, t := range tl.Tasks {
		if t.Status == StatusPending {
			pending = append(pending, i)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	sort.SliceStable(pending, func(a, b int) bool {
		return tl.Tasks[pending[a]].Priority < tl.Tasks[pending[b]].Priority
	})
	return &tl.Tasks[pending[0]]
}

func (tl *TaskList) NextID() string {
	max := 0
	for _, t := range tl.Tasks {
		n, err := strconv.Atoi(strings.TrimPrefix(t.ID, "T"))
		if err == nil && n > max {
			max = n
		}
	}
	return fmt.Sprintf("T%03d", max+1)
}

func (tl *TaskList) Append(newTasks []Task, cycle int) []Task {
	added := make([]Task, 0, len(newTasks))
	for _, t := range newTasks {
		t.ID = tl.NextID()
		t.Status = StatusPending
		t.CreatedInReviewCycle = cycle
		t.Attempts = 0
		tl.Tasks = append(tl.Tasks, t)
		added = append(added, t)
	}
	return added
}
