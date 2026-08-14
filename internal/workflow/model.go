// Package workflow provides the durable state machine and scheduler for
// compiled flow IR.
package workflow

import (
	"encoding/json"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
)

type RunStatus string

const (
	RunPending    RunStatus = "pending"
	RunRunning    RunStatus = "running"
	RunSucceeded  RunStatus = "succeeded"
	RunFailed     RunStatus = "failed"
	RunCancelled  RunStatus = "cancelled"
	RunNeedsHuman RunStatus = "needs_human"
)

type StepStatus string

const (
	StepPending        StepStatus = "pending"
	StepReady          StepStatus = "ready"
	StepRunning        StepStatus = "running"
	StepSucceeded      StepStatus = "succeeded"
	StepFailed         StepStatus = "failed"
	StepSkipped        StepStatus = "skipped"
	StepOutcomeUnknown StepStatus = "outcome_unknown"
	StepNeedsHuman     StepStatus = "needs_human"
)

type AttemptStatus string

const (
	AttemptScheduled      AttemptStatus = "scheduled"
	AttemptRunning        AttemptStatus = "running"
	AttemptSucceeded      AttemptStatus = "succeeded"
	AttemptFailed         AttemptStatus = "failed"
	AttemptOutcomeUnknown AttemptStatus = "outcome_unknown"
)

type Run struct {
	ID             string          `json:"id"`
	SourceKey      string          `json:"sourceKey,omitempty"`
	FlowRef        string          `json:"flowRef"`
	DefinitionHash string          `json:"definitionHash"`
	IR             json.RawMessage `json:"ir"`
	Inputs         json.RawMessage `json:"inputs"`
	Policy         json.RawMessage `json:"policy"`
	Status         RunStatus       `json:"status"`
	Error          string          `json:"error,omitempty"`
	StartedAt      time.Time       `json:"startedAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
	FinishedAt     *time.Time      `json:"finishedAt,omitempty"`
}

type StepRun struct {
	ID          int64               `json:"id"`
	RunID       string              `json:"runId"`
	ScopePath   string              `json:"scopePath"`
	StepID      string              `json:"stepId"`
	ForeachKey  string              `json:"foreachKey,omitempty"`
	ActivityRef string              `json:"activityRef,omitempty"`
	Inputs      json.RawMessage     `json:"inputs,omitempty"`
	Output      json.RawMessage     `json:"output,omitempty"`
	Status      StepStatus          `json:"status"`
	ErrorClass  activity.ErrorClass `json:"errorClass,omitempty"`
	Error       string              `json:"error,omitempty"`
	CreatedAt   time.Time           `json:"createdAt"`
	UpdatedAt   time.Time           `json:"updatedAt"`
}

type Attempt struct {
	ID             int64               `json:"id"`
	StepRunID      int64               `json:"stepRunId"`
	Number         int                 `json:"number"`
	ActivityRef    string              `json:"activityRef"`
	IdempotencyKey string              `json:"idempotencyKey"`
	Status         AttemptStatus       `json:"status"`
	ErrorClass     activity.ErrorClass `json:"errorClass,omitempty"`
	Error          string              `json:"error,omitempty"`
	Receipt        string              `json:"receipt,omitempty"`
	CostUSD        float64             `json:"costUSD,omitempty"`
	Output         json.RawMessage     `json:"output,omitempty"`
	StartedAt      time.Time           `json:"startedAt"`
	FinishedAt     *time.Time          `json:"finishedAt,omitempty"`
}

type Approval struct {
	ID        string     `json:"id"`
	RunID     string     `json:"runId"`
	StepRunID int64      `json:"stepRunId"`
	Reason    string     `json:"reason"`
	Status    string     `json:"status"`
	Decision  string     `json:"decision,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	DecidedAt *time.Time `json:"decidedAt,omitempty"`
}
