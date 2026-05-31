package providers

import (
	"context"
	"reflect"
	"testing"
)

func TestCodexBuildFreshResumeAndFork(t *testing.T) {
	fresh, err := (Codex{}).Build(context.Background(), "prompt", Options{Model: "gpt-test", Effort: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	wantFreshPrefix := []string{"codex", "exec"}
	if !reflect.DeepEqual(fresh.Args[:2], wantFreshPrefix) {
		t.Fatalf("fresh prefix = %v, want %v", fresh.Args[:2], wantFreshPrefix)
	}
	if fresh.Stdin != "prompt" {
		t.Fatalf("fresh stdin = %q", fresh.Stdin)
	}

	resume, err := (Codex{}).Build(context.Background(), "prompt", Options{ResumeSessionID: "sess"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resume.Args[:5], []string{"codex", "exec", "resume", "sess", "-"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resume prefix = %v, want %v", got, want)
	}

	fork, err := (Codex{}).Build(context.Background(), "prompt", Options{ResumeSessionID: "sess", ForkSession: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fork.Args[:5], []string{"codex", "exec", "fork", "sess", "-"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fork prefix = %v, want %v", got, want)
	}
}

func TestCodexParseLineEvents(t *testing.T) {
	p := Codex{}

	events := p.ParseLine(`{"type":"thread.started","thread_id":"thread-1"}`)
	if len(events) != 1 || events[0].Type != EventSessionID || events[0].SessionID != "thread-1" {
		t.Fatalf("session events = %#v", events)
	}

	events = p.ParseLine(`{"type":"item.started","item":{"type":"command_execution","command":"go test ./..."}}`)
	if len(events) != 1 || events[0].Type != EventToolCall || events[0].ToolArgs != "go test ./..." {
		t.Fatalf("tool events = %#v", events)
	}

	events = p.ParseLine(`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":30}}`)
	if len(events) != 1 || events[0].Type != EventUsage {
		t.Fatalf("usage events = %#v", events)
	}
	if got := events[0].Usage["input_tokens"]; got != 80 {
		t.Fatalf("input_tokens = %d, want 80", got)
	}
}
