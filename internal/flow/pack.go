package flow

import (
	"bytes"
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
	for relative, expected := range pack.Files {
		if filepath.IsAbs(relative) || strings.Contains(filepath.ToSlash(relative), "../") {
			return nil, fmt.Errorf("pack: unsafe file path %q", relative)
		}
		content, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if readErr != nil {
			return nil, fmt.Errorf("pack: read %s: %w", relative, readErr)
		}
		if actual := digestBytes(content); actual != expected {
			return nil, fmt.Errorf("pack: digest mismatch for %s: got %s want %s", relative, actual, expected)
		}
	}
	return &pack, nil
}
