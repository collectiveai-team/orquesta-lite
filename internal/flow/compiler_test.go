package flow

import (
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/activity"
)

func TestCompilePinsActivityAndIsDeterministic(t *testing.T) {
	doc, err := Decode(strings.NewReader(validFlow))
	if err != nil {
		t.Fatal(err)
	}
	catalog := NewMemoryCatalog()
	catalog.Activities["activity:test.echo@1"] = activity.Spec{Name: "test.echo", Version: "1", Effect: activity.EffectPure}
	catalog.Schemas["schema:string@1"] = &Schema{Type: Types{"string"}}
	first, diagnostics := Compile(doc, catalog)
	if diagnostics.HasErrors() {
		t.Fatalf("diagnostics: %+v", diagnostics)
	}
	second, diagnostics := Compile(doc, catalog)
	if diagnostics.HasErrors() || first.Digest != second.Digest {
		t.Fatalf("non-deterministic: %s %s %+v", first.Digest, second.Digest, diagnostics)
	}
}

func TestCompileRejectsForwardAndMissingReferences(t *testing.T) {
	doc, err := Decode(strings.NewReader(strings.Replace(validFlow, "inputs.name", "steps.later.output.name", 1)))
	if err != nil {
		t.Fatal(err)
	}
	catalog := NewMemoryCatalog()
	catalog.Activities["activity:test.echo@1"] = activity.Spec{Name: "test.echo", Version: "1", Effect: activity.EffectPure}
	catalog.Schemas["schema:string@1"] = &Schema{Type: Types{"string"}}
	if ir, diagnostics := Compile(doc, catalog); ir != nil || !diagnostics.HasErrors() {
		t.Fatalf("expected errors: %#v %+v", ir, diagnostics)
	}
}

func TestCompileRejectsUnknownPinnedOutputPath(t *testing.T) {
	doc, err := Decode(strings.NewReader(`{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"paths","version":"1"},"steps":[{"id":"first","uses":"activity:test.echo@1"},{"id":"second","uses":"activity:test.echo@1","with":{"value":{"$ref":"steps.first.output.missing"}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	deny := false
	catalog := NewMemoryCatalog()
	catalog.Schemas["schema:output@1"] = &Schema{Type: Types{"object"}, Properties: map[string]*Schema{"known": {Type: Types{"string"}}}, AdditionalProperties: &deny}
	catalog.Activities["activity:test.echo@1"] = activity.Spec{Name: "test.echo", Version: "1", Effect: activity.EffectPure, OutputSchema: "schema:output@1"}
	if ir, diagnostics := Compile(doc, catalog); ir != nil || !diagnostics.HasErrors() {
		t.Fatalf("expected output-path diagnostics: ir=%+v diagnostics=%+v", ir, diagnostics)
	}
}
