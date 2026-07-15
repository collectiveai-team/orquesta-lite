package activity

import (
	"context"
	"encoding/json"
)

// Request identifies one logical step execution. IdempotencyKey is stable
// across attempts for that step instance.
type Request struct {
	RunID          string          `json:"runId"`
	StepID         string          `json:"stepId"`
	ScopePath      string          `json:"scopePath"`
	ForeachKey     string          `json:"foreachKey,omitempty"`
	Attempt        int             `json:"attempt"`
	IdempotencyKey string          `json:"idempotencyKey"`
	Inputs         json.RawMessage `json:"inputs"`
}

// Artifact is metadata for evidence stored outside the workflow context.
type Artifact struct {
	Kind   string         `json:"kind"`
	URI    string         `json:"uri"`
	Digest string         `json:"digest,omitempty"`
	Meta   map[string]any `json:"meta,omitempty"`
}

// Result is validated against Spec.OutputSchema before a step can succeed.
type Result struct {
	Output    json.RawMessage `json:"output"`
	Artifacts []Artifact      `json:"artifacts,omitempty"`
	Receipt   string          `json:"receipt,omitempty"`
	CostUSD   float64         `json:"costUSD,omitempty"`
}

type Executor interface {
	Spec() Spec
	Execute(context.Context, Request) (Result, error)
}

type ReconcileRequest struct {
	Request
	Receipt string `json:"receipt,omitempty"`
}

type ReconcileStatus string

const (
	ReconcileApplied    ReconcileStatus = "applied"
	ReconcileNotApplied ReconcileStatus = "not_applied"
	ReconcileUnknown    ReconcileStatus = "unknown"
)

type ReconcileResult struct {
	Status ReconcileStatus `json:"status"`
	Result *Result         `json:"result,omitempty"`
}

type Reconciler interface {
	Reconcile(context.Context, ReconcileRequest) (ReconcileResult, error)
}

type CompensateRequest struct {
	Request
	Receipt string `json:"receipt,omitempty"`
}

type Compensator interface {
	Compensate(context.Context, CompensateRequest) (Result, error)
}
