package providers

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestAntigravityBuild(t *testing.T) {
	fresh, err := Antigravity{}.Build(context.Background(), "prompt", Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"agy", "--print=prompt",
		"--output-format", "stream-json",
		"--model", "gemini-3.8-flash-medium",
		"--print-timeout=24h",
	}
	if !reflect.DeepEqual(fresh.Args, want) {
		t.Fatalf("args = %v, want %v", fresh.Args, want)
	}
	// agy print mode ignores stdin: the prompt only arrives through argv.
	if fresh.Stdin != "" {
		t.Fatalf("stdin = %q, want empty", fresh.Stdin)
	}

	full, err := Antigravity{}.Build(context.Background(), "p", Options{
		Model:                "gemini-3.8-flash-high",
		DangerouslySkipPerms: true,
		SafeMode:             true,
		ResumeSessionID:      "conv-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	want = []string{
		"agy", "--print=p",
		"--output-format", "stream-json",
		"--model", "gemini-3.8-flash-high",
		"--print-timeout=24h",
		"--dangerously-skip-permissions",
		"--sandbox",
		"--conversation", "conv-1",
	}
	if !reflect.DeepEqual(full.Args, want) {
		t.Fatalf("full args = %v, want %v", full.Args, want)
	}
}

// TestAntigravityBuildPromptStaysOneArgv pins the prompt into a single
// --print=<prompt> element. Passing it as a separate argv element makes
// doctor's emittedFlag read a prompt that starts with "--" as an emitted
// flag and report a CLI drift that does not exist.
func TestAntigravityBuildPromptStaysOneArgv(t *testing.T) {
	launch, err := Antigravity{}.Build(context.Background(), "--not-a-flag\nsecond line", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if launch.Args[1] != "--print=--not-a-flag\nsecond line" {
		t.Fatalf("prompt argv = %q", launch.Args[1])
	}
	for _, arg := range launch.Args[2:] {
		if strings.Contains(arg, "not-a-flag") {
			t.Fatalf("prompt leaked into a second argv element: %v", launch.Args)
		}
	}
}

// TestAntigravityEffortMatchesModelSuffix covers the three-way interaction
// agy enforces: "--model gemini-3.8-flash-high --effort low" is a hard error
// from the CLI, and a bare "gemini-3.8-flash" is rejected without --effort.
func TestAntigravityEffortMatchesModelSuffix(t *testing.T) {
	suffixed, err := Antigravity{}.Build(context.Background(), "p", Options{Model: "gemini-3.8-flash-high"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(suffixed.Args, " "), "--effort") {
		t.Fatalf("suffixed model must not emit --effort: %v", suffixed.Args)
	}

	bare, err := Antigravity{}.Build(context.Background(), "p", Options{Model: "gemini-3.8-flash", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(bare.Args, " "); !strings.Contains(got, "--effort high") {
		t.Fatalf("bare model with effort must emit --effort: %v", bare.Args)
	}

	agreeing, err := Antigravity{}.Build(context.Background(), "p", Options{Model: "gemini-3.8-flash-high", Effort: "high"})
	if err != nil {
		t.Fatalf("effort agreeing with the suffix is not a conflict: %v", err)
	}
	if strings.Contains(strings.Join(agreeing.Args, " "), "--effort") {
		t.Fatalf("agreeing effort must stay off the argv: %v", agreeing.Args)
	}

	if _, err := (Antigravity{}).Build(context.Background(), "p", Options{Model: "gemini-3.8-flash-high", Effort: "low"}); err == nil {
		t.Fatal("contradicting effort and model suffix must fail the build")
	}
}

func TestAntigravityParseLineEvents(t *testing.T) {
	p := Antigravity{}

	events := p.ParseLine(`{"event":"init","conversation_id":"692d8b3c-41b8-4e14-844d-cb46e89029ae","init":{"model":"gemini-3.8-flash-low","cwd":"/tmp","permission_mode":"request-review"}}`)
	if len(events) != 1 || events[0].Type != EventSessionID || events[0].SessionID != "692d8b3c-41b8-4e14-844d-cb46e89029ae" {
		t.Fatalf("init events = %#v", events)
	}

	events = p.ParseLine(`{"event":"step_update","step_update":{"conversation_id":"c","step_index":0,"state":"DONE","step_type":"user_input"}}`)
	if len(events) != 0 {
		t.Fatalf("user_input should be ignored, got %#v", events)
	}

	events = p.ParseLine(`{"event":"step_update","step_update":{"conversation_id":"c","step_index":1,"state":"ACTIVE","step_type":"agent_response","text_delta":"OK"}}`)
	if len(events) != 1 || events[0].Type != EventText || events[0].Text != "OK" {
		t.Fatalf("agent_response events = %#v", events)
	}

	events = p.ParseLine(`{"event":"step_update","step_update":{"conversation_id":"c","step_index":2,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"echo hello-from-agy"}}}}`)
	if len(events) != 1 || events[0].Type != EventToolCall {
		t.Fatalf("tool events = %#v", events)
	}
	if events[0].ToolName != "run_command" || events[0].ToolArgs != "echo hello-from-agy" {
		t.Fatalf("tool call = %#v", events[0])
	}

	// The same tool step arrives again as DONE with its output attached.
	// Emitting it twice would double every command in the activity log.
	events = p.ParseLine(`{"event":"step_update","step_update":{"conversation_id":"c","step_index":2,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"echo hello-from-agy"},"output":"hello-from-agy\n"}}}`)
	if len(events) != 0 {
		t.Fatalf("DONE tool step should not repeat the call, got %#v", events)
	}

	events = p.ParseLine(`{"event":"result","result":{"conversation_id":"c","status":"SUCCESS","response":"OK\n","num_turns":1,"usage":{"input_tokens":13315,"output_tokens":1,"thinking_tokens":0,"cache_read_tokens":0,"total_tokens":13316}}}`)
	if len(events) != 2 || events[0].Type != EventUsage || events[1].Type != EventResult {
		t.Fatalf("result events = %#v", events)
	}
	if events[1].Result != "OK\n" {
		t.Fatalf("result text = %q", events[1].Result)
	}
}

// TestAntigravityParseLineUsage pins the token mapping against a measured run:
// input 5195 + output 472 == total 5667, so thinking_tokens (309) is already
// inside output_tokens and cache_read_tokens (8128) is outside input_tokens.
// Adding thinking to output double counts it; subtracting the cache read from
// input understates the bill.
func TestAntigravityParseLineUsage(t *testing.T) {
	events := Antigravity{}.ParseLine(`{"event":"result","result":{"status":"SUCCESS","response":"391","usage":{"input_tokens":5195,"output_tokens":472,"thinking_tokens":309,"cache_read_tokens":8128,"total_tokens":5667}}}`)
	if len(events) != 2 || events[0].Type != EventUsage {
		t.Fatalf("events = %#v", events)
	}
	usage := events[0].Usage
	if usage["input_tokens"] != 5195 {
		t.Fatalf("input_tokens = %d, want 5195", usage["input_tokens"])
	}
	if usage["output_tokens"] != 472 {
		t.Fatalf("output_tokens = %d, want 472 (thinking_tokens already included)", usage["output_tokens"])
	}
	if usage["cached_input_tokens"] != 8128 {
		t.Fatalf("cached_input_tokens = %d, want 8128", usage["cached_input_tokens"])
	}
}

func TestAntigravityParseLineErrors(t *testing.T) {
	p := Antigravity{}

	events := p.ParseLine(`{"event":"result","result":{"conversation_id":"","status":"ERROR","response":"","error":"invalid model selection (--model \"gemini-3.8-flash\" --effort \"\"): --model gemini-3.8-flash requires --effort (available: low, medium, high)","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
	if len(events) != 1 || events[0].Type != EventError {
		t.Fatalf("error events = %#v", events)
	}
	if !strings.Contains(events[0].Result, "requires --effort") {
		t.Fatalf("error text = %q", events[0].Result)
	}

	if events := p.ParseLine(`not json`); len(events) != 0 {
		t.Fatalf("non-JSON line = %#v", events)
	}
	if events := p.ParseLine(``); len(events) != 0 {
		t.Fatalf("empty line = %#v", events)
	}
}

func TestAntigravityCLIHelpSynopsis(t *testing.T) {
	help := Antigravity{}.CLIHelp()
	if !reflect.DeepEqual(help.Args, []string{"agy", "--help"}) {
		t.Fatalf("help args = %v", help.Args)
	}
	if help.Synopsis != "Usage of agy:" {
		t.Fatalf("synopsis = %q", help.Synopsis)
	}
}
