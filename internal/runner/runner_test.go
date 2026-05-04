package runner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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
