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
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusDone       Status = "done"
	StatusFailed     Status = "failed"
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

type Task struct {
	ID                   string         `json:"id"`
	Title                string         `json:"title"`
	Description          string         `json:"description"`
	Status               Status         `json:"status"`
	Priority             int            `json:"priority"`
	CreatedInReviewCycle int            `json:"created_in_review_cycle"`
	Attempts             int            `json:"attempts"`
	LastFeedback         *string        `json:"last_feedback"`
	FailureReason        *FailureReason `json:"failure_reason,omitempty"`
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
