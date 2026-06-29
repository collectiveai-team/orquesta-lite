package commands

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/config"
	"github.com/lionelchamorro/orquestalite/internal/eventlog"
)

// eventlogOpenDiscard opens a run.log logger whose pretty stream is discarded,
// so lintGateOutcome can log on the failure/skip paths without noisy output.
func eventlogOpenDiscard(logPath string) (*eventlog.Logger, error) {
	return eventlog.OpenWithFormat(logPath, io.Discard, eventlog.FormatVerbose)
}

func TestDetectPrecommitRuleset_PerLanguage(t *testing.T) {
	cases := []struct {
		name     string
		lang     string
		files    map[string]string
		wantName string
	}{
		{"go", "go", nil, "go"},
		{"python", "python", nil, "python"},
		{"javascript", "node", nil, "javascript"},
		{"typescript via tsconfig", "node", map[string]string{"tsconfig.json": "{}"}, "typescript"},
		{"unknown", "", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for n, body := range c.files {
				if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			rs := detectPrecommitRuleset(dir, c.lang)
			if c.wantName == "" {
				if rs.Name != "" {
					t.Fatalf("expected empty ruleset, got %q", rs.Name)
				}
				return
			}
			if rs.Name != c.wantName {
				t.Fatalf("Name = %q, want %q", rs.Name, c.wantName)
			}
			if !strings.HasPrefix(rs.LintCommand, "sh -c ") {
				t.Errorf("LintCommand should wrap a composite shell script, got %q", rs.LintCommand)
			}
			if !strings.Contains(rs.Config, "repos:") {
				t.Errorf("Config should be a pre-commit YAML repos block, got %q", rs.Config)
			}
		})
	}
}

func TestApplyPrecommit_WritesConfigAndSetsLintCommand(t *testing.T) {
	dir := t.TempDir()
	teamPath := filepath.Join(dir, "team.json")
	team := "{\n  \"full_test_command\": \"go test ./...\",\n  \"lint_command\": \"\"\n}\n"
	if err := os.WriteFile(teamPath, []byte(team), 0o644); err != nil {
		t.Fatal(err)
	}

	rs, err := applyPrecommit(dir, teamPath, "go")
	if err != nil {
		t.Fatal(err)
	}
	if rs.Name != "go" {
		t.Fatalf("ruleset = %q", rs.Name)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, ".pre-commit-config"))
	if !strings.Contains(string(cfg), "golangci-lint") {
		t.Errorf(".pre-commit-config missing golangci-lint hook:\n%s", cfg)
	}
	got, _ := os.ReadFile(teamPath)
	if !strings.Contains(string(got), `"lint_command": "sh -c `) {
		t.Errorf("team.json lint_command not set:\n%s", got)
	}
}

func TestApplyPrecommit_IdempotentLintCommand(t *testing.T) {
	dir := t.TempDir()
	teamPath := filepath.Join(dir, "team.json")
	team := "{\n  \"lint_command\": \"ruff check .\"\n}\n"
	if err := os.WriteFile(teamPath, []byte(team), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := applyPrecommit(dir, teamPath, "go"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(teamPath)
	// Already-populated lint_command must be left untouched (not overwritten).
	if !strings.Contains(string(got), `"lint_command": "ruff check ."`) {
		t.Errorf("populated lint_command was clobbered:\n%s", got)
	}
}

func TestParseCommand_ShcSingleArg(t *testing.T) {
	got := parseCommand("sh -c 'set -e; go vet ./...'")
	if len(got) != 3 || got[0] != "sh" || got[1] != "-c" || got[2] != "set -e; go vet ./..." {
		t.Fatalf("parseCommand = %#v, want [sh -c <script>]", got)
	}
	// No surrounding quotes: still one -c arg.
	got = parseCommand("sh -c set -e; ruff check .")
	if len(got) != 3 || got[2] != "set -e; ruff check ." {
		t.Fatalf("parseCommand unquoted = %#v", got)
	}
}

func TestParseCommand_PlainFields(t *testing.T) {
	got := parseCommand("ruff check .")
	if len(got) != 3 || got[0] != "ruff" {
		t.Fatalf("parseCommand plain = %#v", got)
	}
}

// TestLintGateOutcome_CompositeScript exercises the wired lint gate against a
// real composite `sh -c` script, proving a violation feeds feedback to the coder
// (the fix-loop contract) rather than aborting a commit, and that a missing
// primary tool is skipped via the in-script guard.
func TestLintGateOutcome_CompositeScript(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".orquestalite"), 0o755); err != nil {
		t.Fatal(err)
	}
	logger, err := eventlogOpenDiscard(filepath.Join(dir, ".orquestalite", "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	// Passing composite script.
	ld := &liveDeps{dir: dir}
	ld.cfg = &config.Config{LintCommand: "sh -c 'exit 0'"}
	ld.log = logger
	ok, fb := ld.lintGateOutcome(context.Background())
	if !ok || fb != "" {
		t.Fatalf("passing script: ok=%v fb=%q", ok, fb)
	}

	// Failing composite script: must report a violation with feedback.
	ld.cfg.LintCommand = "sh -c 'echo bad; exit 1'"
	ok, fb = ld.lintGateOutcome(context.Background())
	if ok {
		t.Fatalf("failing script: ok=true, want false")
	}
	if !strings.Contains(fb, "Lint gate failed") || !strings.Contains(fb, "bad") {
		t.Fatalf("feedback missing script output: %q", fb)
	}

	// Missing primary tool is a SKIP (the in-script guard exits 0), not a block.
	ld.cfg.LintCommand = "sh -c 'command -v nonexistent_tool_xyz >/dev/null 2>&1 || exit 0; nonexistent_tool_xyz'"
	ok, fb = ld.lintGateOutcome(context.Background())
	if !ok {
		t.Fatalf("missing primary tool should be skipped, not blocked: ok=%v fb=%q", ok, fb)
	}
}
