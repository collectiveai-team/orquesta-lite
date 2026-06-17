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

// detectLintCommand returns a lint/quality command for the repo, or "" when no
// linter is clearly configured. It is deliberately more conservative than
// detectTestCommand: it only proposes a tool when the repo carries that tool's
// own config (so an unconfigured/uninstalled linter never becomes a gate that
// fails every task). `go vet` is the exception — it ships with the toolchain,
// so any module gets it. A missing binary is still tolerated at run time (see
// liveDeps.FullSuite), this just avoids proposing tools the repo never adopted.
func detectLintCommand(dir string) string {
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}

	switch {
	case exists("ruff.toml"), exists(".ruff.toml"), fileContains(dir, "pyproject.toml", "[tool.ruff"):
		return "ruff check ."
	case exists(".eslintrc"), exists(".eslintrc.js"), exists(".eslintrc.cjs"),
		exists(".eslintrc.json"), exists(".eslintrc.yml"), exists("eslint.config.js"),
		exists("eslint.config.mjs"), fileContains(dir, "package.json", "\"eslint\""):
		return "npx --no-install eslint ."
	case exists("go.mod"):
		return "go vet ./..."
	case exists("Cargo.toml"):
		return "cargo clippy"
	}
	return ""
}

// fileContains reports whether the named file in dir exists and contains needle.
func fileContains(dir, name, needle string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), needle)
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

// persistConfigString writes a detected value back into team.json by replacing
// the empty `"key": ""` line, preserving the file's hand-authored layout (a
// literal replace, like applyTestCommand at init time). It is best-effort: a
// missing file, an already-populated value, or a write error is not fatal, and
// a key that is absent or already set is left untouched.
func persistConfigString(teamPath, key, val string) error {
	raw, err := os.ReadFile(teamPath)
	if err != nil {
		return err
	}
	old := []byte(fmt.Sprintf(`"%s": ""`, key))
	if !bytes.Contains(raw, old) {
		return nil // already populated, absent, or formatted differently; leave it
	}
	replacement := []byte(fmt.Sprintf(`"%s": %q`, key, val))
	return os.WriteFile(teamPath, bytes.Replace(raw, old, replacement, 1), 0o644)
}
