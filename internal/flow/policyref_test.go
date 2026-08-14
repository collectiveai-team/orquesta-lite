package flow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
)

func policyFlow(metadataPolicy string) string {
	policy := ""
	if metadataPolicy != "" {
		policy = `,"policy":"` + metadataPolicy + `"`
	}
	return `{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"budgeted","version":"1"` + policy + `},"steps":[{"id":"echo","uses":"activity:test.echo@1"}]}`
}

func policyCatalog() *MemoryCatalog {
	catalog := NewMemoryCatalog()
	catalog.Activities["activity:test.echo@1"] = activity.Spec{Name: "test.echo", Version: "1", Effect: activity.EffectPure}
	catalog.Policies["policy:development@3"] = json.RawMessage(`{"maxDurationSeconds":28800,"maxAttempts":0}`)
	return catalog
}

// A flow-declared policy is pinned into the IR, so the budget a run executed
// under is part of the definition digest instead of an operator's shell history.
func TestCompilePinsFlowDeclaredPolicy(t *testing.T) {
	doc, err := Decode(strings.NewReader(policyFlow("policy:development@3")))
	if err != nil {
		t.Fatal(err)
	}
	ir, diagnostics := Compile(doc, policyCatalog())
	if diagnostics.HasErrors() {
		t.Fatalf("%+v", diagnostics)
	}
	if ir.Metadata.Policy != "policy:development@3" {
		t.Fatalf("metadata.policy = %q", ir.Metadata.Policy)
	}
	if _, ok := ir.Policies["policy:development@3"]; !ok {
		t.Fatalf("declared policy is not pinned: %v", ir.Policies)
	}
	if _, ok := ir.Resources["policy:development@3"]; !ok {
		t.Fatalf("declared policy digest is not pinned: %v", ir.Resources)
	}
}

// The whole point of failing here is that `flow validate` and `flow run` reject
// the flow before any step runs, rather than the scheduler tripping over a
// missing policy after real work has been done.
func TestCompileRejectsUnresolvableFlowPolicy(t *testing.T) {
	for name, ref := range map[string]string{
		"unknown policy": "policy:nonexistent@9",
		"wrong kind":     "schema:development@3",
		"not a ref":      "development@3",
	} {
		doc, err := Decode(strings.NewReader(policyFlow(ref)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ir, diagnostics := Compile(doc, policyCatalog())
		if ir != nil || !diagnostics.HasErrors() {
			t.Errorf("%s (%s) must fail compilation: ir=%v diagnostics=%+v", name, ref, ir, diagnostics)
		}
	}
}

func TestCompileAcceptsFlowWithoutDeclaredPolicy(t *testing.T) {
	doc, err := Decode(strings.NewReader(policyFlow("")))
	if err != nil {
		t.Fatal(err)
	}
	ir, diagnostics := Compile(doc, policyCatalog())
	if diagnostics.HasErrors() {
		t.Fatalf("%+v", diagnostics)
	}
	if ir.Metadata.Policy != "" || len(ir.Policies) != 0 {
		t.Fatalf("unexpected policy on an undeclared flow: %+v", ir.Policies)
	}
}
