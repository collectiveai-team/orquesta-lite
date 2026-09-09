package doctor

import (
	"context"
	"fmt"
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

// TestKeychainProvidersStayOutOfCredentialPaths pins the trap this check had to
// avoid. credentialPaths drives ProviderHasUsableCredentials, which the
// run-time static preflight uses to skip agents that cannot authenticate. An
// entry with no file and no env var would therefore mark every agy agent
// unusable and end a run with "all agents for role X are marked skipped" —
// a role error for a provider that is in fact logged in.
func TestKeychainProvidersStayOutOfCredentialPaths(t *testing.T) {
	for provider := range keychainProviders {
		if _, listed := credentialPaths[provider]; listed {
			t.Fatalf("%s is in credentialPaths: the static preflight will skip all of its agents", provider)
		}
		if !ProviderHasUsableCredentials(provider) {
			t.Fatalf("%s must be assumed usable: its session cannot be proven from disk", provider)
		}
	}
}

// TestEveryKeychainProviderHasASessionProbe pins the rule that replaced the
// permanent warning. A provider whose credentials cannot be read from disk and
// that has no probe either can only be reported as "cannot verify" forever,
// which is the state a reader stops reading. Adding one without the other must
// break the build's tests, not ship silence.
func TestEveryKeychainProviderHasASessionProbe(t *testing.T) {
	for provider := range keychainProviders {
		if _, ok := sessionProbes[provider]; !ok {
			t.Fatalf("%s has no session probe, so doctor can only ever say it cannot verify it", provider)
		}
	}
}

// TestCredentialCheckIsSilentForUnknownProviders pins that a provider with no
// declared profile still emits nothing, which is the pre-existing behaviour for
// a custom cmd agent.
func TestCredentialCheckIsSilentForUnknownProviders(t *testing.T) {
	if _, _, reportable := credentialCheck(context.Background(), "mystery-provider", "mystery-provider"); reportable {
		t.Fatal("an unknown provider must not produce a credentials check")
	}
}

// agyProject fabricates a project whose only agent uses agy, with a fake `agy`
// on PATH whose `models` subcommand exits with the given status. `models` is
// the real session probe: it posts to loadCodeAssist, so it cannot succeed
// without credentials.
func agyProject(t *testing.T, modelsExit int, modelsOutput string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$1\" in\n  models) echo '" + modelsOutput + "'; exit " +
		fmt.Sprint(modelsExit) + " ;;\n  *) echo 'Usage of agy:'; echo '  --print string'; exit 0 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "agy"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	team := `{
  "agents":{"a":{"provider":"agy","model":"gemini-3.8-flash-high","rate_limit_pattern":"429"}},
  "roles":{"coder":{"agents":["a"],"prompt":"prompt.md","result_path":"result.json","timeout_seconds":1}},
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

// TestAgySessionProbePasses pins that a logged-in machine gets a pass, not a
// permanent warning. The previous check reported agy as unverifiable on every
// run, which is the state a reader learns to scroll past — and it did mislead
// one into concluding the provider was unavailable.
func TestAgySessionProbePasses(t *testing.T) {
	checks := Run(context.Background(), agyProject(t, 0, "gemini-3.8-flash-high"))
	got := checkByName(checks, "credentials:agy")
	if got.Status != StatusOK {
		t.Fatalf("status = %v, want %v (detail: %s)", got.Status, StatusOK, got.Detail)
	}
}

// TestAgySessionProbeReportsTheCLIsOwnError pins that a failed probe hands back
// what the CLI said. "cannot be verified" tells an operator nothing to act on;
// "Eligibility check failed: Post ... connection refused" tells them whether
// they are logged out or offline.
func TestAgySessionProbeReportsTheCLIsOwnError(t *testing.T) {
	dir := agyProject(t, 1, "Error: Eligibility check failed: no credentials")
	got := checkByName(Run(context.Background(), dir), "credentials:agy")
	if got.Status != StatusWarn {
		t.Fatalf("status = %v, want %v", got.Status, StatusWarn)
	}
	if !strings.Contains(got.Detail, "Eligibility check failed") {
		t.Fatalf("detail drops the CLI's own message: %q", got.Detail)
	}
}
