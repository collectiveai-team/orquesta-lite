package gitx

import (
	"fmt"
	"os/exec"
	"strings"
)

func run(dir string, args ...string) (string, error) {
	command := exec.Command("git", args...)
	command.Dir = dir
	out, err := command.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

func IsCleanTree(dir string) (bool, error) {
	out, err := run(dir, "status", "--porcelain")
	return out == "", err
}

func IsRepo(dir string) bool {
	command := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	command.Dir = dir
	out, err := command.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// DiffRefs returns the full patch between two validated refs.
func DiffRefs(dir, base, head string) (string, error) {
	return run(dir, "diff", "--no-color", base, head)
}

// DiffWorktree returns tracked and untracked pending changes versus HEAD. The
// intent-to-add operation makes new files visible without staging their bytes.
func DiffWorktree(dir string) (string, error) {
	if !IsRepo(dir) {
		return "", nil
	}
	if _, err := run(dir, "add", "-A", "-N"); err != nil {
		return "", err
	}
	return run(dir, "diff", "--no-color", "HEAD")
}
