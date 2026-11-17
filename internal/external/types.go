// Package external defines transport-independent contracts for durable external
// work. Adapters report facts; only the workflow engine decides transitions.
package external

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

type Adapter interface {
	ResolveEmployee(context.Context, string) (string, error)
	Dispatch(context.Context, DispatchRequest) (References, error)
	Inspect(context.Context, References) (Result, error)
	Cancel(context.Context, References) (Cancellation, error)
}

type DispatchRequest struct {
	FlowRunID           string          `json:"flowRunId"`
	SourceKey           string          `json:"sourceKey,omitempty"`
	StepID              string          `json:"stepId"`
	AttemptID           string          `json:"attemptId"`
	IdempotencyKey      string          `json:"idempotencyKey"`
	EmployeeID          string          `json:"employeeId"`
	ProjectID           string          `json:"projectId,omitempty"`
	Instructions        string          `json:"instructions"`
	CompletionCondition string          `json:"completionCondition"`
	OutputSchema        json.RawMessage `json:"outputSchema,omitempty"`
	Deadline            time.Time       `json:"deadline"`
}

type References struct {
	IssueID string `json:"issueId"`
	RunID   string `json:"runId,omitempty"`
}

type Result struct {
	TaskStatus     string          `json:"taskStatus"`
	RunStatus      string          `json:"runStatus"`
	EmployeeID     string          `json:"employeeId"`
	EmployeeStatus string          `json:"employeeStatus,omitempty"`
	Deliverables   json.RawMessage `json:"deliverables,omitempty"`
	Evidence       []string        `json:"evidence,omitempty"`
	CostUSD        float64         `json:"costUSD,omitempty"`
	InputTokens    int64           `json:"inputTokens,omitempty"`
	OutputTokens   int64           `json:"outputTokens,omitempty"`
	References     References      `json:"references"`
	Usage          *Usage          `json:"usage,omitempty"`
}

// Usage keeps an explicitly reported zero distinct from unavailable metrics.
// Provider observations are cumulative for their referenced execution run.
type Usage struct {
	CostUSD      *float64 `json:"costUSD,omitempty"`
	InputTokens  *int64   `json:"inputTokens,omitempty"`
	OutputTokens *int64   `json:"outputTokens,omitempty"`
}

func (r Result) ValidUsage() bool {
	validCost := func(v float64) bool { return v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }
	if !validCost(r.CostUSD) || r.InputTokens < 0 || r.OutputTokens < 0 {
		return false
	}
	u := r.Usage
	return u == nil || ((u.CostUSD == nil || validCost(*u.CostUSD)) && (u.InputTokens == nil || *u.InputTokens >= 0) && (u.OutputTokens == nil || *u.OutputTokens >= 0))
}

type Cancellation struct {
	Accepted bool   `json:"accepted"`
	Detail   string `json:"detail,omitempty"`
}

// Event is a durable notification asking the engine to inspect external state.
// Its payload is never interpreted as permission to advance a flow.
type Event struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	References References      `json:"references,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

// UnavailableError means verification could not obtain authoritative state.
// It is never evidence of external success or failure.
type UnavailableError struct {
	Operation  string
	StatusCode int
	Err        error
}

func (e *UnavailableError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("paperclip %s: HTTP %d", e.Operation, e.StatusCode)
	}
	return fmt.Sprintf("paperclip %s: temporarily unavailable", e.Operation)
}

func (e *UnavailableError) Unwrap() error { return e.Err }
