package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lionelchamorro/orquestalite/internal/flow"
)

const packUsage = "usage: orq-lite pack install <pack-dir> [--force]"

// PackCLI implements `orq-lite pack <command>`. `install` verifies a pack
// directory against its pack.json manifest (every file digest, no unlisted
// files, no symlinks) and copies it to .orquestalite/packs/<name>/<version>/,
// the layout `orq-lite flow run <name>/<flow>@<version>` resolves.
func PackCLI(_ context.Context, projectDir string, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "install" {
		return fmt.Errorf("%s", packUsage)
	}
	force := false
	source := ""
	for _, arg := range args[1:] {
		switch {
		case arg == "--force" || arg == "-force":
			force = true
		case source == "":
			source = arg
		default:
			return fmt.Errorf("%s", packUsage)
		}
	}
	if source == "" {
		return fmt.Errorf("%s", packUsage)
	}
	return packInstall(projectDir, source, force, out)
}

func packInstall(projectDir, source string, force bool, out io.Writer) error {
	pack, err := flow.LoadPack(source)
	if err != nil {
		return err
	}
	dest := filepath.Join(projectDir, ".orquestalite", "packs", pack.Name, pack.Version)
	if _, statErr := os.Stat(dest); statErr == nil {
		if !force {
			return fmt.Errorf("pack %s@%s is already installed at %s (use --force to replace)", pack.Name, pack.Version, dest)
		}
		if err = os.RemoveAll(dest); err != nil {
			return err
		}
	}
	relatives := make([]string, 0, len(pack.Files)+1)
	relatives = append(relatives, "pack.json")
	for relative := range pack.Files {
		relatives = append(relatives, relative)
	}
	for _, relative := range relatives {
		if err = copyPackFile(source, dest, relative); err != nil {
			_ = os.RemoveAll(dest)
			return err
		}
	}
	// Re-verify the installed copy so a partial or racing write can never
	// leave an unverified pack behind.
	if _, err = flow.LoadPack(dest); err != nil {
		_ = os.RemoveAll(dest)
		return fmt.Errorf("installed copy failed verification: %w", err)
	}
	fmt.Fprintf(out, "installed %s@%s (%d files) at %s\n", pack.Name, pack.Version, len(pack.Files)+1, dest)
	fmt.Fprintf(out, "run flows with: orq-lite flow run %s/<flow>@%s\n", pack.Name, pack.Version)
	return nil
}

func copyPackFile(sourceRoot, destRoot, relative string) error {
	data, err := os.ReadFile(filepath.Join(sourceRoot, filepath.FromSlash(relative)))
	if err != nil {
		return err
	}
	target := filepath.Join(destRoot, filepath.FromSlash(relative))
	if err = os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o644)
}
