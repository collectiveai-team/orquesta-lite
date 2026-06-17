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

func TestDetectLintCommand(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"ruff via pyproject", map[string]string{"pyproject.toml": "[tool.ruff]\nline-length = 100\n"}, "ruff check ."},
		{"ruff via ruff.toml", map[string]string{"ruff.toml": ""}, "ruff check ."},
		{"eslint config", map[string]string{".eslintrc.json": "{}"}, "npx --no-install eslint ."},
		{"eslint in package.json", map[string]string{"package.json": "{\"devDependencies\":{\"eslint\":\"9\"}}"}, "npx --no-install eslint ."},
		{"go vet", map[string]string{"go.mod": "module x"}, "go vet ./..."},
		{"rust clippy", map[string]string{"Cargo.toml": ""}, "cargo clippy"},
		{"python without ruff config", map[string]string{"pyproject.toml": "[project]\nname = 'x'\n"}, ""},
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
			if got := detectLintCommand(dir); got != c.want {
				t.Fatalf("detectLintCommand = %q, want %q", got, c.want)
			}
		})
	}
}

func TestPersistConfigString(t *testing.T) {
	dir := t.TempDir()
	teamPath := filepath.Join(dir, "team.json")
	original := "{\n  \"name\": \"x\",\n  \"full_test_command\": \"\",\n  \"lint_command\": \"\"\n}\n"
	if err := os.WriteFile(teamPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := persistConfigString(teamPath, "full_test_command", "uv run pytest -q"); err != nil {
		t.Fatal(err)
	}
	if err := persistConfigString(teamPath, "lint_command", "ruff check ."); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(teamPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"name\": \"x\",\n  \"full_test_command\": \"uv run pytest -q\",\n  \"lint_command\": \"ruff check .\"\n}\n"
	if string(got) != want {
		t.Fatalf("persisted team.json =\n%s\nwant\n%s", got, want)
	}

	// Idempotent: a non-empty value line is left untouched.
	if err := persistConfigString(teamPath, "full_test_command", "go test ./..."); err != nil {
		t.Fatal(err)
	}
	got2, _ := os.ReadFile(teamPath)
	if string(got2) != want {
		t.Fatalf("second persist mutated populated value:\n%s", got2)
	}

	// A key that is absent from the file is a no-op, not an error.
	if err := persistConfigString(teamPath, "missing_key", "x"); err != nil {
		t.Fatalf("persisting absent key should be a no-op, got %v", err)
	}
}
