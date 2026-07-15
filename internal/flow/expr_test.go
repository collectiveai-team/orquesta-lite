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
