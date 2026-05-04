package gitx

import (
	"fmt"
	"os/exec"
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

func CommitAll(dir, message string) (string, error) {
	if _, err := run(dir, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := run(dir, "commit", "-m", message); err != nil {
		return "", err
	}
	return HeadSHA(dir)
}

func CheckoutAll(dir string) error {
	if _, err := run(dir, "checkout", "."); err != nil {
		return err
	}
	if _, err := run(dir, "clean", "-fd"); err != nil {
		return err
	}
	return nil
}

func HeadSHA(dir string) (string, error) {
	return run(dir, "rev-parse", "HEAD")
}

func LogStat(dir, sinceSHA string) (string, error) {
	return run(dir, "log", sinceSHA+"..HEAD", "--stat")
}
