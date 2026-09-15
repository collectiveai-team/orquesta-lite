package workflow

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/flow"
)

// defaultsCatalog builds a parent flow that calls a subflow, so the fixtures
// below only have to vary the two documents.
func defaultsCatalog(t *testing.T, parent, sub string) (*flow.IR, *memoryCatalog) {
	t.Helper()
	subdoc, err := flow.Decode(strings.NewReader(sub))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := flow.Decode(strings.NewReader(parent))
	if err != nil {
		t.Fatal(err)
	}
	catalog := newMemoryCatalog()
	catalog.Schemas["schema:any@1"] = &flow.Schema{}
	catalog.Schemas["schema:text@1"] = &flow.Schema{Type: flow.Types{"string"}}
	catalog.Activities["activity:test.echo@1"] = activity.Spec{Name: "test.echo", Version: "1", Effect: activity.EffectIdempotent}
	catalog.Documents["subflow:sub@1"] = subdoc
	ir, diags := flow.Compile(doc, catalog)
	if diags.HasErrors() {
		t.Fatalf("compile: %+v", diags)
	}
	return ir, catalog
}

func runDefaults(t *testing.T, ir *flow.IR, catalog *memoryCatalog, inputs map[string]any) (*Run, *Store, error) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	registry := activity.NewRegistry()
	if err := registry.Register(&echoExecutor{}); err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{Store: store, Activities: registry, Catalog: catalog}
	run, err := runtime.Start(context.Background(), ir, StartOptions{RunID: "r", Inputs: inputs, Policy: DefaultPolicy()})
	return run, store, err
}

// captured reads back the label the echo activity was actually handed, which is
// the only evidence that the default reached the step rather than the resolver.
func captured(t *testing.T, store *Store, scope, stepID string) string {
	t.Helper()
	step, err := store.GetStep(context.Background(), "r", scope, stepID, "")
	if err != nil {
		t.Fatalf("get step %s/%s: %v", scope, stepID, err)
	}
	var output struct {
		Label string `json:"label"`
	}
	if err := json.Unmarshal(step.Output, &output); err != nil {
		t.Fatalf("decode %s output: %v", stepID, err)
	}
	return output.Label
}

const subflowWithDefault = `{"apiVersion":"orq.dev/v2","kind":"Subflow","metadata":{"name":"sub","version":"1"},"inputs":{"value":{"schema":"schema:any@1"},"label":{"schema":"schema:text@1","default":"fallback"}},"steps":[{"id":"capture","uses":"activity:test.echo@1","with":{"value":{"$ref":"inputs.value"},"label":{"$ref":"inputs.label"}}}],"outputs":{"label":{"$ref":"steps.capture.output.label"}}}`

// TestSubflowInputDefaultResolvesWhenCallSiteOmitsIt pins the bug that killed a
// factory-governed run 32 minutes in: develop-ticket@1 declared commit_type
// with a default, the call site did not pass it, and the step that referenced
// inputs.commit_type died with `reference "inputs.commit_type" not found`. A
// declared default has to produce a value, not just excuse a missing key.
func TestSubflowInputDefaultResolvesWhenCallSiteOmitsIt(t *testing.T) {
	parent := `{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"parent","version":"1"},"inputs":{"value":{"schema":"schema:any@1"}},"steps":[{"id":"child","uses":"subflow:sub@1","with":{"value":{"$ref":"inputs.value"}}}],"outputs":{"label":{"$ref":"steps.child.output.label"}}}`
	ir, catalog := defaultsCatalog(t, parent, subflowWithDefault)
	run, store, err := runDefaults(t, ir, catalog, map[string]any{"value": "v"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if run.Status != RunSucceeded {
		t.Fatalf("status=%s error=%s", run.Status, run.Error)
	}
	if got := captured(t, store, "root/child", "capture"); got != "fallback" {
		t.Fatalf("label=%q, want the declared default", got)
	}
}

// TestSubflowInputDefaultYieldsToTheCallSite keeps the default from shadowing an
// explicitly passed value — issue-fix@1 passes commit_type "fix" and must keep it.
func TestSubflowInputDefaultYieldsToTheCallSite(t *testing.T) {
	parent := `{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"parent","version":"1"},"inputs":{"value":{"schema":"schema:any@1"}},"steps":[{"id":"child","uses":"subflow:sub@1","with":{"value":{"$ref":"inputs.value"},"label":"explicit"}}],"outputs":{"label":{"$ref":"steps.child.output.label"}}}`
	ir, catalog := defaultsCatalog(t, parent, subflowWithDefault)
	_, store, err := runDefaults(t, ir, catalog, map[string]any{"value": "v"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if got := captured(t, store, "root/child", "capture"); got != "explicit" {
		t.Fatalf("label=%q, want the call site value", got)
	}
}

// TestTopLevelInputDefaultResolvesWithoutTheCLI covers the same gap one level up.
// It does not reproduce today only because the CLI pre-fills defaults before
// calling Start; any other caller (the web API, a test, an embedder) gets the
// dangling reference.
func TestTopLevelInputDefaultResolvesWithoutTheCLI(t *testing.T) {
	doc, err := flow.Decode(strings.NewReader(`{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"parent","version":"1"},"inputs":{"label":{"schema":"schema:text@1","default":"fallback"}},"steps":[{"id":"capture","uses":"activity:test.echo@1","with":{"label":{"$ref":"inputs.label"}}}],"outputs":{"label":{"$ref":"steps.capture.output.label"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	catalog := newMemoryCatalog()
	catalog.Schemas["schema:text@1"] = &flow.Schema{Type: flow.Types{"string"}}
	catalog.Activities["activity:test.echo@1"] = activity.Spec{Name: "test.echo", Version: "1", Effect: activity.EffectIdempotent}
	ir, diags := flow.Compile(doc, catalog)
	if diags.HasErrors() {
		t.Fatalf("compile: %+v", diags)
	}
	run, store, err := runDefaults(t, ir, catalog, map[string]any{})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if run.Status != RunSucceeded {
		t.Fatalf("status=%s error=%s", run.Status, run.Error)
	}
	if got := captured(t, store, "root", "capture"); got != "fallback" {
		t.Fatalf("label=%q, want the declared default", got)
	}
	// The persisted inputs are what a resume replays from, so a default the run
	// actually used has to be recorded there rather than re-derived by luck.
	var persisted map[string]any
	if err := json.Unmarshal(run.Inputs, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted["label"] != "fallback" {
		t.Fatalf("persisted inputs=%v, want the bound default", persisted)
	}
}

// TestInputDefaultIsCheckedAgainstItsSchema stops a pack from shipping a default
// that its own schema rejects. Before defaults were bound they were never
// validated at all, so the violation surfaced — if at all — as a type error deep
// inside whichever step happened to read it.
func TestInputDefaultIsCheckedAgainstItsSchema(t *testing.T) {
	sub := `{"apiVersion":"orq.dev/v2","kind":"Subflow","metadata":{"name":"sub","version":"1"},"inputs":{"value":{"schema":"schema:any@1"},"label":{"schema":"schema:text@1","default":7}},"steps":[{"id":"capture","uses":"activity:test.echo@1","with":{"value":{"$ref":"inputs.value"},"label":{"$ref":"inputs.label"}}}],"outputs":{"label":{"$ref":"steps.capture.output.label"}}}`
	parent := `{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"parent","version":"1"},"inputs":{"value":{"schema":"schema:any@1"}},"steps":[{"id":"child","uses":"subflow:sub@1","with":{"value":{"$ref":"inputs.value"}}}],"outputs":{"label":{"$ref":"steps.child.output.label"}}}`
	ir, catalog := defaultsCatalog(t, parent, sub)
	run, _, err := runDefaults(t, ir, catalog, map[string]any{"value": "v"})
	if err == nil && run.Status == RunSucceeded {
		t.Fatal("a default that violates its own schema was accepted")
	}
	message := run.Error
	if err != nil {
		message = err.Error()
	}
	// Naming the input is not enough: before defaults were bound the run also
	// failed mentioning "label", but for the unrelated reason that the reference
	// dangled. The schema complaint is what proves the default was checked.
	if !strings.Contains(message, "label") || !strings.Contains(message, "expected string") {
		t.Fatalf("error %q is not a schema rejection of the default", message)
	}
}
