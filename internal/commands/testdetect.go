package commands

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// detectTestCommand inspects a project directory and returns a sensible
// full-suite test command for the repo, or "" when nothing recognizable is
// found. It mirrors the language heuristics used at init time but runs at the
// start of a run, so a feature checkout (e.g. in factory mode) still gets a
// working verification gate even when `orq-lite init` was never run against it
// or couldn't determine the language.
func detectTestCommand(dir string) string {
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}

	// A Makefile `test:` target is the repo's own declared entry point; prefer it.
	if hasMakeTarget(dir, "test") {
		return "make test"
	}

	switch {
	case exists("pyproject.toml"):
		return "uv run pytest -q"
	case exists("pytest.ini"), exists("tox.ini"), exists("setup.cfg"), exists("requirements.txt"), exists("Pipfile"):
		return "pytest -q"
	case exists("package.json"):
		return "npm test --silent"
	case exists("go.mod"):
		return "go test ./..."
	case exists("Cargo.toml"):
		return "cargo test"
	case exists("pom.xml"):
		return "mvn -q test"
	case exists("build.gradle"), exists("build.gradle.kts"):
		return "gradle test"
	}

	// Loose fallback: a directory of *.py with no manifest still tests via pytest.
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".py") {
				return "pytest -q"
			}
		}
	}
	return ""
}

// hasMakeTarget reports whether any Makefile in dir declares the given target
// (a line whose first non-whitespace token is "<target>:").
func hasMakeTarget(dir, target string) bool {
	prefix := target + ":"
	for _, name := range []string{"Makefile", "makefile", "GNUmakefile"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), prefix) {
				return true
			}
		}
	}
	return false
}

// persistTestCommand writes a detected command back into team.json by replacing
// the empty full_test_command line, preserving the file's hand-authored layout
// (a literal replace, like applyTestCommand at init time). It is best-effort: a
// missing file, an already-populated command, or a write error is not fatal.
func persistTestCommand(teamPath, cmd string) error {
	raw, err := os.ReadFile(teamPath)
	if err != nil {
		return err
	}
	old := []byte(`"full_test_command": ""`)
	if !bytes.Contains(raw, old) {
		return nil // already populated or formatted differently; leave it alone
	}
	replacement := []byte(fmt.Sprintf(`"full_test_command": %q`, cmd))
	return os.WriteFile(teamPath, bytes.Replace(raw, old, replacement, 1), 0o644)
}
