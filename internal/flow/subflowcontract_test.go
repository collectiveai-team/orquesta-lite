package flow

import (
	"strings"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
)

// subflowCatalog compiles a parent flow against one subflow document.
func subflowCatalog(t *testing.T, parent, sub string) Diagnostics {
	t.Helper()
	subdoc, err := Decode(strings.NewReader(sub))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Decode(strings.NewReader(parent))
	if err != nil {
		t.Fatal(err)
	}
	catalog := NewMemoryCatalog()
	catalog.Schemas["schema:text@1"] = &Schema{Type: Types{"string"}}
	catalog.Activities["activity:test.echo@1"] = activity.Spec{Name: "test.echo", Version: "1", Effect: activity.EffectPure}
	catalog.Documents["subflow:sub@1"] = subdoc
	_, diagnostics := Compile(doc, catalog)
	return diagnostics
}

func diagnosticsMention(diagnostics Diagnostics, needle string) bool {
	for _, item := range diagnostics {
		if item.Severity == "error" && strings.Contains(item.Message, needle) {
			return true
		}
	}
	return false
}

const contractSub = `{"apiVersion":"orq.dev/v2","kind":"Subflow","metadata":{"name":"sub","version":"1"},"inputs":{"value":{"schema":"schema:text@1"},"label":{"schema":"schema:text@1","default":"fallback"}},"steps":[{"id":"capture","uses":"activity:test.echo@1","with":{"value":{"$ref":"inputs.value"},"label":{"$ref":"inputs.label"}}}],"outputs":{}}`

// TestCompileRejectsSubflowCallSiteMissingRequiredInput moves "missing required
// input" from the middle of a long run to `flow validate`. A subflow's `with` is
// the whole scope its steps see, so an input absent there can never appear later.
func TestCompileRejectsSubflowCallSiteMissingRequiredInput(t *testing.T) {
	parent := `{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"parent","version":"1"},"steps":[{"id":"child","uses":"subflow:sub@1","with":{"label":"x"}}],"outputs":{}}`
	diagnostics := subflowCatalog(t, parent, contractSub)
	if !diagnosticsMention(diagnostics, "value") {
		t.Fatalf("expected a diagnostic naming the missing input: %+v", diagnostics)
	}
}

// TestCompileAcceptsSubflowCallSiteRelyingOnADefault is the counterpart: an
// omitted input that declares a default is supplied by the engine, so requiring
// it at the call site would break every flow that legitimately leans on one.
func TestCompileAcceptsSubflowCallSiteRelyingOnADefault(t *testing.T) {
	parent := `{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"parent","version":"1"},"steps":[{"id":"child","uses":"subflow:sub@1","with":{"value":"v"}}],"outputs":{}}`
	if diagnostics := subflowCatalog(t, parent, contractSub); diagnostics.HasErrors() {
		t.Fatalf("a defaulted input must not be required at the call site: %+v", diagnostics)
	}
}

// TestCompileRejectsSubflowCallSiteUndeclaredInput catches the typo that is
// worse than a crash: `commit_typ` is silently dropped, the subflow quietly uses
// its default, and the run succeeds doing the wrong thing.
func TestCompileRejectsSubflowCallSiteUndeclaredInput(t *testing.T) {
	parent := `{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"parent","version":"1"},"steps":[{"id":"child","uses":"subflow:sub@1","with":{"value":"v","labl":"typo"}}],"outputs":{}}`
	diagnostics := subflowCatalog(t, parent, contractSub)
	if !diagnosticsMention(diagnostics, "labl") {
		t.Fatalf("expected a diagnostic naming the undeclared input: %+v", diagnostics)
	}
}
