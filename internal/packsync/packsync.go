// Package packsync compares the pack a project has installed against the copy
// embedded in this binary, and decides whether a run may proceed.
//
// The signal is the orq-lite version recorded in packstate, not the pack's own
// `version` field: the pack version names the contract, and improvements ship
// inside it in place, so it stays constant across exactly the updates a project
// needs to hear about.
package packsync

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/collectiveai-team/orquesta-lite/internal/packstate"
)

// DevVersion is the version a binary built without release ldflags reports.
const DevVersion = "dev"

// Change is one file that differs between the installed pack and this binary's.
type Change struct {
	Path string
	Kind string // added, updated, or removed
}

// Decision is what the caller should do about the difference.
type Decision int

const (
	// Proceed: run as-is, write nothing.
	Proceed Decision = iota
	// Restamp: record this version and run; nothing would actually change.
	Restamp
	// Block: stop before the run exists and let the operator choose.
	Block
)

func (d Decision) String() string {
	switch d {
	case Proceed:
		return "Proceed"
	case Restamp:
		return "Restamp"
	default:
		return "Block"
	}
}

// Decide reports what to do for one installed pack.
func Decide(stamp packstate.Stamp, version string, changed bool) Decision {
	if version == "" || version == DevVersion {
		return Proceed
	}
	if stamp.InstalledFrom == version || stamp.Acknowledged == version {
		return Proceed
	}
	if !changed {
		return Restamp
	}
	return Block
}

// Diff lists every file that differs between the installed pack root and the
// embedded copy under prefix, sorted by path so the report is stable.
//
// prefix names which embedded pack to read: the binary carries more than one,
// and comparing a project against the frozen copy it does not run would report
// drift that means nothing.
func Diff(installedRoot string, embedded fs.FS, prefix string) ([]Change, error) {
	want, err := readEmbedded(embedded, prefix)
	if err != nil {
		return nil, err
	}
	have, err := readInstalled(installedRoot)
	if err != nil {
		return nil, err
	}
	var changes []Change
	for path, content := range want {
		existing, ok := have[path]
		switch {
		case !ok:
			changes = append(changes, Change{Path: path, Kind: "added"})
		case !bytes.Equal(existing, content):
			changes = append(changes, Change{Path: path, Kind: "updated"})
		}
	}
	for path := range have {
		if _, ok := want[path]; !ok {
			changes = append(changes, Change{Path: path, Kind: "removed"})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

func readEmbedded(embedded fs.FS, prefix string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := fs.WalkDir(embedded, prefix, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, readErr := fs.ReadFile(embedded, path)
		if readErr != nil {
			return readErr
		}
		out[strings.TrimPrefix(path, prefix+"/")] = data
		return nil
	})
	return out, err
}

func readInstalled(root string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) && path == root {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		out[filepath.ToSlash(relative)] = data
		return nil
	})
	return out, err
}

// Apply overwrites the installed pack root with the embedded copy.
//
// Files the embedded pack no longer ships are deleted rather than left behind:
// flow.LoadPack rejects any file its manifest does not list, so a leftover
// would not be merely stale, it would stop the pack from loading at all.
func Apply(installedRoot string, embedded fs.FS, prefix string) error {
	want, err := readEmbedded(embedded, prefix)
	if err != nil {
		return err
	}
	have, err := readInstalled(installedRoot)
	if err != nil {
		return err
	}
	for path := range have {
		if _, keep := want[path]; keep {
			continue
		}
		if err = os.Remove(filepath.Join(installedRoot, filepath.FromSlash(path))); err != nil {
			return err
		}
	}
	paths := make([]string, 0, len(want))
	for path := range want {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		target := filepath.Join(installedRoot, filepath.FromSlash(path))
		if err = os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err = os.WriteFile(target, want[path], 0o644); err != nil {
			return err
		}
	}
	return pruneEmptyDirs(installedRoot)
}

// pruneEmptyDirs removes directories left empty by a removal, deepest first, so
// a renamed pack subdirectory does not linger as an empty shell.
func pruneEmptyDirs(root string) error {
	var dirs []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			if err = os.Remove(dir); err != nil {
				return err
			}
		}
	}
	return nil
}
