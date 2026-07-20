package flow

import (
	"encoding/json"
	"testing"
)

func TestResolveNestedValuePreservesTypes(t *testing.T) {
	var value Value
	if err := json.Unmarshal([]byte(`{"name":{"$ref":"inputs.name"},"flags":[true,2]}`), &value); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveValue(value, func(path string) (any, bool) { return "Ada", path == "inputs.name" })
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["name"] != "Ada" || len(m["flags"].([]any)) != 2 {
		t.Fatalf("got %#v", got)
	}
}
