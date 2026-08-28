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

// fakeOpenCode installs a stub `opencode` on PATH that records the argv it was
// called with and prints the supplied JSONL lines. It returns the path the argv
// is recorded to.
func fakeOpenCode(t *testing.T, dir, stdout string) string {
	t.Helper()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	argvPath := filepath.Join(dir, "argv.txt")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> \"" + argvPath + "\"; done\n" + stdout
	if err := os.WriteFile(filepath.Join(binDir, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argvPath
}

// TestRunAgent_AttachFlagsReachTheCLI closes the loop between Spec and argv:
// the provider adapter is unit-tested on its own, but nothing else proves the
// runner actually forwards the attach fields into provider Options.
func TestRunAgent_AttachFlagsReachTheCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}
	dir := t.TempDir()
	resultPath := filepath.Join(dir, "out.json")
	argvPath := fakeOpenCode(t, dir, "printf '%s' '{\"status\":\"ok\"}' > \""+resultPath+"\"\n")

	res, err := RunAgent(context.Background(), Spec{
		Provider:        "opencode",
		Prompt:          "hello",
		ResultPath:      resultPath,
		Timeout:         5 * time.Second,
		Model:           "m",
		AttachURL:       "http://127.0.0.1:4096",
		AttachDir:       "/proj",
		ResumeSessionID: "ses_child",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.ResultExists {
		t.Fatal("stub did not run")
	}

	raw, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimSpace(string(raw)), "\n")
	want := map[string]string{"--attach": "http://127.0.0.1:4096", "--dir": "/proj", "-s": "ses_child"}
	for flag, value := range want {
		idx := -1
		for i, a := range argv {
			if a == flag {
				idx = i
				break
			}
		}
		if idx < 0 {
			t.Fatalf("%s missing from argv: %v", flag, argv)
		}
		if idx+1 >= len(argv) || argv[idx+1] != value {
			t.Fatalf("%s value = %v, want %q (argv %v)", flag, argv[idx+1:], value, argv)
		}
	}
	if argv[len(argv)-1] != "hello" {
		t.Fatalf("prompt must stay the final positional arg, got %q", argv[len(argv)-1])
	}
}

// TestRunAgent_RecordsAbort pins the finding that motivates Result.Aborted:
// opencode exits 0 when a session is aborted, so the exit code cannot be used
// to tell a cancellation from a clean run that wrote no result.
func TestRunAgent_RecordsAbort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}
	dir := t.TempDir()
	resultPath := filepath.Join(dir, "out.json")
	fakeOpenCode(t, dir, `printf '%s\n' '{"type":"step_start","sessionID":"ses_x","part":{"id":"p1"}}'
printf '%s\n' '{"type":"error","sessionID":"ses_x","error":{"name":"MessageAbortedError","data":{"message":"Aborted"}}}'
exit 0
`)

	res, err := RunAgent(context.Background(), Spec{
		Provider:   "opencode",
		Prompt:     "hello",
		ResultPath: resultPath,
		Timeout:    5 * time.Second,
		Model:      "m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("stub should exit 0 like the real CLI, got %d", res.ExitCode)
	}
	if res.ResultExists {
		t.Fatal("aborted run should have written no result")
	}
	if !res.Aborted {
		t.Fatal("abort event did not set Result.Aborted — a cancellation is indistinguishable from a clean empty run")
	}
}

// A normal run must leave Aborted false, otherwise the terminal-abort handling
// would swallow every ordinary invocation.
func TestRunAgent_NormalRunIsNotAborted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}
	dir := t.TempDir()
	resultPath := filepath.Join(dir, "out.json")
	fakeOpenCode(t, dir, `printf '%s\n' '{"type":"text","sessionID":"ses_x","part":{"id":"p1","text":"done"}}'
printf '%s' '{"status":"ok"}' > "`+resultPath+`"
`)

	res, err := RunAgent(context.Background(), Spec{
		Provider: "opencode", Prompt: "hello", ResultPath: resultPath,
		Timeout: 5 * time.Second, Model: "m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Aborted {
		t.Fatal("a successful run was marked aborted")
	}
	if res.FinalText != "done" {
		t.Fatalf("FinalText = %q, want done", res.FinalText)
	}
}

// Without attach, no attach flags may appear — attach mode stays opt-in all the
// way down to argv.
func TestRunAgent_NoAttachFlagsWhenUnset(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}
	dir := t.TempDir()
	resultPath := filepath.Join(dir, "out.json")
	argvPath := fakeOpenCode(t, dir, "printf '%s' '{\"status\":\"ok\"}' > \""+resultPath+"\"\n")

	if _, err := RunAgent(context.Background(), Spec{
		Provider: "opencode", Prompt: "hello", ResultPath: resultPath,
		Timeout: 5 * time.Second, Model: "m",
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--attach", "--dir"} {
		if strings.Contains(string(raw), flag) {
			t.Fatalf("%s present without attach configured: %s", flag, raw)
		}
	}
}
