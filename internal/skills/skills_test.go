package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoad_ParsesFrontMatterAndProcedure(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "tdd.md", "---\nname: tdd\ndescription: Test-driven loop.\n---\nWrite a failing test first.\nThen implement.\n")
	writeFile(t, dir, "debug.md", "---\nname: debug\ndescription: Diagnose a bug.\n---\nReproduce then minimise.\n")

	r, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	tdd, ok := r.Get("tdd")
	if !ok {
		t.Fatal("missing tdd")
	}
	if tdd.Description != "Test-driven loop." {
		t.Errorf("description = %q", tdd.Description)
	}
	if !strings.Contains(tdd.Procedure, "Write a failing test first.") {
		t.Errorf("procedure = %q", tdd.Procedure)
	}
	if got := r.Names(); len(got) != 2 || got[0] != "debug" || got[1] != "tdd" {
		t.Errorf("Names = %v", got)
	}
}

func TestLoad_NameFallsBackToFilename(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "no-name.md", "Just a procedure body.\n")
	r, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := r.Get("no-name")
	if !ok {
		t.Fatal("missing no-name")
	}
	if !strings.Contains(s.Procedure, "Just a procedure body.") {
		t.Errorf("procedure = %q", s.Procedure)
	}
}

func TestLoad_MissingDirIsEmptyRegistry(t *testing.T) {
	r, err := Load(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("missing dir should not error, got %v", err)
	}
	if len(r.Names()) != 0 {
		t.Errorf("expected empty registry, got %v", r.Names())
	}
}

func TestRender_EmptyYieldsPlaceholder(t *testing.T) {
	r, _ := Load(t.TempDir())
	got, err := r.Render(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "no skills requested") {
		t.Errorf("placeholder = %q", got)
	}
}

func TestRender_RendersRequestedSkills(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "tdd.md", "---\nname: tdd\ndescription: TDD.\n---\nred green refactor\n")
	writeFile(t, dir, "debug.md", "---\nname: debug\ndescription: Debug.\n---\nreproduce minimise\n")
	r, _ := Load(dir)
	got, err := r.Render([]string{"tdd", "debug"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Skill: tdd", "TDD.", "red green refactor", "Skill: debug", "reproduce minimise"} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing %q:\n%s", want, got)
		}
	}
}

func TestRender_Dedupes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "tdd.md", "---\nname: tdd\ndescription: TDD.\n---\nbody\n")
	r, _ := Load(dir)
	got, err := r.Render([]string{"tdd", "tdd"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "Skill: tdd") != 1 {
		t.Errorf("expected one tdd block, got:\n%s", got)
	}
}

func TestRender_MissingSkillIsError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "tdd.md", "---\nname: tdd\ndescription: TDD.\n---\nbody\n")
	r, _ := Load(dir)
	_, err := r.Render([]string{"nope"})
	if err == nil || !strings.Contains(err.Error(), `"nope" not found`) {
		t.Fatalf("expected clear not-found error, got %v", err)
	}
}

func TestRender_SkipsBlankNames(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "tdd.md", "---\nname: tdd\ndescription: TDD.\n---\nbody\n")
	r, _ := Load(dir)
	got, err := r.Render([]string{"", "tdd"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Skill: tdd") {
		t.Errorf("render = %q", got)
	}
}
