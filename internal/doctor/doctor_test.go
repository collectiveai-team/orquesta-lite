package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestRunVerifiesProviderEmittedFlags(t *testing.T) {
	validHelp := `opencode run [message..]
Options:
  --format       output format
  --print-logs   print logs
  -m, --model    model
  --variant      effort
  -s, --session  session
  --auto         approve permissions
  --thinking     show thinking
`
	for name, test := range map[string]struct {
		help       string
		extraArgs  string
		wantStatus Status
		wantDetail string
	}{
		"valid": {
			help: validHelp, extraArgs: `"extra_args":["--thinking"],`, wantStatus: StatusOK,
		},
		"missing emitted flag": {
			help:       strings.ReplaceAll(validHelp, "  --auto         approve permissions\n", ""),
			wantStatus: StatusError, wantDetail: "--auto",
		},
		"unparseable help": {
			help:       "opencode run [message..]\nno options here\n",
			wantStatus: StatusError, wantDetail: "no parseable options",
		},
		"controlled extra flag": {
			help: validHelp, extraArgs: `"extra_args":["--format","text"],`,
			wantStatus: StatusError, wantDetail: "controls flag",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := doctorProject(t, test.help, test.extraArgs)
			check := checkByName(Run(context.Background(), dir), "provider:opencode")
			if check.Status != test.wantStatus || !strings.Contains(check.Detail, test.wantDetail) {
				t.Fatalf("provider check = %+v, want status %s containing %q", check, test.wantStatus, test.wantDetail)
			}
		})
	}
}

func doctorProject(t *testing.T, help, extraArgs string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat <<'EOF'\n" + help + "EOF\n"
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	team := `{
  "agents":{"oc":{"provider":"opencode","model":"openai/test","effort":"high","dangerously_skip_permissions":true,` + extraArgs + `"rate_limit_pattern":"429"}},
  "roles":{"coder":{"agents":["oc"],"prompt":"prompt.md","result_path":"result.json","timeout_seconds":1}},
  "rate_limit_backoff":{"initial_seconds":1,"factor":2,"max_seconds":2},
  "runtime":{"context_optimization":{"compression_proxy":{"enabled":false},"command_filter":{"enabled":false}}},
  "lint_argv":["true"],"test_argv":["true"]
}`
	if err := os.WriteFile(filepath.Join(dir, "team.json"), []byte(team), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func checkByName(checks []Check, name string) Check {
	for _, check := range checks {
		if check.Name == name {
			return check
		}
	}
	return Check{}
}
