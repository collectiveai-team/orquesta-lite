package flow

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
)

func loopFlow(bound string) string {
	return fmt.Sprintf(`{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"loop","version":"1"},"steps":[{"id":"inc","uses":"activity:test.echo@1","while":{"condition":"item.value < 3","maxIterations":%s,"initial":{"value":0}},"with":{"value":{"$ref":"item.value"}}}]}`, bound)
}

func loopCatalog() *MemoryCatalog {
	catalog := NewMemoryCatalog()
	catalog.Activities["activity:test.echo@1"] = activity.Spec{Name: "test.echo", Version: "1", Effect: activity.EffectPure}
	return catalog
}

// A literal bound must behave exactly as it did before maxIterations became a
// Value — every pack in the wild ships literals.
func TestDecodeAcceptsLiteralMaxIterations(t *testing.T) {
	doc, err := Decode(strings.NewReader(loopFlow("20")))
	if err != nil {
		t.Fatal(err)
	}
	bound, ok := doc.Steps[0].While.MaxIterations.LiteralInt()
	if !ok || bound != 20 {
		t.Fatalf("bound=%d literal=%v", bound, ok)
	}
	if _, diagnostics := Compile(doc, loopCatalog()); diagnostics.HasErrors() {
		t.Fatalf("literal bound must compile: %+v", diagnostics)
	}
}

func TestDecodeAcceptsRefMaxIterations(t *testing.T) {
	doc, err := Decode(strings.NewReader(loopFlow(`{"$ref":"item.budget"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Steps[0].While.MaxIterations.Ref == nil {
		t.Fatalf("expected a reference bound: %+v", doc.Steps[0].While)
	}
	// allowItem must be true for the bound (unlike while.initial): it is
	// re-resolved per pass with `item` already set.
	if _, diagnostics := Compile(doc, loopCatalog()); diagnostics.HasErrors() {
		t.Fatalf("item-derived bound must compile: %+v", diagnostics)
	}
}

func TestDecodeRejectsInvalidLiteralMaxIterations(t *testing.T) {
	for name, literal := range map[string]string{
		"non-numeric": `"twenty"`,
		"non-integer": `2.5`,
		"below one":   `0`,
		"negative":    `-1`,
		"object":      `{"value":3}`,
	} {
		if _, err := Decode(strings.NewReader(loopFlow(literal))); err == nil {
			t.Errorf("%s bound %s must be rejected at decode time", name, literal)
		}
	}
}

func TestCompileRejectsMaxIterationsRefToUnknownRoot(t *testing.T) {
	doc, err := Decode(strings.NewReader(loopFlow(`{"$ref":"inputs.budget"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if _, diagnostics := Compile(doc, loopCatalog()); !diagnostics.HasErrors() {
		t.Fatal("a bound referencing an undeclared input must fail compilation")
	}
}

func TestIRReferencePathsIncludesWhileBound(t *testing.T) {
	doc, err := Decode(strings.NewReader(loopFlow(`{"$ref":"item.budget"}`)))
	if err != nil {
		t.Fatal(err)
	}
	ir, diagnostics := Compile(doc, loopCatalog())
	if diagnostics.HasErrors() {
		t.Fatalf("%+v", diagnostics)
	}
	found := false
	for _, path := range ir.ReferencePaths() {
		if path == "item.budget" {
			found = true
		}
	}
	if !found {
		t.Fatalf("while bound reference missing from %v", ir.ReferencePaths())
	}
}

// The compiled IR must round-trip through JSON unchanged: the durable store
// persists it and `flow resume` decodes it back.
func TestWhileBoundSurvivesIRRoundTrip(t *testing.T) {
	doc, err := Decode(strings.NewReader(loopFlow("7")))
	if err != nil {
		t.Fatal(err)
	}
	ir, diagnostics := Compile(doc, loopCatalog())
	if diagnostics.HasErrors() {
		t.Fatalf("%+v", diagnostics)
	}
	raw, err := json.Marshal(ir)
	if err != nil {
		t.Fatal(err)
	}
	var restored IR
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	bound, ok := restored.Steps[0].While.MaxIterations.LiteralInt()
	if !ok || bound != 7 {
		t.Fatalf("bound=%d literal=%v raw=%s", bound, ok, raw)
	}
}
