package flow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
)

func TestDirectoryCatalogResolvesInstalledPack(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"flows", "schemas", "policies"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "flows", "demo@1.json"), []byte(`{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"demo","version":"1"},"steps":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "schemas", "any@1.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := NewDirectoryCatalog(root, []activity.Spec{{Name: "command.run", Version: "1", Effect: activity.EffectAtMostOnce}})
	ref, _ := ParseResourceRef("flow:demo@1")
	if _, _, err := catalog.ResolveDocument(ref); err != nil {
		t.Fatal(err)
	}
	ref, _ = ParseResourceRef("schema:any@1")
	if _, _, err := catalog.ResolveSchema(ref); err != nil {
		t.Fatal(err)
	}
	ref, _ = ParseResourceRef("activity:command.run@1")
	if _, err := catalog.ResolveActivity(ref); err != nil {
		t.Fatal(err)
	}
}
