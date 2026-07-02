package commands

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed assets/team.json assets/prompts/*.md assets/schemas/*.json assets/flows.json
var defaultAssets embed.FS

// InitOptions tunes scaffolding behaviour.
type InitOptions struct {
	// Lang overrides language autodetection. One of "python", "node", "go",
	// "auto", or "" (treated as auto).
	Lang string
	// Precommit, when set, also writes a `.pre-commit-config` matching the
	// detected language and sets `lint_command` in team.json so the fix-loop
	// lint gate enforces the same rule set as the developer's local hook.
	Precommit bool
}

// Init scaffolds a project directory with default assets, using language
// autodetection for the .gitignore and full_test_command.
func Init(dir string) error {
	return InitWithOptions(dir, InitOptions{})
}

// InitWithOptions scaffolds a project with the given options.
func InitWithOptions(dir string, opts InitOptions) error {
	if err := os.MkdirAll(filepath.Join(dir, ".orquestalite", "results"), 0o755); err != nil {
		return err
	}

	if err := ensureGitRepo(dir); err != nil {
		return err
	}

	lang := opts.Lang
	if lang == "" || lang == "auto" {
		lang = detectLanguage(dir)
	}

	team := mustReadAsset("assets/team.json")
	team = applyTestCommand(team, lang)
	teamPath := filepath.Join(dir, "team.json")
	if err := writeIfMissing(teamPath, team); err != nil {
		return err
	}

	// Optional pre-commit scaffolding: write .pre-commit-config and set
	// team.json's lint_command to the matching rule set so the fix-loop lint
	// gate enforces it (a violation feeds {{LINT_FEEDBACK}} back to the coder
	// instead of a raw git hook aborting the agent's commit).
	if opts.Precommit {
		if rs, err := applyPrecommit(dir, teamPath, lang); err != nil {
			fmt.Fprintf(os.Stdout, "warning: precommit scaffolding skipped for %q: %v\n", lang, err)
		} else {
			fmt.Fprintf(os.Stdout, "precommit: wrote .pre-commit-config (%s) and set team.json lint_command\n", rs.Name)
		}
	}

	if _, err := exec.LookPath("codex"); err != nil {
		fmt.Fprintln(os.Stdout, "warning: codex CLI not found in PATH; the default team.json sets codex_gpt5 as primary coder. Install codex (https://github.com/openai/codex) or edit team.json to use a different agent.")
	}

	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		return err
	}
	entries, err := fs.ReadDir(defaultAssets, "assets/prompts")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := writeIfMissing(
			filepath.Join(dir, "prompts", e.Name()),
			mustReadAsset("assets/prompts/"+e.Name()),
		); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(filepath.Join(dir, "schemas"), 0o755); err != nil {
		return err
	}
	schemaEntries, err := fs.ReadDir(defaultAssets, "assets/schemas")
	if err != nil {
		return err
	}
	for _, e := range schemaEntries {
		if err := writeIfMissing(
			filepath.Join(dir, "schemas", e.Name()),
			mustReadAsset("assets/schemas/"+e.Name()),
		); err != nil {
			return err
		}
	}

	if err := writeGitignore(filepath.Join(dir, ".gitignore"), lang); err != nil {
		return err
	}

	// flows.json — the configuration-driven flow engine's default flow
	// catalogue. Same write-if-missing policy as team.json: a project that
	// already customised its flows keeps them.
	if err := writeIfMissing(filepath.Join(dir, "flows.json"), mustReadAsset("assets/flows.json")); err != nil {
		return err
	}

	return commitGitignore(dir)
}

// commitGitignore stages and commits ONLY the .gitignore so the ignore rules
// (which keep run's Rollback `git clean -fd` from deleting team.json/prompts/
// schemas/.orquestalite) are themselves tracked and survive across rollbacks.
//
// It is a no-op when git is absent, dir is not a work tree, or .gitignore has
// no staged change (keeps re-running Init idempotent). Only the .gitignore path
// is committed; any other staged work the user has is left untouched.
func commitGitignore(dir string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return nil
	}
	c := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	c.Dir = dir
	if err := c.Run(); err != nil {
		return nil
	}
	if out, err := runGit(dir, "add", "--", ".gitignore"); err != nil {
		return fmt.Errorf("git add .gitignore: %w\n%s", err, out)
	}
	// `git diff --cached --quiet` exits 0 when nothing is staged for .gitignore
	// (already committed with identical content) — skip the commit in that case.
	if _, err := runGit(dir, "diff", "--cached", "--quiet", "--", ".gitignore"); err == nil {
		return nil
	}
	if out, err := runGit(dir, "commit", "-q", "-m", "chore: orq-lite ignore rules", "--", ".gitignore"); err != nil {
		return fmt.Errorf("git commit .gitignore: %w\n%s", err, out)
	}
	return nil
}

// ensureGitRepo runs `git init` and creates an empty initial commit when the
// directory is not already inside a git work tree. Per-task commits issued by
// the orchestrator need a parent commit, otherwise `git commit` rejects them.
//
// If git is not installed, ensureGitRepo returns nil silently: an init without
// a repo is still useful (the user can `git init` later) and run will surface
// the issue itself.
func ensureGitRepo(dir string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return nil
	}
	c := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	c.Dir = dir
	if err := c.Run(); err == nil {
		return nil
	}
	if out, err := runGit(dir, "init", "-q"); err != nil {
		return fmt.Errorf("git init: %w\n%s", err, out)
	}
	if out, err := runGit(dir, "commit", "--allow-empty", "-q", "-m", "initial scaffold"); err != nil {
		return fmt.Errorf("initial commit: %w\n%s", err, out)
	}
	return nil
}

func runGit(dir string, args ...string) (string, error) {
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	return string(out), err
}

// detectLanguage returns "python", "node", "go", or "" by inspecting the dir
// for the canonical project file of each ecosystem.
func detectLanguage(dir string) string {
	check := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	switch {
	case check("pyproject.toml"), check("requirements.txt"), check("Pipfile"):
		return "python"
	case check("package.json"):
		return "node"
	case check("go.mod"):
		return "go"
	}
	// Fallback: scan top-level files for *.py — covers ad-hoc Python projects
	// that haven't been packaged yet.
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".py") {
				return "python"
			}
		}
	}
	return ""
}

// applyTestCommand substitutes the embedded default "go test ./..." in the
// team.json template with a language-appropriate command. Uses a literal
// string replace to preserve JSON layout/comments (encoding/json reorders keys).
func applyTestCommand(team []byte, lang string) []byte {
	defaultLine := []byte(`"full_test_command": "go test ./..."`)
	var newCmd string
	switch lang {
	case "python":
		newCmd = "uv run pytest -q"
	case "node":
		newCmd = "npm test --silent"
	case "go":
		return team
	default:
		// Ambiguous language: clear the command rather than keep the Go default,
		// which would fail every full-suite gate in a non-Go repo. Empty is a
		// no-op (see config.Validate); the run-time detector fills it in later.
		newCmd = ""
	}
	replacement := []byte(fmt.Sprintf(`"full_test_command": %q`, newCmd))
	return bytes.Replace(team, defaultLine, replacement, 1)
}

// writeGitignore ensures .gitignore covers .orquestalite/ plus any
// language-appropriate build artefacts. Idempotent: re-running adds nothing.
func writeGitignore(path, lang string) error {
	// team.json, prompts/, and schemas/ are ignored alongside .orquestalite/ so
	// run's Rollback (`git clean -fd`) cannot delete them. The .gitignore itself
	// is committed by commitGitignore so the rules are tracked and survive the
	// clean — otherwise an untracked .gitignore is wiped on the first rollback
	// and the ignored dirs die on the next one.
	entries := []string{".orquestalite/", "team.json", "prompts/", "schemas/", "flows.json"}
	switch lang {
	case "python":
		entries = append(entries,
			"__pycache__/",
			"*.pyc",
			"*.pyo",
			".pytest_cache/",
			".venv/",
			".mypy_cache/",
			".ruff_cache/",
		)
	case "node":
		entries = append(entries,
			"node_modules/",
			"dist/",
			".next/",
			".cache/",
		)
	case "go":
		entries = append(entries,
			"bin/",
			"*.test",
			"*.out",
		)
	}
	for _, e := range entries {
		if err := ensureGitignoreEntry(path, e); err != nil {
			return err
		}
	}
	return nil
}

func writeIfMissing(path string, content []byte) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, content, 0o644)
}

func mustReadAsset(p string) []byte {
	raw, err := defaultAssets.ReadFile(p)
	if err != nil {
		panic(fmt.Sprintf("missing embedded asset %s: %v", p, err))
	}
	return raw
}

func ensureGitignoreEntry(path, entry string) error {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return os.WriteFile(path, []byte(entry+"\n"), 0o644)
	}
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == entry {
			return nil
		}
	}
	body := string(raw)
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return os.WriteFile(path, []byte(body+entry+"\n"), 0o644)
}
