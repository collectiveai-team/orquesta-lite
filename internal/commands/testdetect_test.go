package commands

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectTestCommand(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"python pyproject", map[string]string{"pyproject.toml": ""}, "uv run pytest -q"},
		{"python requirements", map[string]string{"requirements.txt": ""}, "pytest -q"},
		{"node", map[string]string{"package.json": "{}"}, "npm test --silent"},
		{"go", map[string]string{"go.mod": "module x"}, "go test ./..."},
		{"rust", map[string]string{"Cargo.toml": ""}, "cargo test"},
		{"makefile target wins", map[string]string{"Makefile": "test:\n\tgo test ./...\n", "go.mod": "module x"}, "make test"},
		{"loose python", map[string]string{"main.py": "print(1)"}, "pytest -q"},
		{"unknown", map[string]string{"README.txt": "hi"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range c.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := detectTestCommand(dir); got != c.want {
				t.Fatalf("detectTestCommand = %q, want %q", got, c.want)
			}
		})
	}
}

func TestPersistTestCommand(t *testing.T) {
	dir := t.TempDir()
	teamPath := filepath.Join(dir, "team.json")
	original := "{\n  \"name\": \"x\",\n  \"full_test_command\": \"\"\n}\n"
	if err := os.WriteFile(teamPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := persistTestCommand(teamPath, "uv run pytest -q"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(teamPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"name\": \"x\",\n  \"full_test_command\": \"uv run pytest -q\"\n}\n"
	if string(got) != want {
		t.Fatalf("persisted team.json =\n%s\nwant\n%s", got, want)
	}

	// Idempotent: a non-empty command line is left untouched.
	if err := persistTestCommand(teamPath, "go test ./..."); err != nil {
		t.Fatal(err)
	}
	got2, _ := os.ReadFile(teamPath)
	if string(got2) != want {
		t.Fatalf("second persist mutated populated command:\n%s", got2)
	}
}
