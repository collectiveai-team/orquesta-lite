package workflow

import (
	"encoding/json"
	"reflect"
	"testing"
)

func testResolver(config map[string]any) *executionState {
	return &executionState{
		runtime: &Runtime{Config: config},
		run:     &Run{ID: "r1", FlowRef: "flow:demo@1"},
		inputs:  map[string]any{"name": "demo"},
		steps: map[string]any{
			"loop": []any{
				map[string]any{"state": map[string]any{"status": "active"}},
				map[string]any{"state": map[string]any{"status": "complete"}},
			},
			"empty": []any{},
		},
	}
}

func TestResolveConfigNamespace(t *testing.T) {
	state := testResolver(map[string]any{"lint_argv": []any{"go", "vet", "./..."}})
	value, ok := state.resolve("config.lint_argv")
	if !ok {
		t.Fatal("config.lint_argv must resolve")
	}
	if !reflect.DeepEqual(value, []any{"go", "vet", "./..."}) {
		t.Fatalf("value=%#v", value)
	}
	// An unset key behaves exactly like an unknown inputs.* key: not found.
	if _, ok := state.resolve("config.test_argv"); ok {
		t.Fatal("an absent config key must not resolve")
	}
	// The namespace is flat.
	for _, path := range []string{"config", "config.lint_argv.0", "config.a.b"} {
		if _, ok := state.resolve(path); ok {
			t.Errorf("%q must not resolve", path)
		}
	}
}

func TestResolveConfigWithNoConfigured(t *testing.T) {
	state := testResolver(nil)
	if _, ok := state.resolve("config.lint_argv"); ok {
		t.Fatal("no config means nothing resolves")
	}
}

// A while step aggregates every pass into an array. `last` is what lets a gate
// assert on the value the loop actually ended with, instead of shelling out to
// a JSON parser to read a role's result file.
func TestResolveArrayIndexAndLast(t *testing.T) {
	state := testResolver(nil)
	cases := map[string]any{
		"steps.loop.output.last.state.status": "complete",
		"steps.loop.output.0.state.status":    "active",
		"steps.loop.output.1.state.status":    "complete",
	}
	for path, want := range cases {
		value, ok := state.resolve(path)
		if !ok || value != want {
			t.Errorf("%s = %v (ok=%v), want %v", path, value, ok, want)
		}
	}
	for _, path := range []string{
		"steps.loop.output.2.state.status", // out of range
		"steps.loop.output.-1.state",       // negative
		"steps.loop.output.first",          // not a keyword we support
		"steps.empty.output.last",          // an empty array has no last element
	} {
		if _, ok := state.resolve(path); ok {
			t.Errorf("%q must not resolve", path)
		}
	}
}

func TestIntegerValueAcceptsDurableAndLiteralNumbers(t *testing.T) {
	for name, value := range map[string]any{
		"json.Number": json.Number("12"),
		"float64":     float64(12),
		"int":         12,
		"int64":       int64(12),
	} {
		got, ok := integerValue(value)
		if !ok || got != 12 {
			t.Errorf("%s: got %d ok=%v", name, got, ok)
		}
	}
	for name, value := range map[string]any{
		"fractional json.Number": json.Number("2.5"),
		"fractional float64":     2.5,
		"string":                 "12",
		"bool":                   true,
		"nil":                    nil,
	} {
		if _, ok := integerValue(value); ok {
			t.Errorf("%s must not be an integer", name)
		}
	}
}
