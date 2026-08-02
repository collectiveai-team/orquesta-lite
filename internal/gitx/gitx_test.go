package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func testRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "base.txt")
	git(t, dir, "commit", "-q", "-m", "base")
	return dir
}

func TestRepoCleanAndWorktreeDiff(t *testing.T) {
	dir := testRepo(t)
	if !IsRepo(dir) {
		t.Fatal("expected repository")
	}
	if clean, err := IsCleanTree(dir); err != nil || !clean {
		t.Fatalf("clean=%v err=%v", clean, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diff, err := DiffWorktree(dir)
	if err != nil || !strings.Contains(diff, "new.txt") {
		t.Fatalf("diff=%q err=%v", diff, err)
	}
}

func TestDiffRefs(t *testing.T) {
	dir := testRepo(t)
	base := git(t, dir, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "base.txt")
	git(t, dir, "commit", "-q", "-m", "change")
	head := git(t, dir, "rev-parse", "HEAD")
	diff, err := DiffRefs(dir, base, head)
	if err != nil || !strings.Contains(diff, "changed") {
		t.Fatalf("diff=%q err=%v", diff, err)
	}
}
