package builtin

import (
	"context"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
)

// TestGateAssertRejectsAnAssertionWithNothingToAssert pins gate.assert@1's
// contract against the one outcome a gate must never have: passing while
// asserting nothing.
//
// assertEqual compares input.Value against input.Equals, both plain `any`. When
// the flow author writes neither — or writes only a message, which is the
// natural typo when the ref is filled in later — both decode to nil, nil equals
// nil, and the gate returns {"passed":true}. The pack's whole reason for
// introducing gate.assert@1 was to have a blocking gate that does not depend on
// a shell or an interpreter; a green gate that inspected nothing is the failure
// mode the design calls out ("un gate que se saltea en silencio es peor que uno
// que falla") and the one the round-3 report blamed for shipping a bug under a
// governance approval.
//
// agent.invoke@1 already models the fix: it rejects a request missing `role` or
// `outputSchema` with a contract error rather than proceeding. gate.assert@1
// must require `value` and `equals` the same way.
func TestGateAssertRejectsAnAssertionWithNothingToAssert(t *testing.T) {
	cases := map[string]string{
		"no value and no equals": `{"message":"the ticket loop must reach complete"}`,
		"explicit nulls":         `{"value":null,"equals":null}`,
		"value only":             `{"value":null,"message":"m"}`,
		"equals only":            `{"equals":"complete","message":"m"}`,
	}
	for name, inputs := range cases {
		result, err := GateAssertExecutor{}.Execute(context.Background(), activity.Request{Inputs: []byte(inputs)})
		if err == nil {
			t.Errorf("%s: gate.assert passed with nothing to assert (output=%s); it must reject the request instead", name, result.Output)
			continue
		}
		// invalid_contract, not gate_failed: the flow is malformed, and the
		// class matters because development@3.json retries neither but only
		// invalid_contract names the right culprit in the log.
		if class := activity.Classify(err); class != activity.ErrorInvalidContract {
			t.Errorf("%s: class=%s, want invalid_contract", name, class)
		}
	}
}
