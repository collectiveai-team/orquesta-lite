package flow

import (
	"encoding/json"
	"os"
	"path/filepath"
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
