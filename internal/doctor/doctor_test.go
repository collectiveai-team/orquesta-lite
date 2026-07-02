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
