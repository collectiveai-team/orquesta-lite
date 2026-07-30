package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/lionelchamorro/orquestalite/internal/activity"
)

// GateAssertExecutor is a language-agnostic equality gate. It replaces the
// `sh -c` / `python -c` one-liners flows used to assert on a role's JSON
// result: no shell, no interpreter, no dependency on the project's language.
type GateAssertExecutor struct{}

func (GateAssertExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "gate.assert", Version: "1", Effect: activity.EffectPure}
}

type gateAssertInput struct {
	Value   any    `json:"value"`
	Equals  any    `json:"equals"`
	Message string `json:"message,omitempty"`
}

func (GateAssertExecutor) Execute(_ context.Context, request activity.Request) (activity.Result, error) {
	var input gateAssertInput
	if err := strictJSON(request.Inputs, &input); err != nil {
		return activity.Result{}, contractError("gate.assert input", err)
	}
	passed := assertEqual(input.Value, input.Equals)
	raw, _ := json.Marshal(map[string]any{"passed": passed})
	if !passed {
		message := input.Message
		if message == "" {
			message = "gate assertion failed"
		}
		return activity.Result{Output: raw}, &activity.Error{
			Class: activity.ErrorGateFailed,
			Op:    "gate.assert",
			Err:   fmt.Errorf("%s (value %s does not equal %s)", message, describeValue(input.Value), describeValue(input.Equals)),
		}
	}
	return activity.Result{Output: raw}, nil
}

// assertEqual compares two JSON-decoded values. Numbers compare by numeric
// value across encodings (json.Number vs float64), everything else compares
// structurally. Values of different JSON types are simply not equal — a number
// never equals a string — so a type mismatch is a clean gate failure rather
// than a panic or a silent pass.
func assertEqual(a, b any) bool {
	aNumber, aIsNumber := assertNumber(a)
	bNumber, bIsNumber := assertNumber(b)
	if aIsNumber || bIsNumber {
		return aIsNumber && bIsNumber && aNumber == bNumber
	}
	return reflect.DeepEqual(a, b)
}

func assertNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}

func describeValue(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(raw)
}
