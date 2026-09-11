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

	"github.com/collectiveai-team/orquesta-lite/internal/buildinfo"
	"github.com/collectiveai-team/orquesta-lite/internal/packstate"
)

// withVersion pretends this is a released binary for the duration of one test.
func withVersion(t *testing.T, version string) {
	t.Helper()
	previous := buildinfo.Version
	buildinfo.Version = version
	t.Cleanup(func() { buildinfo.Version = previous })
}

func initProject(t *testing.T, version string) string {
	t.Helper()
	withVersion(t, version)
	dir := t.TempDir()
	if err := InitWithOptions(dir, InitOptions{Lang: "go"}); err != nil {
		t.Fatalf("init: %v", err)
	}
	return dir
}

func installedPackRoot(dir string) string {
	return filepath.Join(dir, ".orquestalite", "packs", "development", builtinPackVersion)
}

func TestInitStampsThePackWithTheRunningVersion(t *testing.T) {
	dir := initProject(t, "v0.7.0")
	stamp := packstate.Load(dir, "development", builtinPackVersion)
	if stamp.InstalledFrom != "v0.7.0" {
		t.Fatalf("init must record the version that wrote the pack, got %q", stamp.InstalledFrom)
	}
}

func TestPackSyncRestoresAPackAnOlderVersionLeftBehind(t *testing.T) {
	dir := initProject(t, "v0.6.1")
	target := filepath.Join(installedPackRoot(dir), "prompts", "coder.md")
	if err := os.WriteFile(target, []byte("stale content from an older release\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withVersion(t, "v0.7.0")
	var out bytes.Buffer
	if err := PackCLI(context.Background(), dir, []string{"sync", "development"}, &out); err != nil {
		t.Fatalf("pack sync: %v", err)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "stale content") {
		t.Fatalf("sync did not overwrite the stale file")
	}
	if stamp := packstate.Load(dir, "development", builtinPackVersion); stamp.InstalledFrom != "v0.7.0" || stamp.Acknowledged != "v0.7.0" {
		t.Fatalf("sync must stamp both fields, got %+v", stamp)
	}
	if !strings.Contains(out.String(), "prompts/coder.md") {
		t.Fatalf("sync must report what it changed, got %q", out.String())
	}
}

func TestPackSyncDryRunReportsWithoutWriting(t *testing.T) {
	dir := initProject(t, "v0.6.1")
	target := filepath.Join(installedPackRoot(dir), "prompts", "coder.md")
	if err := os.WriteFile(target, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withVersion(t, "v0.7.0")
	var out bytes.Buffer
	if err := PackCLI(context.Background(), dir, []string{"sync", "development", "--dry-run"}, &out); err != nil {
		t.Fatalf("pack sync --dry-run: %v", err)
	}
	body, _ := os.ReadFile(target)
	if string(body) != "stale\n" {
		t.Fatalf("dry run wrote to disk")
	}
	if stamp := packstate.Load(dir, "development", builtinPackVersion); stamp.InstalledFrom != "v0.6.1" {
		t.Fatalf("dry run moved the stamp: %+v", stamp)
	}
}

func TestPackSyncLeavesAPackThatStillVerifies(t *testing.T) {
	// The whole point of overwriting is to restore a loadable pack; a sync that
	// produced a pack flow.LoadPack rejects would break every later run.
	dir := initProject(t, "v0.6.1")
	if err := os.WriteFile(filepath.Join(installedPackRoot(dir), "prompts", "coder.md"), []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withVersion(t, "v0.7.0")
	if err := PackCLI(context.Background(), dir, []string{"sync", "development"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("pack sync: %v", err)
	}
	if err := packVerifies(installedPackRoot(dir)); err != nil {
		t.Fatalf("pack does not verify after sync: %v", err)
	}
}

func TestPackKeepStampsAcknowledgedWithoutTouchingFiles(t *testing.T) {
	dir := initProject(t, "v0.6.1")
	target := filepath.Join(installedPackRoot(dir), "prompts", "coder.md")
	if err := os.WriteFile(target, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withVersion(t, "v0.7.0")
	if err := PackCLI(context.Background(), dir, []string{"keep", "development"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("pack keep: %v", err)
	}
	body, _ := os.ReadFile(target)
	if string(body) != "stale\n" {
		t.Fatalf("keep modified the pack")
	}
	stamp := packstate.Load(dir, "development", builtinPackVersion)
	if stamp.Acknowledged != "v0.7.0" {
		t.Fatalf("keep must acknowledge this version, got %q", stamp.Acknowledged)
	}
	if stamp.InstalledFrom != "v0.6.1" {
		t.Fatalf("keep must not claim the files came from this version, got %q", stamp.InstalledFrom)
	}
}

// stalePack rewrites one pack file and regenerates pack.json so the result is a
// coherent older pack — exactly what a project carries after an orq-lite update
// that changed the embedded copy. A merely corrupted pack would fail digest
// verification first and never reach the drift check.
func stalePack(t *testing.T, dir string) {
	t.Helper()
	root := installedPackRoot(dir)
	if err := os.WriteFile(filepath.Join(root, "prompts", "coder.md"), []byte("an older release's coder prompt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := regeneratePackManifest(root); err != nil {
		t.Fatal(err)
	}
	if err := packVerifies(root); err != nil {
		t.Fatalf("the simulated stale pack must still verify: %v", err)
	}
}

func TestFlowRunBlocksOnAPackAnOlderVersionInstalled(t *testing.T) {
	dir := initProject(t, "v0.6.1")
	stalePack(t, dir)
	withVersion(t, "v0.7.0")
	var out bytes.Buffer
	err := FlowCLI(context.Background(), dir, []string{"run", "development/factory-fast@1"}, &out)
	if err == nil {
		t.Fatal("a stale pack must stop the run before it exists")
	}
	for _, want := range []string{"v0.6.1", "v0.7.0", "prompts/coder.md", "pack sync", "pack keep", "--accept-pack-drift"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message must name %q so an agent can act on it:\n%s", want, err.Error())
		}
	}
}

// compileBuiltin resolves a pack flow the way `flow run` does, so the drift
// check under test receives exactly what the command would hand it.
func compileBuiltin(t *testing.T, dir string) *compiledWorkflow {
	t.Helper()
	compiled, err := compileWorkflowTarget(dir, "development/factory-fast@1")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return compiled
}

func TestAcceptingDriftForOneRunWritesNothing(t *testing.T) {
	dir := initProject(t, "v0.6.1")
	stalePack(t, dir)
	withVersion(t, "v0.7.0")
	if err := checkPackDrift(dir, compileBuiltin(t, dir), true); err != nil {
		t.Fatalf("--accept-pack-drift must bypass the block: %v", err)
	}
	if stamp := packstate.Load(dir, "development", builtinPackVersion); stamp.Acknowledged != "" || stamp.InstalledFrom != "v0.6.1" {
		t.Fatalf("accepting drift for one run must record nothing, got %+v", stamp)
	}
}

func TestPackKeepSilencesTheBlock(t *testing.T) {
	dir := initProject(t, "v0.6.1")
	stalePack(t, dir)
	withVersion(t, "v0.7.0")
	if err := PackCLI(context.Background(), dir, []string{"keep", "development"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := checkPackDrift(dir, compileBuiltin(t, dir), false); err != nil {
		t.Fatalf("keep must silence the block: %v", err)
	}
}

func TestNoFileChangedRestampsSilently(t *testing.T) {
	// A release that does not touch the pack must not interrupt anyone.
	dir := initProject(t, "v0.6.1")
	withVersion(t, "v0.7.0")
	if err := checkPackDrift(dir, compileBuiltin(t, dir), false); err != nil {
		t.Fatalf("an untouched pack must not block: %v", err)
	}
	if stamp := packstate.Load(dir, "development", builtinPackVersion); stamp.InstalledFrom != "v0.7.0" {
		t.Fatalf("want a silent restamp to v0.7.0, got %+v", stamp)
	}
}

func TestDevelopmentBuildNeverBlocks(t *testing.T) {
	dir := initProject(t, "v0.6.1")
	stalePack(t, dir)
	withVersion(t, "dev")
	if err := checkPackDrift(dir, compileBuiltin(t, dir), false); err != nil {
		t.Fatalf("a dev build must not gate anyone's run: %v", err)
	}
}

// A pack the user forked under another name is theirs: this binary ships no
// newer copy of it and must not offer to overwrite one.
func TestAForkedPackIsNeverChecked(t *testing.T) {
	dir := initProject(t, "v0.6.1")
	stalePack(t, dir)
	withVersion(t, "v0.7.0")
	compiled := compileBuiltin(t, dir)
	compiled.IR.Pack.Name = "my-fork"
	if err := checkPackDrift(dir, compiled, false); err != nil {
		t.Fatalf("a pack this binary does not ship must not be checked: %v", err)
	}
}

// regeneratePackManifest rewrites pack.json from the files actually on disk.
// It is a test utility: production never rewrites a manifest, it copies the one
// the embedded pack ships with.
func regeneratePackManifest(root string) error {
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		if relative == "pack.json" {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(body)
		files[relative] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		return err
	}
	manifest, err := json.MarshalIndent(map[string]any{
		"apiVersion": "orq.pack/v1",
		"name":       builtinPackName,
		"version":    builtinPackVersion,
		"files":      files,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "pack.json"), manifest, 0o644)
}
