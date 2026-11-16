package external

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Step defines the engine-owned contract. Adapters receive instructions, never
// routes or permission to select the next workflow step.
type Step struct {
	Adapter             string            `json:"adapter"`
	Employee            string            `json:"employee"`
	ProjectID           string            `json:"projectId,omitempty"`
	Instructions        string            `json:"instructions"`
	CompletionCondition string            `json:"completionCondition"`
	TimeoutSeconds      int               `json:"timeoutSeconds"`
	RequireEvidence     *bool             `json:"requireEvidence,omitempty"`
	OutputSchema        string            `json:"outputSchema,omitempty"`
	CompletionGate      string            `json:"completionGate,omitempty"`
	Routes              map[string]string `json:"routes,omitempty"`
	OutputMode          string            `json:"outputMode,omitempty"`
}

func DecodeStep(raw json.RawMessage) (Step, error) {
	var step Step
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&step); err != nil {
		return step, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return step, fmt.Errorf("external step has trailing JSON")
	}
	for name, value := range map[string]string{"adapter": step.Adapter, "employee": step.Employee, "instructions": step.Instructions, "completionCondition": step.CompletionCondition} {
		if strings.TrimSpace(value) == "" {
			return step, fmt.Errorf("external step %s is required", name)
		}
	}
	// A finite bound also prevents duration overflow when materialising a deadline.
	if step.TimeoutSeconds <= 0 || step.TimeoutSeconds > 365*24*60*60 {
		return step, fmt.Errorf("external timeoutSeconds must be within 1..31536000")
	}
	if step.OutputMode != "" && step.OutputMode != "envelope" && step.OutputMode != "deliverables" {
		return step, fmt.Errorf("external outputMode must be envelope or deliverables")
	}
	for outcome, route := range step.Routes {
		switch outcome {
		case "success", "timeout", "cancelled", "blocked", "failed", "unavailable", "invalid":
		default:
			return step, fmt.Errorf("unknown external outcome %q", outcome)
		}
		switch route {
		case "continue", "fail", "needs_human", "wait":
		default:
			return step, fmt.Errorf("unknown external route %q", route)
		}
		if route == "wait" && outcome != "unavailable" {
			return step, fmt.Errorf("only unavailable may explicitly route to wait")
		}
		if step.OutputMode == "deliverables" && outcome != "success" && route == "continue" {
			return step, fmt.Errorf("deliverables output cannot continue after %s; use envelope output for recovery branches", outcome)
		}
	}
	return step, nil
}

func (s Step) Route(outcome string) string {
	if route := s.Routes[outcome]; route != "" {
		return route
	}
	if outcome == "success" {
		return "continue"
	}
	if outcome == "unavailable" {
		return "wait"
	}
	return "needs_human"
}
