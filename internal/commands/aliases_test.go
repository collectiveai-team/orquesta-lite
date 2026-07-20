package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckV2FactoryStartProtectsUnfinishedLegacyState(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, ".orquestalite")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "factory.json"), []byte(`{"features":[{"id":"F001","status":"pending"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckV2FactoryStart(dir, false); err == nil {
		t.Fatal("expected guard")
	}
	if err := CheckV2FactoryStart(dir, true); err != nil {
		t.Fatal(err)
	}
}

func TestValidateEngine(t *testing.T) {
	if ValidateEngine("legacy") != nil || ValidateEngine("v2") != nil {
		t.Fatal("valid engines rejected")
	}
	if ValidateEngine("other") == nil {
		t.Fatal("invalid engine accepted")
	}
}

func TestCheckV2RunStartProtectsUnfinishedLegacyTasks(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".orquestalite"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".orquestalite", "tasks.json"), []byte(`{"tasks":[{"id":"T001","status":"in_progress"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckV2RunStart(dir, false); err == nil {
		t.Fatal("expected legacy tasks guard")
	}
	if err := CheckV2RunStart(dir, true); err != nil {
		t.Fatal(err)
	}
}

func TestSelectDevelopmentEngineDrainsImplicitV2DefaultThroughLegacy(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".orquestalite"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".orquestalite", "tasks.json"), []byte(`{"tasks":[{"id":"T001","status":"pending"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	selected, err := SelectDevelopmentEngine(dir, "run", "v2", false, false, &out)
	if err != nil || selected != "legacy" || !strings.Contains(out.String(), "routing to --engine=legacy") {
		t.Fatalf("selected=%q err=%v output=%q", selected, err, out.String())
	}
	if _, err = SelectDevelopmentEngine(dir, "run", "v2", true, false, &out); err == nil {
		t.Fatal("explicit v2 must reject unfinished legacy state")
	}
	selected, err = SelectDevelopmentEngine(dir, "run", "v2", true, true, &out)
	if err != nil || selected != "v2" {
		t.Fatalf("forced selected=%q err=%v", selected, err)
	}
}
