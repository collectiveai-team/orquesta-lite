package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun_EmptyDirReportsTeamJSONError(t *testing.T) {
	checks := Run(context.Background(), t.TempDir())
	byName := map[string]Check{}
	for _, c := range checks {
		byName[c.Name] = c
	}
	team, ok := byName["team.json"]
	if !ok || team.Status != StatusError {
		t.Fatalf("team.json check = %+v", team)
	}
	ev, ok := byName["eventdb"]
	if !ok || ev.Status != StatusWarn || !strings.Contains(ev.Detail, "orq-lite index") {
		t.Fatalf("eventdb check = %+v", ev)
	}
	for _, c := range checks {
		switch c.Status {
		case StatusOK, StatusWarn, StatusError:
		default:
			t.Fatalf("check %q has invalid status %q", c.Name, c.Status)
		}
	}
}

// Credential probing (moved from internal/commands/doctorcmd_test.go —
// keep the original assertions, they exercise ProviderHasUsableCredentials
// with t.Setenv / fake HOME).
func TestRun_PackOnlyTeamResolvesWithLegacyWarn(t *testing.T) {
	dir := t.TempDir()
	team := `{
  "agents": {"haiku": {"provider": "claude", "model": "claude-haiku-4-5-20251001"}},
  "roles": {
    "coder": {"agents": ["haiku"], "prompt": "prompts/coder.md", "result_path": ".orquestalite/results/coder.json", "timeout_seconds": 60}
  },
  "limits": {"max_review_cycles": 1, "max_fix_iterations": 1},
  "rate_limit_backoff": {"initial_seconds": 1, "factor": 2, "max_seconds": 2}
}`
	if err := os.WriteFile(filepath.Join(dir, "team.json"), []byte(team), 0o644); err != nil {
		t.Fatal(err)
	}
	checks := Run(context.Background(), dir)
	byName := map[string]Check{}
	for _, c := range checks {
		byName[c.Name] = c
	}
	if tc := byName["team.json"]; tc.Status != StatusOK {
		t.Fatalf("team.json = %+v, want StatusOK for a pack-only team", tc)
	}
	legacy, ok := byName["legacy roles"]
	if !ok || legacy.Status != StatusWarn {
		t.Fatalf("legacy roles = %+v, want StatusWarn", legacy)
	}
	for _, role := range []string{"parser", "tester", "reviewer", "critic"} {
		if !strings.Contains(legacy.Detail, role) {
			t.Fatalf("legacy roles detail %q missing %q", legacy.Detail, role)
		}
	}
}

func TestRun_FullLegacyTeamHasNoLegacyRolesWarn(t *testing.T) {
	dir := t.TempDir()
	role := func(name string) string {
		return `"` + name + `": {"agents": ["haiku"], "prompt": "prompts/` + name + `.md", "result_path": ".orquestalite/results/` + name + `.json", "timeout_seconds": 60}`
	}
	team := `{
  "agents": {"haiku": {"provider": "claude", "model": "claude-haiku-4-5-20251001"}},
  "roles": {` + role("parser") + `,` + role("coder") + `,` + role("tester") + `,` + role("critic") + `,` + role("reviewer") + `},
  "limits": {"max_review_cycles": 1, "max_fix_iterations": 1},
  "rate_limit_backoff": {"initial_seconds": 1, "factor": 2, "max_seconds": 2}
}`
	if err := os.WriteFile(filepath.Join(dir, "team.json"), []byte(team), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range Run(context.Background(), dir) {
		if c.Name == "legacy roles" {
			t.Fatalf("unexpected legacy roles check for a full legacy team: %+v", c)
		}
	}
}

func TestProviderHasUsableCredentials_UnknownProviderAssumedUsable(t *testing.T) {
	if !ProviderHasUsableCredentials("mystery-provider") {
		t.Fatal("unknown provider must be assumed usable")
	}
}

func TestProviderHasUsableCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GEMINI_API_KEY", "")

	// A provider we have no credential profile for: assume usable (cannot tell).
	if !ProviderHasUsableCredentials("mystery-provider") {
		t.Errorf("unknown provider should be assumed usable")
	}

	// No env var and no cached login: not usable (this is the gemini case that
	// caused interactive auth prompts mid-run).
	if ProviderHasUsableCredentials("gemini") {
		t.Errorf("expected gemini unusable with no env var and no cached login")
	}

	// API-key env var present: usable headless.
	t.Setenv("GEMINI_API_KEY", "test-key")
	if !ProviderHasUsableCredentials("gemini") {
		t.Errorf("expected gemini usable via GEMINI_API_KEY")
	}
	t.Setenv("GEMINI_API_KEY", "")

	// Cached login file present: usable.
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gemini", "oauth_creds.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !ProviderHasUsableCredentials("gemini") {
		t.Errorf("expected gemini usable via cached login file")
	}
}
