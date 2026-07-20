package flow

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const PackAPIVersion = "orq.pack/v1"

type Pack struct {
	APIVersion string            `json:"apiVersion"`
	Name       string            `json:"name"`
	Version    string            `json:"version"`
	Files      map[string]Digest `json:"files"`
}

type PackSnapshot struct {
	Name    string            `json:"name"`
	Version string            `json:"version"`
	Digest  Digest            `json:"digest"`
	Files   map[string]Digest `json:"files"`
}

func (p *Pack) Snapshot() *PackSnapshot {
	raw, _ := json.Marshal(p)
	files := make(map[string]Digest, len(p.Files))
	for path, digest := range p.Files {
		files[path] = digest
	}
	return &PackSnapshot{Name: p.Name, Version: p.Version, Digest: digestBytes(raw), Files: files}
}

func (p *PackSnapshot) Matches(pack *Pack) bool {
	if p == nil || pack == nil {
		return p == nil && pack == nil
	}
	other := pack.Snapshot()
	return p.Name == other.Name && p.Version == other.Version && p.Digest == other.Digest
}

// PinPack adds the verified manifest to the immutable IR and includes it in
// the definition digest used by new runs.
func PinPack(ir *IR, pack *Pack) error {
	if ir == nil || pack == nil {
		return fmt.Errorf("pack: IR and manifest are required")
	}
	ir.Pack = pack.Snapshot()
	raw, err := json.Marshal(ir)
	if err != nil {
		return err
	}
	ir.Digest = digestBytes(raw)
	return nil
}

func LoadPack(root string) (*Pack, error) {
	raw, err := os.ReadFile(filepath.Join(root, "pack.json"))
	if err != nil {
		return nil, fmt.Errorf("pack: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var pack Pack
	if err = decoder.Decode(&pack); err != nil {
		return nil, fmt.Errorf("pack: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("pack: trailing JSON")
	}
	if pack.APIVersion != PackAPIVersion || !identifier.MatchString(pack.Name) || !versionPattern.MatchString(pack.Version) {
		return nil, fmt.Errorf("pack: invalid apiVersion, name, or version")
	}
	verifiedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("pack: resolve root: %w", err)
	}
	for relative, expected := range pack.Files {
		clean, cleanErr := cleanPackPath(relative)
		if cleanErr != nil {
			return nil, fmt.Errorf("pack: unsafe file path %q", relative)
		}
		if len(expected) != 64 {
			return nil, fmt.Errorf("pack: invalid SHA-256 digest for %s", relative)
		}
		if _, decodeErr := hex.DecodeString(string(expected)); decodeErr != nil {
			return nil, fmt.Errorf("pack: invalid SHA-256 digest for %s", relative)
		}
		target := filepath.Join(root, filepath.FromSlash(clean))
		resolved, resolveErr := filepath.EvalSymlinks(target)
		if resolveErr != nil {
			return nil, fmt.Errorf("pack: resolve %s: %w", relative, resolveErr)
		}
		inside, relErr := filepath.Rel(verifiedRoot, resolved)
		if relErr != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("pack: file %s resolves outside pack root", relative)
		}
		content, readErr := os.ReadFile(resolved)
		if readErr != nil {
			return nil, fmt.Errorf("pack: read %s: %w", relative, readErr)
		}
		if actual := digestBytes(content); actual != expected {
			return nil, fmt.Errorf("pack: digest mismatch for %s: got %s want %s", relative, actual, expected)
		}
	}
	if err = rejectUnlistedPackFiles(root, pack.Files); err != nil {
		return nil, err
	}
	return &pack, nil
}

func cleanPackPath(relative string) (string, error) {
	portable := strings.ReplaceAll(relative, `\`, "/")
	if portable == "" || strings.HasPrefix(portable, "/") {
		return "", fmt.Errorf("empty or absolute path")
	}
	parts := strings.Split(portable, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("unsafe path segment")
		}
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(portable)))
	if clean != portable || filepath.IsAbs(filepath.FromSlash(portable)) {
		return "", fmt.Errorf("path is not canonical")
	}
	return clean, nil
}

func rejectUnlistedPackFiles(root string, listed map[string]Digest) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("pack: inspect files: %w", walkErr)
		}
		if path == root || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("pack: symlink is not allowed: %s", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("pack: inspect %s: %w", path, err)
		}
		relative = filepath.ToSlash(relative)
		if relative == "pack.json" {
			return nil
		}
		if _, ok := listed[relative]; !ok {
			return fmt.Errorf("pack: unlisted file %s", relative)
		}
		return nil
	})
}
