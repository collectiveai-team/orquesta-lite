package flow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/activity"
)

func TestLoadPackVerifiesDigests(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "flows"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte(`{}`)
	if err := os.WriteFile(filepath.Join(root, "flows", "demo.json"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := Pack{APIVersion: PackAPIVersion, Name: "development", Version: "1.0.0", Files: map[string]Digest{"flows/demo.json": digestBytes(content)}}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(root, "pack.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPack(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "flows", "demo.json"), []byte(`{"changed":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPack(root); err == nil {
		t.Fatal("expected digest mismatch")
	}
}

func TestPinPackChangesIRDigestAndDetectsManifestReplacement(t *testing.T) {
	ir := &IR{APIVersion: APIVersionV2, Kind: KindFlow, Metadata: Metadata{Name: "demo", Version: "1"}, Resources: map[string]Digest{}}
	pack := &Pack{APIVersion: PackAPIVersion, Name: "development", Version: "1", Files: map[string]Digest{"flows/demo.json": "abc"}}
	if err := PinPack(ir, pack); err != nil {
		t.Fatal(err)
	}
	if ir.Pack == nil || ir.Digest == "" || !ir.Pack.Matches(pack) {
		t.Fatalf("pinned IR = %+v", ir)
	}
	replaced := &Pack{APIVersion: PackAPIVersion, Name: "development", Version: "1", Files: map[string]Digest{"flows/demo.json": "changed"}}
	if ir.Pack.Matches(replaced) {
		t.Fatal("same-version content replacement must not match pinned pack")
	}
}

func TestDevelopmentFixturePackCompilesOffline(t *testing.T) {
	root := filepath.Join("testdata", "packs", "development-fixture")
	pack, err := LoadPack(root)
	if err != nil {
		t.Fatal(err)
	}
	catalog := NewDirectoryCatalog(root, []activity.Spec{
		{Name: "artifact.capture", Version: "1", Effect: activity.EffectIdempotent},
		{Name: "gate.run", Version: "1", Effect: activity.EffectPure},
		{Name: "command.run", Version: "1", Effect: activity.EffectAtMostOnce},
	})
	doc, _, err := catalog.ResolveDocument(ResourceRef{Kind: "flow", Name: "neutral-pipeline", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	ir, diagnostics := Compile(doc, catalog)
	if diagnostics.HasErrors() {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
	if err = PinPack(ir, pack); err != nil || ir.Pack == nil {
		t.Fatalf("pin err=%v ir=%+v", err, ir)
	}
}

func TestTicketedBenchmarkPackCompilesToDurableDynamicLoop(t *testing.T) {
	root := filepath.Join("..", "..", "benchmark", "packs", "development", "2")
	pack, err := LoadPack(root)
	if err != nil {
		t.Fatal(err)
	}
	catalog := NewDirectoryCatalog(root, []activity.Spec{
		{Name: "agent.invoke", Version: "1", Effect: activity.EffectAtMostOnce},
		{Name: "gate.run", Version: "1", Effect: activity.EffectPure},
	})
	doc, _, err := catalog.ResolveDocument(ResourceRef{Kind: "flow", Name: "factory-governed", Version: "2"})
	if err != nil {
		t.Fatal(err)
	}
	ir, diagnostics := Compile(doc, catalog)
	if diagnostics.HasErrors() {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
	if err = PinPack(ir, pack); err != nil {
		t.Fatal(err)
	}
	if ir.Pack == nil || ir.Pack.Name != "development" || ir.Pack.Version != "2" {
		t.Fatalf("pack snapshot=%+v", ir.Pack)
	}

	var loop *IRStep
	for index := range ir.Steps {
		if ir.Steps[index].ID == "develop_tickets" {
			loop = &ir.Steps[index]
			break
		}
	}
	if loop == nil || loop.While == nil || loop.Subflow == nil {
		t.Fatalf("develop_tickets must be a while over a pinned subflow: %+v", loop)
	}
	if loop.While.Condition != `item.state.status == "active"` || loop.While.MaxIterations != 20 {
		t.Fatalf("unexpected durable loop contract: %+v", loop.While)
	}
	if len(loop.Subflow.Steps) != 5 || loop.Subflow.Steps[0].ID != "implement_ticket" || loop.Subflow.Steps[1].ID != "verify_ticket" || loop.Subflow.Steps[4].ID != "update_ticket_plan" {
		t.Fatalf("unexpected ticket subflow steps: %+v", loop.Subflow.Steps)
	}
	var integratedReview *IRStep
	for index := range ir.Steps {
		if ir.Steps[index].ID == "integrated_review" {
			integratedReview = &ir.Steps[index]
			break
		}
	}
	if integratedReview == nil || integratedReview.Subflow == nil {
		t.Fatalf("factory must compose the integrated review subflow: %+v", integratedReview)
	}
	var qa, critic *IRStep
	for index := range integratedReview.Subflow.Steps {
		switch integratedReview.Subflow.Steps[index].ID {
		case "qa":
			qa = &integratedReview.Subflow.Steps[index]
		case "critic":
			critic = &integratedReview.Subflow.Steps[index]
		}
	}
	if qa == nil {
		t.Fatal("qa step is missing")
	}
	qaInputs, marshalErr := json.Marshal(qa.With)
	if marshalErr != nil || !strings.Contains(string(qaInputs), `"fallbackOutput"`) {
		t.Fatalf("qa must degrade to a validated fail-closed output: %+v", qa)
	}
	if critic == nil {
		t.Fatal("critic step is missing")
	}
	if _, ok := critic.With["context"]; !ok {
		t.Fatal("critic must receive structured QA output through context, not string vars")
	}
	criticInputs, marshalErr := json.Marshal(critic.With)
	if marshalErr != nil || !strings.Contains(string(criticInputs), `"fallbackOutput"`) || !strings.Contains(string(criticInputs), `steps.qa.output`) {
		t.Fatalf("critic must degrade to a validated output while consuming QA evidence: %+v", critic)
	}
	reviewDoc, _, resolveErr := catalog.ResolveDocument(ResourceRef{Kind: "flow", Name: "review-existing", Version: "2"})
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	reviewIR, reviewDiagnostics := Compile(reviewDoc, catalog)
	if reviewDiagnostics.HasErrors() || len(reviewIR.Steps) != 1 || reviewIR.Steps[0].Subflow == nil {
		t.Fatalf("review-existing must compile to the same reusable subflow: diagnostics=%+v ir=%+v", reviewDiagnostics, reviewIR)
	}
	for path, expected := range map[string]string{
		"prompts/ticket-planner.md": ".orquestalite/results/ticket_planner.json",
		"prompts/coder.md":          ".orquestalite/results/coder.json",
		"prompts/ticket-qa.md":      ".orquestalite/results/ticket_qa.json",
	} {
		raw, readErr := os.ReadFile(filepath.Join(root, path))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(raw), "{{RESULT_PATH}}") || !strings.Contains(string(raw), expected) {
			t.Fatalf("%s must pin its durable result path %q", path, expected)
		}
	}
	stateSchema, _, schemaErr := catalog.ResolveSchema(ResourceRef{Kind: "schema", Name: "workflow-state", Version: "1"})
	if schemaErr != nil {
		t.Fatal(schemaErr)
	}
	structuredHistory := []byte(`{"status":"complete","revision":1,"summary":"done","next_ticket":null,"pending":[],"completed":[],"blocked":[],"risks":[],"history":[{"revision":1,"mode":"initial"}]}`)
	if err = stateSchema.ValidateJSON(structuredHistory); err != nil {
		t.Fatalf("workflow state must accept structured durable history: %v", err)
	}
}

func TestLoadPackRejectsUnlistedResource(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "flows"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte(`{}`)
	if err := os.WriteFile(filepath.Join(root, "flows", "listed.json"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "flows", "unlisted.json"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := Pack{APIVersion: PackAPIVersion, Name: "development", Version: "1", Files: map[string]Digest{"flows/listed.json": digestBytes(content)}}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(root, "pack.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPack(root); err == nil || !strings.Contains(err.Error(), "unlisted file") {
		t.Fatalf("expected unlisted file error, got %v", err)
	}
}

func TestLoadPackRejectsPortableTraversal(t *testing.T) {
	root := t.TempDir()
	manifest := Pack{APIVersion: PackAPIVersion, Name: "development", Version: "1", Files: map[string]Digest{`flows\..\outside.json`: digestBytes(nil)}}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(root, "pack.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPack(root); err == nil || !strings.Contains(err.Error(), "unsafe file path") {
		t.Fatalf("expected unsafe path error, got %v", err)
	}
}
