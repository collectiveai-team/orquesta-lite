package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/lionelchamorro/orquestalite/internal/activity"
)

// GateAssertExecutor is a language-agnostic equality gate. It replaces the
// `sh -c` / `python -c` one-liners flows used to assert on a role's JSON
// result: no shell, no interpreter, no dependency on the project's language.
type GateAssertExecutor struct{}

func (GateAssertExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "gate.assert", Version: "1", Effect: activity.EffectPure}
}

// Value and Equals are json.RawMessage rather than `any` so the executor can
// tell "absent" from "present and null". Decoded as `any`, a request that
// declared neither would compare nil to nil, pass, and report a green gate
// that inspected nothing — the one outcome a blocking gate must never have,
// and the exact shape a flow takes when a $ref is meant to be filled in later
// or a hand-edit drops a key.
type gateAssertInput struct {
	Value   json.RawMessage `json:"value"`
	Equals  json.RawMessage `json:"equals"`
	Message string          `json:"message,omitempty"`
}

func (GateAssertExecutor) Execute(_ context.Context, request activity.Request) (activity.Result, error) {
	var input gateAssertInput
	if err := strictJSON(request.Inputs, &input); err != nil {
		return activity.Result{}, contractError("gate.assert input", err)
	}
	// Required the same way agent.invoke@1 requires role and outputSchema: a
	// gate with nothing to assert is a contract error, not a pass.
	var missing []string
	if len(input.Value) == 0 {
		missing = append(missing, "value")
	}
	if len(input.Equals) == 0 {
		missing = append(missing, "equals")
	}
	if len(missing) > 0 {
		return activity.Result{}, contractError("gate.assert input", fmt.Errorf("%s required: a gate that asserts nothing always passes", strings.Join(missing, " and ")))
	}
	value, err := decodeAssertOperand(input.Value)
	if err != nil {
		return activity.Result{}, contractError("gate.assert value", err)
	}
	equals, err := decodeAssertOperand(input.Equals)
	if err != nil {
		return activity.Result{}, contractError("gate.assert equals", err)
	}
	// `null == null` is a tautology: at the moment this executor runs, an
	// assertion whose both sides are null could not have failed no matter what
	// the flow did. That is the same green-but-blind gate as an omitted key,
	// reached instead by an unresolved $ref landing next to a literal null, so
	// it is rejected the same way.
	//
	// The cost is deliberate and small: `gate.assert@1` cannot express "this
	// value is null". Asserting a *present* value is what a blocking gate is
	// for, and a gate that can never fail is worse than not having one.
	if value == nil && equals == nil {
		return activity.Result{}, contractError("gate.assert input", fmt.Errorf("value and equals are both null: this assertion can never fail"))
	}
	passed := assertEqual(value, equals)
	raw, _ := json.Marshal(map[string]any{"passed": passed})
	if !passed {
		message := input.Message
		if message == "" {
			message = "gate assertion failed"
		}
		return activity.Result{Output: raw}, &activity.Error{
			Class: activity.ErrorGateFailed,
			Op:    "gate.assert",
			Err:   fmt.Errorf("%s (value %s does not equal %s)", message, describeValue(value), describeValue(equals)),
		}
	}
	return activity.Result{Output: raw}, nil
}

// decodeAssertOperand decodes one side of the comparison with UseNumber, so a
// number carried out of durable state (json.Number) and the same number
// written as a literal in the flow compare by value rather than by Go type.
func decodeAssertOperand(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
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
