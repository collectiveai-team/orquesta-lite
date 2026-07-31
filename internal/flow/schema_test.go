package flow

import (
	"strings"
	"testing"
)

func TestSchemaSubsetValidation(t *testing.T) {
	schema, err := DecodeSchema(strings.NewReader(`{
      "type":"object",
      "additionalProperties":false,
      "properties":{"status":{"type":"string","enum":["ok"]},"items":{"type":"array","minItems":1,"items":{"type":"integer"}}},
      "required":["status","items"]
    }`))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.ValidateJSON([]byte(`{"status":"ok","items":[1,2]}`)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"status":"bad","items":[1]}`, `{"status":"ok","items":[]}`, `{"status":"ok","items":[1],"extra":true}`} {
		if err := schema.ValidateJSON([]byte(raw)); err == nil {
			t.Fatalf("expected %s to fail", raw)
		}
	}
}

// A numeric range is what stops an agent-declared budget from growing without
// limit: it is enforced on every emission by the same output validation the
// rest of the contract uses.
func TestSchemaNumericRange(t *testing.T) {
	schema, err := DecodeSchema(strings.NewReader(`{
      "type":"object",
      "additionalProperties":false,
      "properties":{"iteration_budget":{"type":"number","minimum":1,"maximum":200}},
      "required":["iteration_budget"]
    }`))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"iteration_budget":1}`, `{"iteration_budget":42}`, `{"iteration_budget":200}`} {
		if err := schema.ValidateJSON([]byte(raw)); err != nil {
			t.Errorf("%s should validate: %v", raw, err)
		}
	}
	for _, raw := range []string{`{"iteration_budget":0}`, `{"iteration_budget":-3}`, `{"iteration_budget":201}`, `{"iteration_budget":"8"}`} {
		if err := schema.ValidateJSON([]byte(raw)); err == nil {
			t.Errorf("expected %s to fail", raw)
		}
	}
}

func TestSchemaUnknownKeywordFailsClosed(t *testing.T) {
	if _, err := DecodeSchema(strings.NewReader(`{"type":"string","pattern":"x"}`)); err == nil {
		t.Fatal("expected unsupported keyword error")
	}
}
