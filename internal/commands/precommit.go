package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PrecommitRuleset is a language-appropriate set of pre-commit rule checks:
// a lint command the fix loop runs inside its lint gate (a violation feeds
// {{LINT_FEEDBACK}} back to the coder instead of a raw git hook aborting the
// agent's commit) and a `.pre-commit-config` body for the human developer's
// own local commits.
type PrecommitRuleset struct {
	Name        string // "go" | "python" | "typescript" | "javascript"
	LintCommand string // run by the fix-loop lint gate (FixConfig.LintGate)
	Config      string // ".pre-commit-config" contents (pre-commit framework YAML)
}

// detectPrecommitRuleset selects the rule set appropriate to the detected
// language. TypeScript is distinguished from JavaScript by the presence of a
// tsconfig.json. Unknown languages yield an empty ruleset (no scaffolding).
func detectPrecommitRuleset(dir, lang string) PrecommitRuleset {
	switch lang {
	case "go":
		return PrecommitRuleset{Name: "go", LintCommand: goLintScript, Config: goPrecommitConfig}
	case "python":
		return PrecommitRuleset{Name: "python", LintCommand: pythonLintScript, Config: pythonPrecommitConfig}
	case "node":
		if fileExists(filepath.Join(dir, "tsconfig.json")) {
			return PrecommitRuleset{Name: "typescript", LintCommand: tsLintScript, Config: tsPrecommitConfig}
		}
		return PrecommitRuleset{Name: "javascript", LintCommand: jsLintScript, Config: jsPrecommitConfig}
	}
	return PrecommitRuleset{}
}

// goLintScript runs gofmt (unformatted files fail), go vet, and golangci-lint
// when it is installed. The two tools that ship with the Go toolchain (gofmt,
// go vet) are always enforced; golangci-lint is skipped when absent. The whole
// gate is skipped when `go` is not on PATH (the in-script guard), preserving
// the lint gate's "missing binary is a skip, not a block" contract.
const goLintScript = `sh -c 'command -v go >/dev/null 2>&1 || exit 0; set -e; test -z "$(gofmt -l .)" || { echo "gofmt: unformatted Go files (run gofmt -w)"; exit 1; }; go vet ./...; if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; fi'`

// pythonLintScript runs ruff check + ruff format --check (the repo's own ruff
// config), and mypy only when it is installed AND configured (mypy.ini or a
// [tool.mypy] table). The whole gate is skipped when ruff is absent so an
// unconfigured repo is never blocked by a missing primary linter.
const pythonLintScript = `sh -c 'command -v ruff >/dev/null 2>&1 || exit 0; set -e; ruff check .; ruff format --check .; if command -v mypy >/dev/null 2>&1 && { [ -f mypy.ini ] || grep -q "\[tool.mypy\]" pyproject.toml 2>/dev/null; }; then mypy .; fi'`

// tsLintScript runs eslint, prettier --check, and tsc --noEmit for TypeScript
// projects (tsconfig.json present). Skipped when eslint is not installed.
const tsLintScript = `sh -c 'npx --no-install eslint --version >/dev/null 2>&1 || exit 0; set -e; npx --no-install eslint .; npx --no-install prettier --check .; npx --no-install tsc --noEmit'`

// jsLintScript runs eslint and prettier --check for plain JavaScript projects.
// Skipped when eslint is not installed.
const jsLintScript = `sh -c 'npx --no-install eslint --version >/dev/null 2>&1 || exit 0; set -e; npx --no-install eslint .; npx --no-install prettier --check .'`

const goPrecommitConfig = `# Managed by orq-lite (orq-lite init --precommit / orq-lite precommit).
# Edit freely; re-run ` + "`orq-lite precommit`" + ` to reset to the detected default.
repos:
  - repo: local
    hooks:
      - id: gofmt
        name: gofmt
        entry: gofmt -l -w
        language: system
        files: \.go$
      - id: go-vet
        name: go vet
        entry: go vet ./...
        language: system
        pass_filenames: false
      - id: golangci-lint
        name: golangci-lint
        entry: golangci-lint run
        language: system
        pass_filenames: false
`

const pythonPrecommitConfig = `# Managed by orq-lite (orq-lite init --precommit / orq-lite precommit).
repos:
  - repo: local
    hooks:
      - id: ruff-check
        name: ruff check
        entry: ruff check
        language: system
        pass_filenames: false
      - id: ruff-format
        name: ruff format
        entry: ruff format --check
        language: system
        pass_filenames: false
      - id: mypy
        name: mypy
        entry: mypy .
        language: system
        pass_filenames: false
        stages: [pre-commit]
`

const tsPrecommitConfig = `# Managed by orq-lite (orq-lite init --precommit / orq-lite precommit).
repos:
  - repo: local
    hooks:
      - id: eslint
        name: eslint
        entry: npx --no-install eslint
        language: system
        pass_filenames: false
      - id: prettier
        name: prettier --check
        entry: npx --no-install prettier --check .
        language: system
        pass_filenames: false
      - id: tsc
        name: tsc --noEmit
        entry: npx --no-install tsc --noEmit
        language: system
        pass_filenames: false
`

const jsPrecommitConfig = `# Managed by orq-lite (orq-lite init --precommit / orq-lite precommit).
repos:
  - repo: local
    hooks:
      - id: eslint
        name: eslint
        entry: npx --no-install eslint
        language: system
        pass_filenames: false
      - id: prettier
        name: prettier --check
        entry: npx --no-install prettier --check .
        language: system
        pass_filenames: false
`

// applyPrecommit writes the `.pre-commit-config` for the detected language and
// sets `lint_command` in team.json so the fix-loop lint gate enforces the same
// rule set (a missing linter is logged as a skip by the gate, never a block).
// Best-effort: a missing team.json or an already-populated lint_command is not
// fatal, and idempotent writes add nothing.
func applyPrecommit(dir, teamPath, lang string) (PrecommitRuleset, error) {
	rs := detectPrecommitRuleset(dir, lang)
	if rs.Name == "" {
		return rs, fmt.Errorf("no pre-commit rule set for detected language %q (supported: go, python, node)", lang)
	}
	configPath := filepath.Join(dir, ".pre-commit-config")
	if err := writeIfMissing(configPath, []byte(rs.Config)); err != nil {
		return rs, fmt.Errorf("write .pre-commit-config: %w", err)
	}
	// Persist the lint command into team.json when its lint_command slot is empty
	// so the gate enforces the same rule set as the developer's local hook.
	if teamPath != "" {
		if err := persistConfigString(teamPath, "lint_command", rs.LintCommand); err != nil {
			return rs, fmt.Errorf("persist lint_command: %w", err)
		}
	}
	return rs, nil
}

// InitPrecommit is the dedicated `orq-lite precommit` subcommand: detect the
// language, write `.pre-commit-config`, and set team.json's `lint_command` to
// the matching rule set. Works on an existing project (the counterpart of `init
// --precommit` for projects already scaffolded).
func InitPrecommit(dir string) error {
	lang := detectLanguage(dir)
	if lang == "" {
		return fmt.Errorf("could not detect project language in %s (hint: pass --lang to `orq-lite init`)", dir)
	}
	teamPath := ""
	if _, err := os.Stat(filepath.Join(dir, "team.json")); err == nil {
		teamPath = filepath.Join(dir, "team.json")
	}
	rs, err := applyPrecommit(dir, teamPath, lang)
	if err != nil {
		return err
	}
	fmt.Printf("precommit: wrote .pre-commit-config (%s) and set team.json lint_command\n", rs.Name)
	if !strings.HasPrefix(rs.LintCommand, "sh -c") {
		fmt.Println("precommit: lint_command:", rs.LintCommand)
	} else {
		fmt.Println("precommit: lint_command: <composite shell script>")
	}
	return nil
}
