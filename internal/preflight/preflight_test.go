package preflight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

func task(desc string) *tasks.Task {
	return &tasks.Task{ID: "T001", Title: "t", Description: desc}
}

func TestCheck_DescriptionTooShort(t *testing.T) {
	v := Check(t.TempDir(), task("Fix bug"))
	if v.OK {
		t.Fatal("expected OK=false for short description")
	}
	if !strings.Contains(v.Reason, "too short") {
		t.Errorf("reason = %q, want to contain 'too short'", v.Reason)
	}
}

func TestCheck_DescriptionOK_NoFileRefs(t *testing.T) {
	// 60-character description with no file-like paths
	desc := "Refactor the authentication module to improve readability and test coverage."
	v := Check(t.TempDir(), task(desc))
	if !v.OK {
		t.Errorf("expected OK=true, got reason: %s", v.Reason)
	}
}

func TestCheck_FileReferenceExists(t *testing.T) {
	// internal/runner/runner.go exists in this repo; use the repo root as workdir.
	repoRoot := filepath.Join("..", "..")
	desc := "Update the runner in internal/runner/runner.go to handle timeouts more gracefully and add unit tests."
	v := Check(repoRoot, task(desc))
	if !v.OK {
		t.Errorf("expected OK=true (file exists), got reason: %s", v.Reason)
	}
}

func TestCheck_FileReferenceMissing(t *testing.T) {
	dir := t.TempDir()
	desc := "Implement the missing handler in does/not/exist.go to fix the routing issue in production deployments."
	v := Check(dir, task(desc))
	if v.OK {
		t.Fatal("expected OK=false for missing file reference")
	}
	if !strings.Contains(v.Reason, "does/not/exist.go") {
		t.Errorf("reason = %q, want to contain the missing path", v.Reason)
	}
}

func TestCheck_BareFilenameIgnored(t *testing.T) {
	// runner.go without a slash — must not be treated as a path reference.
	desc := "The runner.go file should be updated to handle the new timeout policy correctly across all environments."
	v := Check(t.TempDir(), task(desc))
	if !v.OK {
		t.Errorf("expected OK=true for bare filename (no slash), got reason: %s", v.Reason)
	}
}

func TestCheck_URLIgnored(t *testing.T) {
	// URL contains a slash-separated path segment; must not cause a false positive.
	desc := "See the documentation at https://example.com/foo.html for details on the configuration format and options."
	v := Check(t.TempDir(), task(desc))
	if !v.OK {
		t.Errorf("expected OK=true (URL should be ignored), got reason: %s", v.Reason)
	}
}

func TestCheck_FileReferenceExistsInTempDir(t *testing.T) {
	// Create a real file under the temp workdir and reference it in the description.
	dir := t.TempDir()
	subdir := filepath.Join(dir, "src", "app")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	desc := "Refactor the entry point in src/app/main.go to split the initialization logic into smaller functions."
	v := Check(dir, task(desc))
	if !v.OK {
		t.Errorf("expected OK=true (file was created), got reason: %s", v.Reason)
	}
}
