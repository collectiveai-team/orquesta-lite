package providers

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestOpenCodeBuildAttach(t *testing.T) {
	launch, err := (&OpenCode{}).Build(context.Background(), "prompt", Options{
		AttachURL:       "http://127.0.0.1:4096",
		AttachDir:       "/proj",
		ResumeSessionID: "ses_child",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"opencode", "run", "--format", "json", "--print-logs=false",
		"-m", "anthropic/claude-sonnet-4-6",
		"--attach", "http://127.0.0.1:4096", "--dir", "/proj",
		"-s", "ses_child",
		"prompt",
	}
	if !reflect.DeepEqual(launch.Args, want) {
		t.Fatalf("attach args = %v, want %v", launch.Args, want)
	}
}

// TestOpenCodeBuildAttachRequiresDir guards the one hard coupling in the CLI:
// with --attach the server resolves paths on its side, so orq-lite's own
// working directory no longer locates the project.
func TestOpenCodeBuildAttachRequiresDir(t *testing.T) {
	_, err := (&OpenCode{}).Build(context.Background(), "prompt", Options{AttachURL: "http://127.0.0.1:4096"})
	if err == nil {
		t.Fatal("Build accepted --attach without a directory")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Fatalf("error should name the missing directory, got %q", err)
	}
}

// TestOpenCodeBuildPromptStaysLast pins the invariant that survives every
// future flag added to this function: `opencode run` takes the message as its
// final positional argument, so anything appended after it is read as more
// prompt rather than as a flag.
func TestOpenCodeBuildPromptStaysLast(t *testing.T) {
	launch, err := (&OpenCode{}).Build(context.Background(), "the prompt", Options{
		Model:                "openai/gpt-5.4",
		Effort:               "high",
		DangerouslySkipPerms: true,
		AttachURL:            "http://127.0.0.1:4096",
		AttachDir:            "/proj",
		ResumeSessionID:      "ses_child",
		ForkSession:          true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := launch.Args[len(launch.Args)-1]; got != "the prompt" {
		t.Fatalf("last arg = %q, want the prompt", got)
	}
}

func TestOpenCodeBuildNoAttachFlagsWhenUnset(t *testing.T) {
	launch, err := (&OpenCode{}).Build(context.Background(), "prompt", Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range launch.Args {
		if arg == "--attach" || arg == "--dir" {
			t.Fatalf("attach flag %q leaked into a non-attach run: %v", arg, launch.Args)
		}
	}
}

// TestOpenCodeRejectsAttachInExtraArgs keeps the two escape hatches from
// fighting. extra_args is a deliberate way to pass flags the adapter does not
// model, but attach mode only works if orq-lite decides which server and
// project a run targets — a hand-passed --attach would point the CLI somewhere
// the minted session does not exist.
func TestOpenCodeRejectsAttachInExtraArgs(t *testing.T) {
	for _, arg := range []string{"--attach", "--attach=http://x", "--dir", "--dir=/tmp"} {
		if err := (&OpenCode{}).ValidateExtraArgs([]string{arg}); err == nil {
			t.Errorf("extra_args accepted %q, which attach mode owns", arg)
		}
	}
	// Flags the adapter genuinely does not control must still pass.
	if err := (&OpenCode{}).ValidateExtraArgs([]string{"--thinking"}); err != nil {
		t.Errorf("extra_args rejected an unowned flag: %v", err)
	}
}

// TestOpenCodeParseLineAbort covers the finding that motivates a distinct
// event: an aborted run and a successful one both exit 0, so the JSON error
// name is the only thing separating "a human cancelled this" from "the agent
// finished and wrote nothing".
func TestOpenCodeParseLineAbort(t *testing.T) {
	line := `{"type":"error","timestamp":1,"sessionID":"ses_x","error":{"name":"MessageAbortedError","data":{"message":"Aborted"}}}`
	events := (&OpenCode{}).ParseLine(line)

	var aborted, errored bool
	for _, ev := range events {
		switch ev.Type {
		case EventAborted:
			aborted = true
			if ev.Result != "Aborted" {
				t.Errorf("aborted event result = %q, want %q", ev.Result, "Aborted")
			}
		case EventError:
			errored = true
		}
	}
	if !aborted {
		t.Fatalf("MessageAbortedError did not produce an aborted event: %+v", events)
	}
	if errored {
		t.Errorf("abort should not also surface as a generic error: %+v", events)
	}
}

// A genuine provider failure must stay an error — otherwise marking aborts
// terminal would silently make every failure terminal too.
func TestOpenCodeParseLineOrdinaryErrorIsNotAbort(t *testing.T) {
	line := `{"type":"error","timestamp":1,"sessionID":"ses_x","error":{"name":"ProviderAuthError","data":{"message":"bad key"}}}`
	events := (&OpenCode{}).ParseLine(line)

	for _, ev := range events {
		if ev.Type == EventAborted {
			t.Fatalf("ordinary provider error misread as an abort: %+v", events)
		}
	}
	found := false
	for _, ev := range events {
		if ev.Type == EventError && ev.Result == "bad key" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ordinary provider error did not surface: %+v", events)
	}
}
