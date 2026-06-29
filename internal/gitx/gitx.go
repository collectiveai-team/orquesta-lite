package gitx

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNothingToCommit is returned by CommitAll when there is nothing staged to
// commit (the work tree is clean). Callers treat this as a no-op rather than a
// failure: it happens when a task's files were already produced by an earlier
// task (overlapping decomposition), so the desired end-state already exists.
var ErrNothingToCommit = errors.New("nothing to commit: work tree clean")

func run(dir string, args ...string) (string, error) {
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

func IsCleanTree(dir string) (bool, error) {
	out, err := run(dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out == "", nil
}

// IsRepo reports whether dir lies inside a git work tree. Returns false on
// any error (including "git not installed") so callers can treat absent-git
// as a soft signal rather than a hard failure.
func IsRepo(dir string) bool {
	c := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

func CommitAll(dir, message string) (string, error) {
	if _, err := run(dir, "add", "-A"); err != nil {
		return "", err
	}
	// `git diff --cached --quiet` exits 0 when nothing is staged. In that case a
	// commit would fail with "nothing to commit"; surface ErrNothingToCommit so
	// the caller can treat the no-op as done instead of a hard commit failure.
	if _, err := run(dir, "diff", "--cached", "--quiet"); err == nil {
		return "", ErrNothingToCommit
	}
	if _, err := run(dir, "commit", "-m", message); err != nil {
		return "", err
	}
	return HeadSHA(dir)
}

// UntrackedFiles returns the repo-relative paths of untracked files that are
// not covered by .gitignore — exactly the set `git clean -fd` would delete.
// Used to snapshot the work tree at task start so RollbackTo can tell the
// agent's new files apart from pre-existing user files.
func UntrackedFiles(dir string) ([]string, error) {
	out, err := run(dir, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// ResetHard reverts tracked files to sha: it discards staged and unstaged
// modifications and restores tracked deletions. Untracked files are left
// untouched.
func ResetHard(dir, sha string) error {
	_, err := run(dir, "reset", "--hard", sha)
	return err
}

// RollbackTo restores the work tree to the state a task found it in:
//   - tracked modifications and deletions are reverted via `reset --hard baseSHA`
//   - untracked files created since the task began are removed
//
// Untracked paths present in keepUntracked (the snapshot taken at task start)
// are preserved, so pre-existing user files and scratch work are never
// destroyed. Ignored files (.gitignore) are never touched. Empty directories
// left behind by removed files are pruned best-effort.
//
// A known limitation: an untracked file the agent *deleted* cannot be restored
// (it exists in no commit); this matches the previous `git clean` behaviour.
func RollbackTo(dir, baseSHA string, keepUntracked map[string]bool) error {
	if baseSHA != "" {
		if err := ResetHard(dir, baseSHA); err != nil {
			return err
		}
	}
	now, err := UntrackedFiles(dir)
	if err != nil {
		return err
	}
	for _, rel := range now {
		if keepUntracked[rel] {
			continue
		}
		abs := filepath.Join(dir, rel)
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			return err
		}
		pruneEmptyParents(dir, filepath.Dir(abs))
	}
	return nil
}

// pruneEmptyParents removes now-empty directories from leaf up to (but not
// including) root. It stops at the first non-empty directory: os.Remove only
// succeeds on empty dirs, so a populated parent ends the walk.
func pruneEmptyParents(root, leaf string) {
	for leaf != root && strings.HasPrefix(leaf, root) {
		if err := os.Remove(leaf); err != nil {
			return
		}
		leaf = filepath.Dir(leaf)
	}
}

func HeadSHA(dir string) (string, error) {
	return run(dir, "rev-parse", "HEAD")
}

func LogStat(dir, sinceSHA string) (string, error) {
	return run(dir, "log", sinceSHA+"..HEAD", "--stat")
}

// DiffRefs returns the full patch between two refs (the diff a PR review
// inspects). Uses `git diff base head` (two-dot: the net change on head
// relative to base) with no color so the diff is machine-injectable into a
// review prompt. Callers must validate the refs; a leading "-" would otherwise
// look like a flag to git.
func DiffRefs(dir, base, head string) (string, error) {
	return run(dir, "diff", "--no-color", base, head)
}

// ShowCommit returns one commit's message, diffstat, and full patch, as the
// dashboard surfaces per-task changes. The caller must validate sha (it is
// passed straight to git); a leading "-" would otherwise look like a flag.
func ShowCommit(dir, sha string) (string, error) {
	return run(dir, "show", "--no-color", "--stat", "--patch", "--format=fuller", sha)
}

// CurrentBranch returns the checked-out branch name, or an error in detached
// HEAD state (factory mode requires a named base branch).
func CurrentBranch(dir string) (string, error) {
	out, err := run(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if out == "HEAD" {
		return "", fmt.Errorf("detached HEAD: factory mode requires a named branch")
	}
	return out, nil
}

// BranchExists reports whether a local branch with the given name exists.
func BranchExists(dir, name string) bool {
	c := exec.Command("git", "rev-parse", "--verify", "refs/heads/"+name)
	c.Dir = dir
	return c.Run() == nil
}

// Checkout switches to an existing branch.
func Checkout(dir, name string) error {
	_, err := run(dir, "checkout", name)
	return err
}

// CheckoutNewBranch creates branch name starting at base and switches to it.
func CheckoutNewBranch(dir, name, base string) error {
	_, err := run(dir, "checkout", "-b", name, base)
	return err
}

// MergeFastForward checks out base and merges branch into it. It prefers a
// fast-forward; when base has diverged it falls back to a --no-ff merge commit.
// A --ff-only failure is treated as needing a merge commit and falls through to
// --no-ff; genuine git errors surface via the --no-ff attempt and abort.
// Returns the method used ("ff" or "no-ff"). On conflict it runs `git merge
// --abort` and returns an error, leaving base unchanged with a clean tree.
func MergeFastForward(dir, base, branch string) (string, error) {
	if err := Checkout(dir, base); err != nil {
		return "", err
	}
	if _, err := run(dir, "merge", "--ff-only", branch); err == nil {
		return "ff", nil
	}
	if _, err := run(dir, "merge", "--no-ff", "--no-edit", branch); err != nil {
		_, _ = run(dir, "merge", "--abort")
		return "", fmt.Errorf("merge %s into %s: %w", branch, base, err)
	}
	return "no-ff", nil
}
