package providers

import (
	"context"
	"slices"
	"testing"
)

func TestClaudeBuildUsesStdinPrompt(t *testing.T) {
	launch, err := (Claude{}).Build(context.Background(), "large prompt", Options{
		Model:                "claude-test",
		DangerouslySkipPerms: true,
		SafeMode:             true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if launch.Stdin != "large prompt" {
		t.Fatalf("Stdin = %q, want prompt", launch.Stdin)
	}
	if !slices.Contains(launch.Args, "-p") || launch.Args[len(launch.Args)-1] != "-" {
		t.Fatalf("Args = %v, want -p -", launch.Args)
	}
	if !slices.Contains(launch.Args, "--dangerously-skip-permissions") {
		t.Fatalf("Args = %v, want dangerous permissions flag", launch.Args)
	}
	if !slices.Contains(launch.Args, "--safe-mode") {
		t.Fatalf("Args = %v, want safe mode flag", launch.Args)
	}
}

func TestClaudeParseLineSessionAndResult(t *testing.T) {
	p := Claude{}

	events := p.ParseLine(`{"type":"system","subtype":"init","session_id":"abc123"}`)
	if len(events) != 1 || events[0].Type != EventSessionID || events[0].SessionID != "abc123" {
		t.Fatalf("session events = %#v", events)
	}

	events = p.ParseLine(`{"type":"result","result":"done"}`)
	if len(events) != 1 || events[0].Type != EventResult || events[0].Result != "done" {
		t.Fatalf("result events = %#v", events)
	}
}

func TestClaudeParseLineErrorResult(t *testing.T) {
	events := (Claude{}).ParseLine(`{"type":"result","is_error":true,"api_error_status":429,"result":"rate limit exceeded"}`)
	if len(events) != 1 || events[0].Type != EventError || events[0].Result != "rate limit exceeded" {
		t.Fatalf("error events = %#v", events)
	}
}

func TestClaudeParseLineUsage(t *testing.T) {
	events := (Claude{}).ParseLine(`{"type":"result","subtype":"success","result":"done","usage":{"input_tokens":120,"cache_creation_input_tokens":30,"cache_read_input_tokens":40,"output_tokens":50}}`)
	if len(events) != 2 || events[0].Type != EventUsage || events[1].Type != EventResult {
		t.Fatalf("events = %#v", events)
	}
	usage := events[0].Usage
	if usage["input_tokens"] != 120 || usage["cache_creation_input_tokens"] != 30 || usage["cached_input_tokens"] != 40 || usage["output_tokens"] != 50 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestClaudeParseLineAssistantBlocks(t *testing.T) {
	events := (Claude{}).ParseLine(`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"},{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}`)
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2: %#v", len(events), events)
	}
	if events[0].Type != EventText || events[0].Text != "hello" {
		t.Fatalf("text event = %#v", events[0])
	}
	if events[1].Type != EventToolCall || events[1].ToolName != "Bash" || events[1].ToolArgs != "go test ./..." {
		t.Fatalf("tool event = %#v", events[1])
	}
}

// TestClaudeAssistantMessageEmitsPartialUsage captures the per-turn usage the
// API reports on each assistant message. Usage used to be read only from the
// terminal `result` message, which a process killed at its timeout never emits —
// so 25 minutes of opus work priced at exactly zero and the run's cost budget
// never saw it.
func TestClaudeAssistantMessageEmitsPartialUsage(t *testing.T) {
	events := (Claude{}).ParseLine(`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}],"usage":{"input_tokens":10,"cache_read_input_tokens":900,"output_tokens":25}}}`)
	var partial map[string]int
	for _, ev := range events {
		if ev.Type == EventPartialUsage {
			partial = ev.Usage
		}
	}
	if partial == nil {
		t.Fatalf("no partial usage event: %#v", events)
	}
	if partial["input_tokens"] != 10 || partial["cached_input_tokens"] != 900 || partial["output_tokens"] != 25 {
		t.Fatalf("partial usage = %#v", partial)
	}
}

// TestClaudeAssistantMessageWithoutUsageEmitsNone keeps the stream quiet for the
// messages that carry no usage at all.
func TestClaudeAssistantMessageWithoutUsageEmitsNone(t *testing.T) {
	events := (Claude{}).ParseLine(`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`)
	for _, ev := range events {
		if ev.Type == EventPartialUsage {
			t.Fatalf("unexpected partial usage: %#v", ev)
		}
	}
}
