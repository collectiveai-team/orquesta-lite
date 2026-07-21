package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeInstallablePack(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	flowBody := []byte(`{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"noop","version":"1"},"spec":{"inputs":{},"outputs":{},"steps":[]}}`)
	if err := os.MkdirAll(filepath.Join(src, "flows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "flows", "noop@1.json"), flowBody, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(flowBody)
	manifest, err := json.Marshal(map[string]any{
		"apiVersion": "orq.pack/v1",
		"name":       "development",
		"version":    "1",
		"files":      map[string]string{"flows/noop@1.json": hex.EncodeToString(sum[:])},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "pack.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	return src
}

func TestPackInstall_CopiesVerifiedPack(t *testing.T) {
	src := writeInstallablePack(t)
	project := t.TempDir()
	var out bytes.Buffer
	if err := PackCLI(context.Background(), project, []string{"install", src}, &out); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(project, ".orquestalite", "packs", "development", "1")
	for _, rel := range []string{"pack.json", filepath.Join("flows", "noop@1.json")} {
		if _, err := os.Stat(filepath.Join(installed, rel)); err != nil {
			t.Fatalf("missing installed file %s: %v", rel, err)
		}
	}
	if !strings.Contains(out.String(), "installed development@1") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestPackInstall_RefusesOverwriteWithoutForce(t *testing.T) {
	src := writeInstallablePack(t)
	project := t.TempDir()
	if err := PackCLI(context.Background(), project, []string{"install", src}, io.Discard); err != nil {
		t.Fatal(err)
	}
	err := PackCLI(context.Background(), project, []string{"install", src}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected already-installed error mentioning --force, got %v", err)
	}
	if err := PackCLI(context.Background(), project, []string{"install", src, "--force"}, io.Discard); err != nil {
		t.Fatalf("force reinstall failed: %v", err)
	}
}

func TestPackInstall_RejectsTamperedPack(t *testing.T) {
	src := writeInstallablePack(t)
	if err := os.WriteFile(filepath.Join(src, "flows", "noop@1.json"), []byte(`{"tampered":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	err := PackCLI(context.Background(), project, []string{"install", src}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected digest mismatch, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(project, ".orquestalite", "packs", "development")); !os.IsNotExist(statErr) {
		t.Fatalf("tampered pack must not leave files behind")
	}
}
