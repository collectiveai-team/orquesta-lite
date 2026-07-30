package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/activity"
)

func assertGate(t *testing.T, inputs string) (activity.Result, error) {
	t.Helper()
	return GateAssertExecutor{}.Execute(context.Background(), activity.Request{Inputs: json.RawMessage(inputs)})
}

func TestGateAssertSpecIsPure(t *testing.T) {
	spec := GateAssertExecutor{}.Spec()
	if spec.Ref() != "gate.assert@1" || spec.Effect != activity.EffectPure {
		t.Fatalf("spec=%+v", spec)
	}
}

func TestGateAssertPasses(t *testing.T) {
	for name, inputs := range map[string]string{
		"strings": `{"value":"complete","equals":"complete","message":"m"}`,
		"bools":   `{"value":true,"equals":true,"message":"m"}`,
		"numbers": `{"value":3,"equals":3,"message":"m"}`,
		"nulls":   `{"value":null,"equals":null,"message":"m"}`,
		"arrays":  `{"value":["a","b"],"equals":["a","b"],"message":"m"}`,
		"objects": `{"value":{"ok":true},"equals":{"ok":true},"message":"m"}`,
	} {
		result, err := assertGate(t, inputs)
		if err != nil {
			t.Errorf("%s: unexpected failure: %v", name, err)
			continue
		}
		if string(result.Output) != `{"passed":true}` {
			t.Errorf("%s: output=%s", name, result.Output)
		}
	}
}

func TestGateAssertFailsWithGateFailedAndMessage(t *testing.T) {
	result, err := assertGate(t, `{"value":"active","equals":"complete","message":"the ticket plan never reached complete"}`)
	if err == nil {
		t.Fatal("mismatched values must fail the gate")
	}
	if activity.Classify(err) != activity.ErrorGateFailed {
		t.Fatalf("class=%s", activity.Classify(err))
	}
	if !strings.Contains(err.Error(), "the ticket plan never reached complete") {
		t.Fatalf("the caller's message must survive into the error: %v", err)
	}
	// The failing value and expectation are named, so the log says what happened.
	if !strings.Contains(err.Error(), `"active"`) || !strings.Contains(err.Error(), `"complete"`) {
		t.Fatalf("error should name both values: %v", err)
	}
	if string(result.Output) != `{"passed":false}` {
		t.Fatalf("output=%s", result.Output)
	}
}

// A type mismatch is a clean non-match, not a panic and not a silent pass: a
// number never equals a string, and a missing value never equals a present one.
func TestGateAssertTypeMismatchIsACleanFailure(t *testing.T) {
	for name, inputs := range map[string]string{
		"number vs string": `{"value":3,"equals":"3","message":"m"}`,
		"bool vs string":   `{"value":true,"equals":"true","message":"m"}`,
		"null vs string":   `{"value":null,"equals":"complete","message":"m"}`,
		"object vs string": `{"value":{"status":"complete"},"equals":"complete","message":"m"}`,
		"array vs string":  `{"value":["complete"],"equals":"complete","message":"m"}`,
		"missing value":    `{"equals":"complete","message":"m"}`,
	} {
		if _, err := assertGate(t, inputs); err == nil || activity.Classify(err) != activity.ErrorGateFailed {
			t.Errorf("%s: err=%v class=%s, want a gate failure", name, err, activity.Classify(err))
		}
	}
}

// Numeric encodings agree: durable state may carry a json.Number while the
// flow literal decodes as float64.
func TestGateAssertNumbersCompareByValue(t *testing.T) {
	if _, err := assertGate(t, `{"value":3.0,"equals":3,"message":"m"}`); err != nil {
		t.Fatalf("3.0 must equal 3: %v", err)
	}
	if _, err := assertGate(t, `{"value":3,"equals":4,"message":"m"}`); err == nil {
		t.Fatal("3 must not equal 4")
	}
}

func TestGateAssertRejectsUnknownFields(t *testing.T) {
	_, err := assertGate(t, `{"value":1,"equals":1,"surprise":true}`)
	if err == nil || activity.Classify(err) != activity.ErrorInvalidContract {
		t.Fatalf("err=%v class=%s", err, activity.Classify(err))
	}
}

func TestGateAssertUsesADefaultMessage(t *testing.T) {
	_, err := assertGate(t, `{"value":1,"equals":2}`)
	if err == nil || !strings.Contains(err.Error(), "gate assertion failed") {
		t.Fatalf("err=%v", err)
	}
}
