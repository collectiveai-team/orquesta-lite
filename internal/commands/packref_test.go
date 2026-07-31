package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installFixturePack writes a minimal valid pack straight into the install
// root, so a test can set up several versions without going through install.
func installFixturePack(t *testing.T, projectDir, name, version string, extraFiles map[string]string) string {
	t.Helper()
	root := filepath.Join(projectDir, ".orquestalite", "packs", name, version)
	files := map[string]string{
		"flows/probe@1.json": `{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"probe","version":"1"},"steps":[{"id":"noop","uses":"activity:command.run@1","with":{"argv":["/usr/bin/true"]}}],"outputs":{"exit":{"$ref":"steps.noop.output.exitCode"}}}`,
	}
	for path, body := range extraFiles {
		files[path] = body
	}
	digests := map[string]string{}
	for relative, body := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(body))
		digests[relative] = hex.EncodeToString(sum[:])
	}
	manifest, err := json.Marshal(map[string]any{"apiVersion": "orq.pack/v1", "name": name, "version": version, "files": digests})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "pack.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// A single installed version is the common case and must not change behavior.
func TestResolvePackFlowRefSingleVersionIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	want := installFixturePack(t, dir, "development", "1", nil)
	root, ref, err := resolvePackFlowRef(dir, "development/probe@1")
	if err != nil {
		t.Fatal(err)
	}
	if root != want || ref != "flow:probe@1" {
		t.Fatalf("root=%s ref=%s", root, ref)
	}
}

// The pack version and the flow version are now independent numbers: an
// unpinned ref takes the highest installed pack while keeping flow@1.
func TestResolvePackFlowRefUnpinnedTakesHighestInstalled(t *testing.T) {
	dir := t.TempDir()
	installFixturePack(t, dir, "development", "1", nil)
	installFixturePack(t, dir, "development", "2", nil)
	want := installFixturePack(t, dir, "development", "10", nil)
	root, ref, err := resolvePackFlowRef(dir, "development/probe@1")
	if err != nil {
		t.Fatal(err)
	}
	if root != want {
		t.Fatalf("root=%s, want the highest installed version %s", root, want)
	}
	if ref != "flow:probe@1" {
		t.Fatalf("the flow version must not follow the pack version: %s", ref)
	}
}

func TestResolvePackFlowRefHonorsAnExplicitPackPin(t *testing.T) {
	dir := t.TempDir()
	want := installFixturePack(t, dir, "development", "1", nil)
	installFixturePack(t, dir, "development", "4", nil)
	root, ref, err := resolvePackFlowRef(dir, "development@1/probe@1")
	if err != nil {
		t.Fatal(err)
	}
	if root != want || ref != "flow:probe@1" {
		t.Fatalf("root=%s ref=%s", root, ref)
	}
}

// Naming a version that is not installed must say what *is*, so the operator
// does not have to go spelunking in .orquestalite/packs.
func TestResolvePackFlowRefMissingPinListsInstalledVersions(t *testing.T) {
	dir := t.TempDir()
	installFixturePack(t, dir, "development", "1", nil)
	installFixturePack(t, dir, "development", "4", nil)
	_, _, err := resolvePackFlowRef(dir, "development@9/probe@1")
	if err == nil {
		t.Fatal("expected an error for an uninstalled pack version")
	}
	if !strings.Contains(err.Error(), "installed: 1, 4") {
		t.Fatalf("error must list installed versions: %v", err)
	}
}

func TestResolvePackFlowRefRejectsMalformedRefs(t *testing.T) {
	dir := t.TempDir()
	for _, ref := range []string{"development/probe", "development@/probe@1", "/probe@1", "development/@1"} {
		if _, _, err := resolvePackFlowRef(dir, ref); err == nil {
			t.Errorf("%q must be rejected", ref)
		}
	}
}

// End to end: the printed pack= line names the version that actually ran.
func TestFlowRunPrintsResolvedPackVersion(t *testing.T) {
	dir := t.TempDir()
	installFixturePack(t, dir, "development", "1", nil)
	installFixturePack(t, dir, "development", "4", nil)
	for ref, wantPack := range map[string]string{
		"development/probe@1":   "pack=development@4:",
		"development@1/probe@1": "pack=development@1:",
		"development@4/probe@1": "pack=development@4:",
	} {
		var out bytes.Buffer
		project := t.TempDir()
		installFixturePack(t, project, "development", "1", nil)
		installFixturePack(t, project, "development", "4", nil)
		if err := FlowCLI(context.Background(), project, []string{"run", ref}, &out); err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if !strings.Contains(out.String(), wantPack) {
			t.Errorf("%s: out=%s, want %s", ref, out.String(), wantPack)
		}
	}
}
