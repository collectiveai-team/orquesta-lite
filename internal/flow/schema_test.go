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

func TestSchemaUnknownKeywordFailsClosed(t *testing.T) {
	if _, err := DecodeSchema(strings.NewReader(`{"type":"string","pattern":"x"}`)); err == nil {
		t.Fatal("expected unsupported keyword error")
	}
}
