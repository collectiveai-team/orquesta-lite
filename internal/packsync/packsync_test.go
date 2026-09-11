package packsync

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/collectiveai-team/orquesta-lite/internal/packstate"
)

func embedded(files map[string]string) fstest.MapFS {
	out := fstest.MapFS{}
	for name, body := range files {
		out["pack/"+name] = &fstest.MapFile{Data: []byte(body)}
		// A second, frozen pack sits beside the current one in the real
		// embedded filesystem; nothing here may read from it.
		out["pack-v5/"+name] = &fstest.MapFile{Data: []byte("frozen " + body)}
	}
	return out
}

func installed(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestDiffReportsNothingWhenContentMatches(t *testing.T) {
	root := installed(t, map[string]string{"prompts/coder.md": "a", "pack.json": "{}"})
	changes, err := Diff(root, embedded(map[string]string{"prompts/coder.md": "a", "pack.json": "{}"}), "pack")
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("identical packs must report no changes, got %+v", changes)
	}
}

func TestDiffReportsUpdatedAddedAndRemoved(t *testing.T) {
	root := installed(t, map[string]string{"prompts/coder.md": "old", "prompts/gone.md": "x"})
	changes, err := Diff(root, embedded(map[string]string{"prompts/coder.md": "new", "prompts/fresh.md": "y"}), "pack")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, change := range changes {
		got[change.Path] = change.Kind
	}
	want := map[string]string{"prompts/coder.md": "updated", "prompts/fresh.md": "added", "prompts/gone.md": "removed"}
	for path, kind := range want {
		if got[path] != kind {
			t.Errorf("%s: want %s, got %q", path, kind, got[path])
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected extra changes: %+v", got)
	}
}

func TestDiffIsSortedForStableOutput(t *testing.T) {
	root := installed(t, map[string]string{})
	changes, err := Diff(root, embedded(map[string]string{"z.md": "1", "a.md": "2", "m.md": "3"}), "pack")
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 3 || changes[0].Path != "a.md" || changes[2].Path != "z.md" {
		t.Fatalf("changes must be sorted by path, got %+v", changes)
	}
}

func TestDecideSkipsDevelopmentBuilds(t *testing.T) {
	// A local build carries no release version; gating anyone's run on it would
	// block work that has nothing to do with pack drift.
	if got := Decide(packstate.Stamp{InstalledFrom: "v0.1.0"}, "dev", true); got != Proceed {
		t.Fatalf("a dev binary must proceed, got %v", got)
	}
}

func TestDecideProceedsWhenInstalledByThisVersion(t *testing.T) {
	if got := Decide(packstate.Stamp{InstalledFrom: "v0.7.0"}, "v0.7.0", true); got != Proceed {
		t.Fatalf("want Proceed, got %v", got)
	}
}

func TestDecideProceedsWhenAcknowledgedForThisVersion(t *testing.T) {
	stamp := packstate.Stamp{InstalledFrom: "v0.6.1", Acknowledged: "v0.7.0"}
	if got := Decide(stamp, "v0.7.0", true); got != Proceed {
		t.Fatalf("an explicit keep must survive, got %v", got)
	}
}

func TestDecideRestampsWhenNoFileWouldChange(t *testing.T) {
	// A release that ships an untouched pack must not interrupt anyone.
	if got := Decide(packstate.Stamp{InstalledFrom: "v0.6.1"}, "v0.7.0", false); got != Restamp {
		t.Fatalf("want Restamp, got %v", got)
	}
}

func TestDecideBlocksWhenAnOlderVersionInstalledDifferentFiles(t *testing.T) {
	if got := Decide(packstate.Stamp{InstalledFrom: "v0.6.1"}, "v0.7.0", true); got != Block {
		t.Fatalf("want Block, got %v", got)
	}
}

func TestDecideBlocksWhenThereIsNoStampAtAll(t *testing.T) {
	// Projects that predate the stamp read as the initial version.
	if got := Decide(packstate.Stamp{}, "v0.7.0", true); got != Block {
		t.Fatalf("an unstamped pack with real drift must block, got %v", got)
	}
}

func TestDecideIgnoresAnAcknowledgementForADifferentVersion(t *testing.T) {
	stamp := packstate.Stamp{InstalledFrom: "v0.6.0", Acknowledged: "v0.6.1"}
	if got := Decide(stamp, "v0.7.0", true); got != Block {
		t.Fatalf("a stale keep must not silence a newer release, got %v", got)
	}
}

func TestApplyMakesTheInstalledPackIdenticalToTheEmbeddedOne(t *testing.T) {
	root := installed(t, map[string]string{"prompts/coder.md": "old", "prompts/gone.md": "x"})
	source := embedded(map[string]string{"prompts/coder.md": "new", "prompts/fresh.md": "y"})
	if err := Apply(root, source, "pack"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	changes, err := Diff(root, source, "pack")
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("apply must leave no drift, got %+v", changes)
	}
}

func TestApplyRemovesFilesTheEmbeddedPackNoLongerShips(t *testing.T) {
	// A leftover file is not merely stale: flow.LoadPack rejects any file the
	// manifest does not list, so leaving it behind breaks every later run.
	root := installed(t, map[string]string{"prompts/gone.md": "x"})
	if err := Apply(root, embedded(map[string]string{"prompts/coder.md": "a"}), "pack"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "prompts", "gone.md")); !os.IsNotExist(err) {
		t.Fatalf("dropped file survived: %v", err)
	}
}

func TestApplyCreatesAPackRootThatDoesNotExistYet(t *testing.T) {
	root := filepath.Join(t.TempDir(), "packs", "development", "5")
	if err := Apply(root, embedded(map[string]string{"prompts/coder.md": "a"}), "pack"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(root, "prompts", "coder.md"))
	if err != nil || string(body) != "a" {
		t.Fatalf("want the file written, got %q err=%v", body, err)
	}
}

func TestApplyLeavesNoEmptyDirectoriesBehind(t *testing.T) {
	root := installed(t, map[string]string{"legacy/old.md": "x"})
	if err := Apply(root, embedded(map[string]string{"prompts/coder.md": "a"}), "pack"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "legacy")); !os.IsNotExist(err) {
		t.Fatalf("empty directory survived: %v", err)
	}
}

// The embedded filesystem holds more than one pack. Reading the wrong one would
// silently compare a project against a frozen copy it does not run.
func TestDiffReadsOnlyTheNamedPack(t *testing.T) {
	root := installed(t, map[string]string{"a.md": "current"})
	changes, err := Diff(root, embedded(map[string]string{"a.md": "current"}), "pack")
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("the frozen sibling pack must not leak into the diff: %+v", changes)
	}
}
