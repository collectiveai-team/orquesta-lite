package flow

import "testing"

func TestEvalBool(t *testing.T) {
	values := map[string]any{"steps.test.output.pass": true, "attempt": 2.0}
	resolve := func(path string) (any, bool) { value, ok := values[path]; return value, ok }
	for _, expression := range []string{`steps.test.output.pass == true`, `attempt >= 2 && !false`, `(attempt < 3) == true`} {
		got, err := EvalBool(expression, resolve)
		if err != nil || !got {
			t.Fatalf("%s = %v, %v", expression, got, err)
		}
	}
	if _, err := EvalBool(`steps.missing.output.pass`, resolve); err == nil {
		t.Fatal("expected missing reference error")
	}
	if got, err := EvalBool(`"a" < "b" && 1 != "1"`, resolve); err != nil || !got {
		t.Fatalf("typed comparison=%v err=%v", got, err)
	}
}

func TestEvalBoolShortCircuitsMissingReferences(t *testing.T) {
	for _, test := range []struct {
		expression string
		want       bool
	}{
		{`false && steps.skipped.output.approved == true`, false},
		{`true || steps.skipped.output.approved == true`, true},
		{`false && !(steps.skipped.output.count > 0 || steps.skipped.output.ready)`, false},
		{`(false && steps.skipped.output.ready) || true`, true},
		{`true || (false && steps.skipped.output.ready)`, true},
	} {
		t.Run(test.expression, func(t *testing.T) {
			got, err := EvalBool(test.expression, func(path string) (any, bool) { t.Fatalf("resolved skipped reference %s", path); return nil, false })
			if err != nil || got != test.want {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
	for _, expression := range []string{`true && missing`, `false || missing`, `true || (false && )`, `false && (true || )`} {
		if _, err := EvalBool(expression, func(string) (any, bool) { return nil, false }); err == nil {
			t.Fatalf("accepted malformed/reachable reference in %s", expression)
		}
	}
}
