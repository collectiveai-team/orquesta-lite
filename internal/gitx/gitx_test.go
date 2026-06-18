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

func TestUntrackedFiles_ExcludesIgnoredAndTracked(t *testing.T) {
	gitOrSkip(t)
	dir := initRepo(t)
	_ = os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored.txt\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("t"), 0o644)
	_, _ = CommitAll(dir, "add tracked + gitignore")
	_ = os.WriteFile(filepath.Join(dir, "new.txt"), []byte("n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("i"), 0o644)

	got, err := UntrackedFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "new.txt" {
		t.Errorf("UntrackedFiles = %v, want [new.txt] (tracked + ignored excluded)", got)
	}
}

func TestResetHard_RevertsModificationAndRestoresDeletion(t *testing.T) {
	gitOrSkip(t)
	dir := initRepo(t)
	p := filepath.Join(dir, "a.txt")
	_ = os.WriteFile(p, []byte("a"), 0o644)
	base, _ := CommitAll(dir, "add a")
	_ = os.WriteFile(p, []byte("DIRTY"), 0o644) // modify
	_ = os.Remove(filepath.Join(dir, "a.txt"))  // delete then re-modify scenario
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("DIRTY"), 0o644)

	if err := ResetHard(dir, base); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if string(raw) != "a" {
		t.Errorf("reset --hard did not restore tracked file: %q", raw)
	}
}

// TestRollbackTo is the core safety guarantee: a failed task reverts the agent's
// tracked edits and removes the untracked files it created, while leaving
// pre-existing untracked user files (captured in keepUntracked) untouched.
func TestRollbackTo_RemovesAgentFilesButKeepsUserFiles(t *testing.T) {
	gitOrSkip(t)
	dir := initRepo(t)
	tracked := filepath.Join(dir, "src.go")
	_ = os.WriteFile(tracked, []byte("original\n"), 0o644)
	_, _ = CommitAll(dir, "seed src.go")
	base, _ := HeadSHA(dir)

	// Pre-existing untracked user file (present at task start).
	userFile := filepath.Join(dir, "notes.txt")
	_ = os.WriteFile(userFile, []byte("my notes"), 0o644)
	snap, err := UntrackedFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	keep := map[string]bool{}
	for _, f := range snap {
		keep[f] = true
	}

	// Agent does work: modifies a tracked file and creates a new untracked file
	// in a new directory.
	_ = os.WriteFile(tracked, []byte("agent broke this\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "pkg"), 0o755)
	agentFile := filepath.Join(dir, "pkg", "agent_new.go")
	_ = os.WriteFile(agentFile, []byte("junk"), 0o644)

	if err := RollbackTo(dir, base, keep); err != nil {
		t.Fatal(err)
	}

	if raw, _ := os.ReadFile(tracked); string(raw) != "original\n" {
		t.Errorf("tracked file not reverted: %q", raw)
	}
	if _, err := os.Stat(agentFile); !os.IsNotExist(err) {
		t.Errorf("agent-created untracked file should be removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "pkg")); !os.IsNotExist(err) {
		t.Errorf("empty dir left by agent file should be pruned")
	}
	if raw, err := os.ReadFile(userFile); err != nil || string(raw) != "my notes" {
		t.Errorf("pre-existing untracked user file must survive rollback: err=%v content=%q", err, raw)
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
