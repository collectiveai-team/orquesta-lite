package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitOrSkip(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@test"},
		{"config", "user.name", "test"},
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

func TestIsCleanTree_TrueOnFreshRepo(t *testing.T) {
	gitOrSkip(t)
	dir := initRepo(t)
	clean, err := IsCleanTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Errorf("expected clean tree")
	}
}

func TestIsCleanTree_FalseAfterModification(t *testing.T) {
	gitOrSkip(t)
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	clean, _ := IsCleanTree(dir)
	if clean {
		t.Errorf("expected dirty tree")
	}
}

func TestCommitAll_CreatesCommit(t *testing.T) {
	gitOrSkip(t)
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, err := CommitAll(dir, "feat: add a")
	if err != nil {
		t.Fatal(err)
	}
	if len(sha) < 7 {
		t.Errorf("sha too short: %q", sha)
	}
}

func TestCheckoutAll_DiscardsChanges(t *testing.T) {
	gitOrSkip(t)
	dir := initRepo(t)
	p := filepath.Join(dir, "a.txt")
	_ = os.WriteFile(p, []byte("a"), 0o644)
	_, _ = CommitAll(dir, "add a")
	_ = os.WriteFile(p, []byte("DIRTY"), 0o644)
	if err := CheckoutAll(dir); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if string(raw) != "a" {
		t.Errorf("checkout did not restore: %q", raw)
	}
}

func TestLogStatSinceHead(t *testing.T) {
	gitOrSkip(t)
	dir := initRepo(t)
	start, _ := HeadSHA(dir)
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	_, _ = CommitAll(dir, "add a")
	out, err := LogStat(dir, start)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "add a") {
		t.Errorf("log missing commit message: %q", out)
	}
}
