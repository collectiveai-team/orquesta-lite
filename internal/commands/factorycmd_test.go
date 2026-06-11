package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/factory"
)

func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func writeFeatures(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "features.md")
	md := "## Feature one\n\nbody one\n\n## Feature two\n\nbody two\n"
	if err := os.WriteFile(p, []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFactory_CreatesQueueFromFeaturesFile(t *testing.T) {
	dir := initTestRepo(t)
	featuresPath := writeFeatures(t, dir)

	q, err := loadOrCreateQueue(FactoryOptions{ProjectDir: dir, FeaturesPath: featuresPath})
	if err != nil {
		t.Fatal(err)
	}
	if q.BaseBranch != "main" || len(q.Features) != 2 {
		t.Fatalf("queue = %+v", q)
	}
	// State must be persisted.
	loaded, err := factory.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Features) != 2 {
		t.Fatalf("persisted queue = %+v", loaded)
	}
}

func TestFactory_RefusesToReplaceUnfinishedQueueWithoutForce(t *testing.T) {
	dir := initTestRepo(t)
	featuresPath := writeFeatures(t, dir)

	if _, err := loadOrCreateQueue(FactoryOptions{ProjectDir: dir, FeaturesPath: featuresPath}); err != nil {
		t.Fatal(err)
	}
	_, err := loadOrCreateQueue(FactoryOptions{ProjectDir: dir, FeaturesPath: featuresPath})
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected --force guidance, got %v", err)
	}
	if _, err := loadOrCreateQueue(FactoryOptions{ProjectDir: dir, FeaturesPath: featuresPath, Force: true}); err != nil {
		t.Fatalf("--force should replace: %v", err)
	}
}

func TestFactory_ResumeWithoutQueueFails(t *testing.T) {
	dir := initTestRepo(t)
	_, err := loadOrCreateQueue(FactoryOptions{ProjectDir: dir})
	if err == nil {
		t.Fatal("expected error when resuming without a queue")
	}
}

func TestFactory_RequiresCleanTree(t *testing.T) {
	dir := initTestRepo(t)
	featuresPath := writeFeatures(t, dir) // untracked file -> dirty tree

	err := Factory(context.Background(), FactoryOptions{ProjectDir: dir, FeaturesPath: featuresPath})
	if err == nil || !strings.Contains(err.Error(), "clean work tree") {
		t.Fatalf("expected clean-tree error, got %v", err)
	}
}

func TestFactory_StatusOutput(t *testing.T) {
	dir := initTestRepo(t)
	featuresPath := writeFeatures(t, dir)
	if _, err := loadOrCreateQueue(FactoryOptions{ProjectDir: dir, FeaturesPath: featuresPath}); err != nil {
		t.Fatal(err)
	}

	var sb strings.Builder
	if err := Factory(context.Background(), FactoryOptions{ProjectDir: dir, StatusOnly: true, Out: &sb}); err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	for _, want := range []string{"base branch: main", "F001", "factory/002-feature-two", "Feature one"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
}
