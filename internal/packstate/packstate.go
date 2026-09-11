// Package packstate records which orq-lite version wrote each installed pack,
// and which version an operator has explicitly chosen to stay on.
//
// The record lives at .orquestalite/pack-state.json, deliberately outside every
// pack root: flow.LoadPack rejects any file the manifest does not list, so a
// stamp stored inside the pack would stop the pack from loading at all.
package packstate

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Stamp is what is known about one installed pack version.
//
// InstalledFrom is the truth about the bytes on disk: the orq-lite version
// whose embedded copy wrote them. Acknowledged is a decision, not a fact — the
// version an operator told us to stop asking about. They are separate so that
// declining an update does not erase where the files actually came from.
type Stamp struct {
	InstalledFrom string `json:"installed_from,omitempty"`
	Acknowledged  string `json:"acknowledged,omitempty"`
}

type file struct {
	Packs map[string]Stamp `json:"packs"`
}

// Path returns the state file for a project directory.
func Path(projectDir string) string {
	return filepath.Join(projectDir, ".orquestalite", "pack-state.json")
}

func key(name, version string) string { return name + "@" + version }

// Load returns the stamp for one pack version. A missing, unreadable, or
// malformed state file reads as the zero Stamp: a project that predates this
// mechanism is indistinguishable from one that never recorded anything, and
// both should be treated as the initial state rather than as an error.
func Load(projectDir, name, version string) Stamp {
	return read(projectDir).Packs[key(name, version)]
}

func read(projectDir string) file {
	loaded := file{Packs: map[string]Stamp{}}
	raw, err := os.ReadFile(Path(projectDir))
	if err != nil {
		return loaded
	}
	if err = json.Unmarshal(raw, &loaded); err != nil || loaded.Packs == nil {
		return file{Packs: map[string]Stamp{}}
	}
	return loaded
}

// Save records the stamp for one pack version, leaving every other entry alone.
func Save(projectDir, name, version string, stamp Stamp) error {
	loaded := read(projectDir)
	loaded.Packs[key(name, version)] = stamp
	raw, err := json.MarshalIndent(loaded, "", "  ")
	if err != nil {
		return err
	}
	path := Path(projectDir)
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}
