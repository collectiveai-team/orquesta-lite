package packstate

import (
	"path/filepath"
	"testing"
)

func TestLoadMissingFileReportsInitialStamp(t *testing.T) {
	stamp := Load(t.TempDir(), "development", "5")
	if stamp.InstalledFrom != "" || stamp.Acknowledged != "" {
		t.Fatalf("a project with no state file must read as the initial stamp, got %+v", stamp)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, "development", "5", Stamp{InstalledFrom: "v0.7.0", Acknowledged: "v0.7.0"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	stamp := Load(dir, "development", "5")
	if stamp.InstalledFrom != "v0.7.0" || stamp.Acknowledged != "v0.7.0" {
		t.Fatalf("round trip lost the stamp: %+v", stamp)
	}
}

func TestSavePreservesOtherPacks(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, "development", "5", Stamp{InstalledFrom: "v0.7.0"}); err != nil {
		t.Fatalf("save development: %v", err)
	}
	if err := Save(dir, "other", "2", Stamp{InstalledFrom: "v0.6.0"}); err != nil {
		t.Fatalf("save other: %v", err)
	}
	if got := Load(dir, "development", "5").InstalledFrom; got != "v0.7.0" {
		t.Fatalf("writing one pack erased another: development reads %q", got)
	}
}

// Versions are independent entries: acknowledging pack 5 must not silence pack 6.
func TestStampIsPerPackVersion(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, "development", "5", Stamp{Acknowledged: "v0.7.0"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := Load(dir, "development", "6").Acknowledged; got != "" {
		t.Fatalf("pack 6 inherited pack 5's acknowledgement: %q", got)
	}
}

func TestStateFileLivesOutsideThePackRoot(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, "development", "5", Stamp{InstalledFrom: "v0.7.0"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	// A file inside the pack root would break flow.LoadPack's unlisted-file check.
	if _, err := filepath.Rel(filepath.Join(dir, ".orquestalite", "packs"), Path(dir)); err == nil {
		if Path(dir) != filepath.Join(dir, ".orquestalite", "pack-state.json") {
			t.Fatalf("state file must sit at .orquestalite/pack-state.json, got %s", Path(dir))
		}
	}
}
