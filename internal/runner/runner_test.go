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
