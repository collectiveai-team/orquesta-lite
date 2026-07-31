package flow

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestIntegerSchemaAcceptsAWholeNumberWithADecimalPoint is the schema half of
// the iteration_budget contract break.
//
// The while-loop's bound and this validator are two independent readings of
// the same question ("is this an integer?"), and they used to disagree: both
// called json.Number.Int64(), which is strconv.ParseInt and rejects "8.0"
// outright, while a float64(8.0) from a non-UseNumber decode sailed through.
// Tightening schemas/workflow-state@2.json from "number" to "integer" without
// fixing this would merely have relocated the crash — from the loop bound to
// the agent.invoke output contract, an ErrorInvalidContract class that
// development@3.json does not retry. Both sides now share WholeNumber.
func TestIntegerSchemaAcceptsAWholeNumberWithADecimalPoint(t *testing.T) {
	schema, err := DecodeSchema(strings.NewReader(`{"type":"object","properties":{"iteration_budget":{"type":"integer","minimum":1,"maximum":200}},"required":["iteration_budget"]}`))
	if err != nil {
		t.Fatal(err)
	}
	// UseNumber is how the runtime decodes durable workflow state, so a
	// planner's literal `8.0` arrives as json.Number("8.0"), not float64(8).
	valid := []string{`{"iteration_budget":8}`, `{"iteration_budget":8.0}`, `{"iteration_budget":2.0e1}`}
	for _, raw := range valid {
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		var document any
		if err = decoder.Decode(&document); err != nil {
			t.Fatal(err)
		}
		if err = schema.Validate(document); err != nil {
			t.Errorf("%s is a whole number and must satisfy type:integer: %v", raw, err)
		}
	}
	// A genuinely fractional budget stays a contract violation — the point is
	// to agree on whole numbers, not to start rounding.
	for _, raw := range []string{`{"iteration_budget":2.5}`, `{"iteration_budget":"8"}`} {
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		var document any
		if err = decoder.Decode(&document); err != nil {
			t.Fatal(err)
		}
		if err = schema.Validate(document); err == nil {
			t.Errorf("%s must not satisfy type:integer", raw)
		}
	}
}

// TestWholeNumberIsEncodingIndependent pins the property both call sites rely
// on: the same semantic value decides the same way regardless of whether it
// arrived as a json.Number or as a plain Go float.
func TestWholeNumberIsEncodingIndependent(t *testing.T) {
	for _, pair := range []struct {
		number json.Number
		float  float64
		want   int64
		ok     bool
	}{
		{"8", 8, 8, true},
		{"8.0", 8.0, 8, true},
		{"2.5", 2.5, 0, false},
		{"-3.0", -3.0, -3, true},
	} {
		gotNumber, numberOK := WholeNumber(pair.number)
		gotFloat, floatOK := WholeNumber(pair.float)
		if numberOK != pair.ok || floatOK != pair.ok || (pair.ok && (gotNumber != pair.want || gotFloat != pair.want)) {
			t.Errorf("%q: json.Number=(%d,%v) float64=(%d,%v), want (%d,%v)", pair.number, gotNumber, numberOK, gotFloat, floatOK, pair.want, pair.ok)
		}
	}
	if _, ok := WholeNumber("8"); ok {
		t.Error(`the string "8" is not a number`)
	}
}
