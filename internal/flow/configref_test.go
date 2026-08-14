package flow

import (
	"strings"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
)

// The config namespace is deliberately flat: exactly one key segment, so a host
// can validate a reference against its whitelist with a two-segment comparison.
func TestValidateReferenceConfigNamespaceIsFlat(t *testing.T) {
	inputs := map[string]InputSpec{}
	steps := map[string]bool{}
	schemas := map[string]*Schema{}
	if err := validateReference("config.lint_argv", inputs, steps, schemas, false); err != nil {
		t.Errorf("config.lint_argv must be accepted: %v", err)
	}
	for _, rejected := range []string{"config", "config.a.b", "config.a.b.c"} {
		if err := validateReference(rejected, inputs, steps, schemas, false); err == nil {
			t.Errorf("%q must be rejected", rejected)
		}
	}
}

// A config reference is valid outside foreach/while too — it is project
// configuration, not loop state.
func TestConfigReferenceCompilesOutsideLoops(t *testing.T) {
	doc, err := Decode(strings.NewReader(`{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"gated","version":"1"},"steps":[{"id":"lint","uses":"activity:test.echo@1","with":{"argv":{"$ref":"config.lint_argv"}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	ir, diagnostics := Compile(doc, loopCatalog())
	if diagnostics.HasErrors() {
		t.Fatalf("%+v", diagnostics)
	}
	found := false
	for _, path := range ir.ReferencePaths() {
		if path == "config.lint_argv" {
			found = true
		}
	}
	if !found {
		t.Fatalf("config reference missing from %v", ir.ReferencePaths())
	}
}

// ReferencePaths has to see through `if` and `while.condition`, not just the
// value tree. Those are expression strings evaluated against the very same
// resolver, so a host validating a namespace before a run starts is blind to
// half of the flow's references without this — which is how a `config.` gate
// guard used to compile, pass validation, create a run, and only then die.
func TestReferencePathsCoverConditionExpressions(t *testing.T) {
	doc, err := Decode(strings.NewReader(`{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"gated","version":"1"},"steps":[{"id":"loop","uses":"activity:test.echo@1","if":"config.lint_argv != null","while":{"condition":"item.go == true && config.test_argv != null","maxIterations":2,"initial":{"go":true}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	ir, diagnostics := Compile(doc, loopCatalog())
	if diagnostics.HasErrors() {
		t.Fatalf("%+v", diagnostics)
	}
	paths := map[string]bool{}
	for _, path := range ir.ReferencePaths() {
		paths[path] = true
	}
	for _, want := range []string{"config.lint_argv", "config.test_argv", "item.go"} {
		if !paths[want] {
			t.Errorf("%q missing from %v", want, ir.ReferencePaths())
		}
	}
}

func TestNestedConfigReferenceFailsCompilation(t *testing.T) {
	doc, err := Decode(strings.NewReader(`{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"gated","version":"1"},"steps":[{"id":"lint","uses":"activity:test.echo@1","with":{"argv":{"$ref":"config.gates.lint"}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if ir, diagnostics := Compile(doc, loopCatalog()); ir != nil || !diagnostics.HasErrors() {
		t.Fatalf("nested config reference must fail: ir=%v diagnostics=%+v", ir, diagnostics)
	}
}

// A path through a loop aggregate keeps being checked against the pinned
// element schema instead of going untyped at the array boundary.
func TestReferenceThroughArrayAggregateIsTypeChecked(t *testing.T) {
	deny := false
	catalog := NewMemoryCatalog()
	catalog.Schemas["schema:out@1"] = &Schema{Type: Types{"object"}, Properties: map[string]*Schema{"status": {Type: Types{"string"}}}, AdditionalProperties: &deny}
	catalog.Activities["activity:test.echo@1"] = activity.Spec{Name: "test.echo", Version: "1", Effect: activity.EffectPure, OutputSchema: "schema:out@1"}

	body := func(path string) string {
		return `{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"agg","version":"1"},"steps":[{"id":"loop","uses":"activity:test.echo@1","while":{"condition":"item.go == true","maxIterations":2,"initial":{"go":true}}},{"id":"gate","uses":"activity:test.echo@1","with":{"value":{"$ref":"` + path + `"}}}]}`
	}
	doc, err := Decode(strings.NewReader(body("steps.loop.output.last.status")))
	if err != nil {
		t.Fatal(err)
	}
	if _, diagnostics := Compile(doc, catalog); diagnostics.HasErrors() {
		t.Fatalf("a declared property through `last` must compile: %+v", diagnostics)
	}
	doc, err = Decode(strings.NewReader(body("steps.loop.output.last.missing")))
	if err != nil {
		t.Fatal(err)
	}
	if _, diagnostics := Compile(doc, catalog); !diagnostics.HasErrors() {
		t.Fatal("a property the element schema rejects must fail compilation")
	}
}
