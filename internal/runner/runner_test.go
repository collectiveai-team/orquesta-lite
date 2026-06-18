package runner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunAgent_WritesResultJSON(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}
	dir := t.TempDir()
	resultPath := filepath.Join(dir, "out.json")

	cmdTpl := []string{"sh", "-c", "echo $0 > /dev/null; printf '%s' '{\"status\":\"ok\"}' > " + resultPath, "{{PROMPT}}"}

	res, err := RunAgent(context.Background(), Spec{
		Cmd:              cmdTpl,
		Prompt:           "hello world",
		ResultPath:       resultPath,
		Timeout:          5 * time.Second,
		RateLimitPattern: "rate_?limit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RateLimited {
		t.Errorf("unexpected rate_limited")
	}
	raw, _ := os.ReadFile(resultPath)
	if string(raw) != `{"status":"ok"}` {
		t.Errorf("result file content = %q", raw)
	}
}

func TestRunAgent_DetectsRateLimitFromStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}
	res, err := RunAgent(context.Background(), Spec{
		Cmd:              []string{"sh", "-c", "echo 'Error: rate_limit exceeded' 1>&2; exit 0", "{{PROMPT}}"},
		Prompt:           "x",
		ResultPath:       filepath.Join(t.TempDir(), "out.json"),
		Timeout:          5 * time.Second,
		RateLimitPattern: "rate_?limit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.RateLimited {
		t.Errorf("expected RateLimited=true")
	}
}

func TestRunAgent_DetectsInteractiveAuthPrompt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}
	// The real gemini-cli output when it falls back to an OAuth browser flow
	// with no TTY: an auth-page prompt followed by a cancellation.
	geminiAuthErr := "Opening authentication page in your browser. Do you want to continue? [Y/n]: " +
		"Error authenticating: FatalCancellationError: Authentication cancelled by user."
	res, err := RunAgent(context.Background(), Spec{
		Cmd:              []string{"sh", "-c", "echo '" + geminiAuthErr + "' 1>&2; exit 42", "{{PROMPT}}"},
		Prompt:           "x",
		ResultPath:       filepath.Join(t.TempDir(), "out.json"),
		Timeout:          5 * time.Second,
		RateLimitPattern: "rate_?limit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.AuthFailed {
		t.Errorf("expected AuthFailed=true for interactive auth prompt")
	}
	if res.RateLimited {
		t.Errorf("auth prompt must not be misclassified as a rate limit")
	}
}

func TestDetectAuthPrompt(t *testing.T) {
	yes := []string{
		"Opening authentication page in your browser.",
		"Error authenticating: FatalCancellationError",
		"Please log in with the CLI first",
		"not authenticated",
		"login required",
	}
	no := []string{
		"All 6 tests passed",
		"Implemented T002 and wrote results",
		"",
	}
	for _, s := range yes {
		if !detectAuthPrompt(s, "") {
			t.Errorf("expected auth prompt detected in %q", s)
		}
	}
	for _, s := range no {
		if detectAuthPrompt(s, "") {
			t.Errorf("did not expect auth prompt in %q", s)
		}
	}
}

func TestRunAgent_TimeoutKills(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}
	start := time.Now()
	res, err := RunAgent(context.Background(), Spec{
		Cmd:              []string{"sh", "-c", "sleep 5", "{{PROMPT}}"},
		Prompt:           "x",
		ResultPath:       filepath.Join(t.TempDir(), "out.json"),
		Timeout:          500 * time.Millisecond,
		RateLimitPattern: "rate_?limit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Errorf("expected TimedOut=true")
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("did not kill in time: %v", time.Since(start))
	}
}

func TestResult_StderrTail_ShortAndLong(t *testing.T) {
	// Short string — tail should return the full string.
	short := &Result{Stderr: "hello"}
	if got := short.StderrTail(2048); got != "hello" {
		t.Errorf("StderrTail short: got %q, want %q", got, "hello")
	}

	// Long string — tail should return exactly the last n bytes (UTF-8 safe).
	long := &Result{Stderr: strings.Repeat("ab", 2000)} // 4000 bytes
	got := long.StderrTail(2048)
	if len(got) != 2048 {
		t.Errorf("StderrTail long: got %d bytes, want 2048", len(got))
	}
	// Must be a valid UTF-8 suffix.
	if !strings.HasSuffix(long.Stderr, got) {
		t.Errorf("StderrTail long: result is not a suffix of original")
	}

	// StdoutTail mirrors the same logic.
	shortOut := &Result{Stdout: "world"}
	if got := shortOut.StdoutTail(2048); got != "world" {
		t.Errorf("StdoutTail short: got %q, want %q", got, "world")
	}
	longOut := &Result{Stdout: strings.Repeat("xy", 2000)} // 4000 bytes
	gotOut := longOut.StdoutTail(2048)
	if len(gotOut) != 2048 {
		t.Errorf("StdoutTail long: got %d bytes, want 2048", len(gotOut))
	}
	if !strings.HasSuffix(longOut.Stdout, gotOut) {
		t.Errorf("StdoutTail long: result is not a suffix of original")
	}
}

func TestResult_CodexHeader_ParsedWhenPresent(t *testing.T) {
	// Exercise via RunAgent: stdout contains the codex header, verify parsing.
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}

	dir := t.TempDir()
	resultPath := filepath.Join(dir, "out.json")

	// Write result file from the script; stdout contains the header.
	script := `printf '%s\n' 'OpenAI Codex v0.135.0' '--------' 'workdir: /some/path' 'model: gpt-5.5' 'provider: openai' 'approval: never' 'sandbox: workspace-write [workdir, /tmp]' '--------' 'rest of output'`
	res, err := RunAgent(context.Background(), Spec{
		Cmd:        []string{"sh", "-c", script + "; printf '%s' '{\"ok\":1}' > " + resultPath},
		Prompt:     "x",
		ResultPath: resultPath,
		Timeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.CodexHeader == nil {
		t.Fatal("CodexHeader is nil, want map with parsed keys")
	}
	checks := map[string]string{
		"workdir":  "/some/path",
		"model":    "gpt-5.5",
		"provider": "openai",
		"approval": "never",
		"sandbox":  "workspace-write [workdir, /tmp]",
	}
	for k, want := range checks {
		if got := res.CodexHeader[k]; got != want {
			t.Errorf("CodexHeader[%q] = %q, want %q", k, got, want)
		}
	}
}

func TestRunAgent_SubstitutesTemplateVars(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}
	res, err := RunAgent(context.Background(), Spec{
		Cmd:        []string{"/bin/sh", "-c", "echo '{{PROMPT}} {{RESULT_PATH}}'"},
		Prompt:     "hi",
		ResultPath: filepath.Join(t.TempDir(), "out.json"),
		Timeout:    5 * time.Second,
		TemplateVars: map[string]string{
			"RESULT_PATH": "/tmp/out.json",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "hi") {
		t.Errorf("stdout missing prompt substitution: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "/tmp/out.json") {
		t.Errorf("stdout missing RESULT_PATH substitution: %q", res.Stdout)
	}
}

func TestRunAgent_LeavesUnknownTemplateVarsAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}
	res, err := RunAgent(context.Background(), Spec{
		Cmd:          []string{"/bin/sh", "-c", "echo '{{UNKNOWN}}'"},
		Prompt:       "hi",
		ResultPath:   filepath.Join(t.TempDir(), "out.json"),
		Timeout:      5 * time.Second,
		TemplateVars: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "{{UNKNOWN}}") {
		t.Errorf("stdout should contain literal {{UNKNOWN}}, got: %q", res.Stdout)
	}
}

func TestRunAgent_SubstitutionOrderPromptFirst(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}
	// TemplateVars contains "PROMPT" key, but Spec.Prompt substitution runs first
	// so the {{PROMPT}} token is consumed before TemplateVars can touch it.
	res, err := RunAgent(context.Background(), Spec{
		Cmd:        []string{"/bin/sh", "-c", "echo '{{PROMPT}}'"},
		Prompt:     "real-prompt",
		ResultPath: filepath.Join(t.TempDir(), "out.json"),
		Timeout:    5 * time.Second,
		TemplateVars: map[string]string{
			"PROMPT": "should-not-appear",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "real-prompt") {
		t.Errorf("stdout missing Spec.Prompt value: %q", res.Stdout)
	}
	if strings.Contains(res.Stdout, "should-not-appear") {
		t.Errorf("stdout must not contain TemplateVars PROMPT value: %q", res.Stdout)
	}
}

func TestRunAgent_ProviderReceivesPromptOnStdinAndParsesJSONL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(dir, "out.json")
	script := `#!/bin/sh
cat > /dev/null
printf '%s\n' '{"type":"thread.started","thread_id":"thread-123"}'
printf '%s\n' '{"type":"item.started","item":{"type":"command_execution","command":"go test ./..."}}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"final answer"}}'
printf '%s' '{"status":"ok"}' > "` + resultPath + `"
`
	codexPath := filepath.Join(binDir, "codex")
	if err := os.WriteFile(codexPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	res, err := RunAgent(context.Background(), Spec{
		Provider:   "codex",
		Prompt:     "hello via stdin",
		ResultPath: resultPath,
		Timeout:    5 * time.Second,
		Model:      "gpt-test",
	})
	if err != nil {
		t.Fatal(err)
	}

	if res.SessionID != "thread-123" {
		t.Fatalf("SessionID = %q, want thread-123", res.SessionID)
	}
	if res.FinalText != "final answer" {
		t.Fatalf("FinalText = %q, want final answer", res.FinalText)
	}
	if len(res.Events) != 4 {
		t.Fatalf("len(Events) = %d, want 4: %#v", len(res.Events), res.Events)
	}
	if !res.ResultExists {
		t.Fatalf("ResultExists = false, want true")
	}
	if !strings.Contains(res.Stdout, `"thread.started"`) {
		t.Fatalf("stdout did not capture raw JSONL: %q", res.Stdout)
	}
}

func TestRunAgent_ProviderPassesPromptThroughStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stdinPath := filepath.Join(dir, "stdin.txt")
	resultPath := filepath.Join(dir, "out.json")
	script := `#!/bin/sh
cat > "` + stdinPath + `"
printf '%s\n' '{"type":"thread.started","thread_id":"thread-123"}'
printf '%s' '{"status":"ok"}' > "` + resultPath + `"
`
	codexPath := filepath.Join(binDir, "codex")
	if err := os.WriteFile(codexPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	res, err := RunAgent(context.Background(), Spec{
		Provider:   "codex",
		Prompt:     "hello via stdin",
		ResultPath: resultPath,
		Timeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "hello via stdin" {
		t.Fatalf("stdin = %q, want prompt", raw)
	}
	if !res.ResultExists {
		t.Fatalf("ResultExists = false, want true")
	}
}

func TestRunAgent_ProviderStdoutJSONLDoesNotFalsePositiveRateLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(dir, "out.json")
	script := `#!/bin/sh
cat > /dev/null
printf '%s\n' '{"type":"thread.started","thread_id":"thread-429-ok"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"wrote result successfully"}}'
printf '%s' '{"status":"ok"}' > "` + resultPath + `"
`
	codexPath := filepath.Join(binDir, "codex")
	if err := os.WriteFile(codexPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	res, err := RunAgent(context.Background(), Spec{
		Provider:         "codex",
		Prompt:           "hello",
		ResultPath:       resultPath,
		Timeout:          5 * time.Second,
		RateLimitPattern: "rate_?limit|429|quota",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RateLimited {
		t.Fatalf("RateLimited = true for successful provider JSONL stdout: %q", res.Stdout)
	}
	if !res.ResultExists {
		t.Fatalf("ResultExists = false, want true")
	}
}

func TestRunAgent_ProviderErrorEventDetectsRateLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
cat > /dev/null
printf '%s\n' '{"type":"error","message":"429 rate limit exceeded"}'
`
	codexPath := filepath.Join(binDir, "codex")
	if err := os.WriteFile(codexPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	res, err := RunAgent(context.Background(), Spec{
		Provider:         "codex",
		Prompt:           "hello",
		ResultPath:       filepath.Join(dir, "missing.json"),
		Timeout:          5 * time.Second,
		RateLimitPattern: "rate_?limit|429|quota",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.RateLimited {
		t.Fatalf("RateLimited = false, want true for provider error event")
	}
}

func TestRunAgent_RemovesStaleResultBeforeInvocation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}
	dir := t.TempDir()
	resultPath := filepath.Join(dir, "out.json")
	if err := os.WriteFile(resultPath, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := RunAgent(context.Background(), Spec{
		Cmd:        []string{"sh", "-c", `if test -e "$1"; then exit 3; fi`, "{{PROMPT}}", resultPath},
		Prompt:     "x",
		ResultPath: resultPath,
		Timeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, stale result was not removed", res.ExitCode)
	}
	if res.ResultExists {
		t.Fatalf("ResultExists = true, want false")
	}
}

func TestResult_CodexHeader_NilWhenAbsent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}
	dir := t.TempDir()
	resultPath := filepath.Join(dir, "out.json")

	res, err := RunAgent(context.Background(), Spec{
		Cmd:        []string{"sh", "-c", "echo 'normal output'; printf '%s' '{\"ok\":1}' > " + resultPath},
		Prompt:     "x",
		ResultPath: resultPath,
		Timeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.CodexHeader != nil {
		t.Errorf("CodexHeader = %v, want nil for non-codex stdout", res.CodexHeader)
	}
}
