package commands

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/factory"
	"github.com/lionelchamorro/orquestalite/internal/gitx"
	"github.com/lionelchamorro/orquestalite/internal/results"
)

// headingSplitExtractor stands in for the live LLM planner in tests: it builds
// features from the markdown headings, so loadOrCreateQueue is exercised without
// invoking a real agent.
func headingSplitExtractor(_ context.Context, _ FactoryOptions, md string) ([]factory.Feature, error) {
	return factory.ParseFeatures(md), nil
}

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

	q, err := loadOrCreateQueue(context.Background(), FactoryOptions{ProjectDir: dir, FeaturesPath: featuresPath}, headingSplitExtractor)
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

	if _, err := loadOrCreateQueue(context.Background(), FactoryOptions{ProjectDir: dir, FeaturesPath: featuresPath}, headingSplitExtractor); err != nil {
		t.Fatal(err)
	}
	_, err := loadOrCreateQueue(context.Background(), FactoryOptions{ProjectDir: dir, FeaturesPath: featuresPath}, headingSplitExtractor)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected --force guidance, got %v", err)
	}
	if _, err := loadOrCreateQueue(context.Background(), FactoryOptions{ProjectDir: dir, FeaturesPath: featuresPath, Force: true}, headingSplitExtractor); err != nil {
		t.Fatalf("--force should replace: %v", err)
	}
}

func TestFactory_ResumeWithoutQueueFails(t *testing.T) {
	dir := initTestRepo(t)
	_, err := loadOrCreateQueue(context.Background(), FactoryOptions{ProjectDir: dir}, headingSplitExtractor)
	if err == nil {
		t.Fatal("expected error when resuming without a queue")
	}
}

func TestFactory_AutoDiscoversFeatureFile(t *testing.T) {
	dir := initTestRepo(t)
	// Drop a feature.md in the root; no FeaturesPath, no existing queue.
	if err := os.WriteFile(filepath.Join(dir, "feature.md"),
		[]byte("## Build the thing\n\nmake it work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	q, err := loadOrCreateQueue(context.Background(), FactoryOptions{ProjectDir: dir, Out: io.Discard}, headingSplitExtractor)
	if err != nil {
		t.Fatalf("expected auto-discovery to succeed, got %v", err)
	}
	if len(q.Features) != 1 || q.Features[0].Title != "Build the thing" {
		t.Fatalf("queue = %+v", q)
	}
}

func TestDiscoverFeaturesFile(t *testing.T) {
	if got := discoverFeaturesFile(t.TempDir()); got != "" {
		t.Fatalf("empty dir should yield no file, got %q", got)
	}
	// Use "goal.md" specifically: it sits in its own pair, so a case-insensitive
	// filesystem (macOS) can't satisfy an earlier candidate and skew the result.
	dir := t.TempDir()
	want := filepath.Join(dir, "goal.md")
	if err := os.WriteFile(want, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := discoverFeaturesFile(dir); got != want {
		t.Fatalf("discoverFeaturesFile = %q, want %q", got, want)
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

func TestCheckpointResidue_PreservesWorkAndCleansTree(t *testing.T) {
	dir := initTestRepo(t)
	d := &liveFactoryDeps{dir: dir, out: io.Discard}
	feat := factory.Feature{ID: "F001", Title: "Feature one", Branch: "factory/001-feature-one"}

	// Clean tree: no-op, no commit.
	if checkpointed, err := d.CheckpointResidue(feat); err != nil || checkpointed {
		t.Fatalf("clean tree: checkpointed=%v err=%v, want (false, nil)", checkpointed, err)
	}

	// Residue from a half-done task: a modified tracked file + a new file.
	tracked := filepath.Join(dir, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("git", "add", "tracked.txt")
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	c = exec.Command("git", "commit", "-m", "seed tracked")
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	if err := os.WriteFile(tracked, []byte("half-done edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	checkpointed, err := d.CheckpointResidue(feat)
	if err != nil || !checkpointed {
		t.Fatalf("dirty tree: checkpointed=%v err=%v, want (true, nil)", checkpointed, err)
	}

	// Tree is now clean (so a base checkout would not abort) ...
	clean, err := gitx.IsCleanTree(dir)
	if err != nil || !clean {
		t.Fatalf("tree clean=%v err=%v, want clean after checkpoint", clean, err)
	}
	// ... and the residue is preserved in a labelled wip commit on this branch.
	logOut := exec.Command("git", "log", "-1", "--pretty=%s")
	logOut.Dir = dir
	subject, err := logOut.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(subject), "wip(orq-lite): checkpoint residue") || !strings.Contains(string(subject), "F001") {
		t.Errorf("checkpoint commit subject = %q, want labelled wip commit for F001", subject)
	}
	if b, _ := os.ReadFile(tracked); string(b) != "half-done edit" {
		t.Errorf("residue not preserved: tracked.txt = %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); err != nil {
		t.Errorf("new untracked residue should be committed, not lost: %v", err)
	}
}

func TestCopyFileAtomic_CopiesAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.json")
	dst := filepath.Join(dir, "dst.json")
	if err := os.WriteFile(src, []byte(`{"v":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyFileAtomic(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != `{"v":1}` {
		t.Errorf("dst = %q", b)
	}
	// Overwrite an existing dst, and leave no .tmp behind.
	if err := os.WriteFile(src, []byte(`{"v":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyFileAtomic(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != `{"v":2}` {
		t.Errorf("dst after overwrite = %q", b)
	}
	if fileExists(dst + ".tmp") {
		t.Error("temp file should be renamed away, not left behind")
	}
}

func TestPlanTextWithCriteria_FoldsCriteriaAndFiles(t *testing.T) {
	got := planTextWithCriteria(results.PlannerFeature{
		Plan:               "Add the login endpoint.",
		AcceptanceCriteria: []string{"POST /login returns 200", "invalid creds return 401"},
		FilesLikelyTouched: []string{"api/auth.py"},
	})
	for _, want := range []string{
		"Add the login endpoint.",
		"## Acceptance Criteria",
		"- POST /login returns 200",
		"## Files Likely Touched",
		"- api/auth.py",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("folded plan missing %q:\n%s", want, got)
		}
	}
}

func TestFactory_StatusOutput(t *testing.T) {
	dir := initTestRepo(t)
	featuresPath := writeFeatures(t, dir)
	if _, err := loadOrCreateQueue(context.Background(), FactoryOptions{ProjectDir: dir, FeaturesPath: featuresPath}, headingSplitExtractor); err != nil {
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
