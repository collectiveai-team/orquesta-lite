package doctor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunEmptyDirReportsTeamJSONError(t *testing.T) {
	checks := Run(context.Background(), t.TempDir())
	for _, check := range checks {
		if check.Name == "team.json" && check.Status == StatusError {
			return
		}
	}
	t.Fatalf("checks = %+v", checks)
}

func TestProviderHasUsableCredentialsUnknownProvider(t *testing.T) {
	if !ProviderHasUsableCredentials("mystery-provider") {
		t.Fatal("unknown providers must be assumed usable")
	}
}

func TestProviderHasUsableCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GEMINI_API_KEY", "")
	if ProviderHasUsableCredentials("gemini") {
		t.Fatal("gemini should be unusable without credentials")
	}
	t.Setenv("GEMINI_API_KEY", "test-key")
	if !ProviderHasUsableCredentials("gemini") {
		t.Fatal("gemini should be usable with GEMINI_API_KEY")
	}
	t.Setenv("GEMINI_API_KEY", "")
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gemini", "oauth_creds.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !ProviderHasUsableCredentials("gemini") {
		t.Fatal("gemini should be usable with cached credentials")
	}
}
