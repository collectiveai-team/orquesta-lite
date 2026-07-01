package engine

import (
	"path/filepath"
	"testing"
)

// TestLiveFlowsJSONValid guards the shipped flows.json: it must load and
// expose the factory + factory_fast flows. This catches config drift before
// anyone runs the CLI.
func TestLiveFlowsJSONValid(t *testing.T) {
	path := filepath.Join("..", "..", "flows.json")
	flows, err := LoadFlows(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, name := range []string{"factory", "factory_fast"} {
		if _, ok := flows.Flows[name]; !ok {
			t.Errorf("flows.json missing flow %q", name)
		}
	}
	factory := flows.Flows["factory"]
	if got := len(factory.Steps); got < 2 {
		t.Fatalf("factory flow has %d steps, want >=2", got)
	}
	if factory.Steps[0].Action != "factory_extract_features" {
		t.Fatalf("first factory step is %q, want factory_extract_features", factory.Steps[0].Action)
	}
	fast := flows.Flows["factory_fast"]
	sawVerifier := false
	for _, s := range fast.Steps {
		if s.Agent == "verifier" {
			sawVerifier = true
		}
	}
	if !sawVerifier {
		t.Fatalf("factory_fast should end with a verifier step")
	}
}
