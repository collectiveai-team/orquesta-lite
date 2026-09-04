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

	governedpack "github.com/collectiveai-team/orquesta-lite/examples/governed-pack"
)

//go:embed assets/team.json
var defaultAssets embed.FS

// InitOptions tunes scaffolding behaviour.
type InitOptions struct {
	// Lang overrides language autodetection. One of "python", "node", "go",
	// "auto", or "" (treated as auto).
	Lang string
}

// InitWithOptions scaffolds a durable v2 project with the given options.
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
	if err := writeOrMigrateTeam(teamPath, team); err != nil {
		return err
	}

	if _, err := exec.LookPath("claude"); err != nil {
		fmt.Fprintln(os.Stdout, "warning: claude CLI not found in PATH; the default team.json sets claude_opus and claude_sonnet as the primary agent of every role. Install claude (https://github.com/anthropics/claude-code) or edit team.json to use a different agent.")
	}

	if _, err := exec.LookPath("codex"); err != nil {
		fmt.Fprintln(os.Stdout, "warning: codex CLI not found in PATH; the default team.json sets codex_sol and codex_terra as the fallback agent of every role. Install codex (https://github.com/openai/codex) or edit team.json to drop them.")
	}

	if err := writeGitignore(filepath.Join(dir, ".gitignore"), lang); err != nil {
		return err
	}

	if err := installBuiltinDevelopmentPack(dir); err != nil {
		return err
	}

	return commitGitignore(dir)
}

func writeOrMigrateTeam(path string, defaults []byte) error {
	existing, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return writeIfMissing(path, defaults)
	}
	if err != nil {
		return err
	}
	oldPrefix := []byte(".orquestalite/packs/development/4/prompts/")
	newPrefix := []byte(".orquestalite/packs/development/5/prompts/")
	migrated := bytes.ReplaceAll(existing, oldPrefix, newPrefix)
	if bytes.Equal(existing, migrated) {
		return nil
	}
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	return os.WriteFile(path, migrated, mode)
}

func installBuiltinDevelopmentPack(projectDir string) error {
	destination := filepath.Join(projectDir, ".orquestalite", "packs", "development", "5")
	return fs.WalkDir(governedpack.FS, "pack", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel("pack", path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := governedpack.FS.ReadFile(path)
		if err != nil {
			return err
		}
		return writeIfMissing(target, data)
	})
}

// commitGitignore stages and commits ONLY the .gitignore so the ignore rules
// (which keep workflow state and team.json from being deleted by cleanup) are
// themselves tracked and survive across rollbacks.
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

// applyTestCommand substitutes the embedded Go defaults in the team.json
// template with language-appropriate gates. Uses literal string replaces to
// preserve JSON layout/comments (encoding/json reorders keys).
//
// It rewrites the argv forms alongside the shell string. They are not
// decoration: `lint_argv` and `test_argv` are the only team.json keys a v2
// flow can read, and a flow that references one the project does not declare
// is rejected before the run starts. Scaffolding the string command alone —
// which is what this used to do — produced a project where `init`, `pack
// install` and `doctor` all reported success and the very first `flow run`
// failed on a missing key.
func applyTestCommand(team []byte, lang string) []byte {
	var testArgv, lintArgv []string
	switch lang {
	case "python":
		testArgv = []string{"uv", "run", "pytest", "-q"}
		lintArgv = []string{"uv", "run", "ruff", "check", "."}
	case "node":
		testArgv = []string{"npm", "test", "--silent"}
		lintArgv = []string{"npm", "run", "lint"}
	case "go":
		return team
	default:
		// Ambiguous language: clear the argv gates rather than keep Go defaults,
		// which would fail every gate in a non-Go repo. This is a deliberate,
		// loud hole — doctor reports it and a flow that
		// needs the gate refuses to start rather than silently running
		// nothing. The run-time detector and the operator fill these in.
	}
	team = bytes.Replace(team, []byte(`"lint_argv": ["go", "vet", "./..."]`),
		[]byte(`"lint_argv": `+encodeArgv(lintArgv)), 1)
	team = bytes.Replace(team, []byte(`"test_argv": ["go", "test", "./..."]`),
		[]byte(`"test_argv": `+encodeArgv(testArgv)), 1)
	return team
}

func encodeArgv(argv []string) string {
	if len(argv) == 0 {
		return "[]"
	}
	quoted := make([]string, len(argv))
	for index, word := range argv {
		quoted[index] = fmt.Sprintf("%q", word)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// writeGitignore ensures .gitignore covers durable local state plus any
// language-appropriate build artefacts. Idempotent: re-running adds nothing.
func writeGitignore(path, lang string) error {
	// Pack installs and workflow state live under .orquestalite; team.json is
	// local runtime configuration. Project-owned v2 flows remain trackable.
	entries := []string{".orquestalite/", "team.json"}
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
