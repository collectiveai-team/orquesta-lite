package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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
	// Verify the source pack before any writes — rejects tampered or invalid packs.
	pack, err := flow.LoadPack(source)
	if err != nil {
		return err
	}

	packsRoot := filepath.Join(projectDir, ".orquestalite", "packs")
	dest := filepath.Join(packsRoot, pack.Name, pack.Version)

	// Guard: reject a source path that is inside the install root or equals dest,
	// so --force cannot delete the source while trying to replace it.
	absSource, err := filepath.Abs(source)
	if err != nil {
		return fmt.Errorf("resolve source path: %w", err)
	}
	if _, statErr := os.Stat(absSource); statErr == nil {
		if resolved, resolveErr := filepath.EvalSymlinks(absSource); resolveErr == nil {
			absSource = resolved
		}
	}
	absPacksRoot, err := filepath.Abs(packsRoot)
	if err != nil {
		return fmt.Errorf("resolve packs root: %w", err)
	}
	if _, statErr := os.Stat(absPacksRoot); statErr == nil {
		if resolved, resolveErr := filepath.EvalSymlinks(absPacksRoot); resolveErr == nil {
			absPacksRoot = resolved
		}
	}
	if rel, relErr := filepath.Rel(absPacksRoot, absSource); relErr == nil && !strings.HasPrefix(rel, "..") {
		return fmt.Errorf("source %q is inside the pack install root; copy the pack to a location outside %s before installing", source, packsRoot)
	}

	// Fail fast on an existing install before staging any bytes; the force
	// path re-checks after staging so the old install is only removed once
	// the replacement is fully verified.
	if _, statErr := os.Stat(dest); statErr == nil && !force {
		return fmt.Errorf("pack %s@%s is already installed at %s (use --force to replace)", pack.Name, pack.Version, dest)
	}

	// Create the packs root if it does not exist, then stage the pack in a
	// temporary directory on the same filesystem so the final rename is atomic.
	if err := os.MkdirAll(packsRoot, 0o755); err != nil {
		return fmt.Errorf("create packs root: %w", err)
	}
	tempDir, err := os.MkdirTemp(packsRoot, ".install-tmp-")
	if err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	// Remove the staging dir on any error path (on success it is renamed to dest
	// and the path no longer exists, so RemoveAll is a safe no-op).
	defer os.RemoveAll(tempDir)

	// Copy pack.json (no digest entry in Files; its integrity is covered by
	// flow.LoadPack above).
	packJSON, err := os.ReadFile(filepath.Join(source, "pack.json"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tempDir, "pack.json"), packJSON, 0o644); err != nil {
		return err
	}

	// Copy each listed file and verify its SHA-256 digest inline.  This closes
	// the TOCTOU window between the LoadPack verification of the source and the
	// actual bytes written to disk, replacing the second LoadPack(dest) call.
	for relative, expected := range pack.Files {
		data, readErr := os.ReadFile(filepath.Join(source, filepath.FromSlash(relative)))
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(data)
		actual := hex.EncodeToString(sum[:])
		if actual != string(expected) {
			return fmt.Errorf("digest mismatch for %s: got %s want %s", relative, actual, expected)
		}
		target := filepath.Join(tempDir, filepath.FromSlash(relative))
		if mkdirErr := os.MkdirAll(filepath.Dir(target), 0o755); mkdirErr != nil {
			return mkdirErr
		}
		if writeErr := os.WriteFile(target, data, 0o644); writeErr != nil {
			return writeErr
		}
	}

	// Atomically replace dest: check for an existing install, then rename the
	// fully verified staging dir into place.  The staging dir and dest share the
	// same parent (packsRoot), so os.Rename is atomic on POSIX.
	if _, statErr := os.Stat(dest); statErr == nil {
		if !force {
			return fmt.Errorf("pack %s@%s is already installed at %s (use --force to replace)", pack.Name, pack.Version, dest)
		}
		if rmErr := os.RemoveAll(dest); rmErr != nil {
			return rmErr
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create pack parent dir: %w", err)
	}
	if err := os.Rename(tempDir, dest); err != nil {
		return fmt.Errorf("install pack: %w", err)
	}

	fmt.Fprintf(out, "installed %s@%s (%d files) at %s\n", pack.Name, pack.Version, len(pack.Files)+1, dest)
	fmt.Fprintf(out, "run flows with: orq-lite flow run %s/<flow>@%s\n", pack.Name, pack.Version)
	return nil
}
