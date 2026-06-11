package gitx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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
