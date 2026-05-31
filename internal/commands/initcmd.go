package commands

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed assets/team.json assets/prompts/*.md assets/schemas/*.json
var defaultAssets embed.FS

func Init(dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, ".orquestalite", "results"), 0o755); err != nil {
		return err
	}
	if err := writeIfMissing(filepath.Join(dir, "team.json"), mustReadAsset("assets/team.json")); err != nil {
		return err
	}

	// Warn if codex CLI is not installed; the default team.json uses codex_gpt5 as primary coder.
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

	return ensureGitignoreEntry(filepath.Join(dir, ".gitignore"), ".orquestalite/")
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
