package commands

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed assets/team.json assets/prompts/*.md
var defaultAssets embed.FS

func Init(dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, ".pyorquesta", "results"), 0o755); err != nil {
		return err
	}
	if err := writeIfMissing(filepath.Join(dir, "team.json"), mustReadAsset("assets/team.json")); err != nil {
		return err
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
	return ensureGitignoreEntry(filepath.Join(dir, ".gitignore"), ".pyorquesta/")
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
