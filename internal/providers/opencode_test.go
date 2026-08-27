package providers

import (
	"context"
	"reflect"
	"testing"
)

func TestOpenCodeBuildFreshResumeAndFork(t *testing.T) {
	fresh, err := (&OpenCode{}).Build(context.Background(), "do the thing", Options{})
	if err != nil {
		t.Fatal(err)
	}
	wantFresh := []string{
		"opencode", "run", "--format", "json", "--print-logs=false",
		"-m", "anthropic/claude-sonnet-4-6",
		"do the thing",
	}
	if !reflect.DeepEqual(fresh.Args, wantFresh) {
		t.Fatalf("fresh args = %v, want %v", fresh.Args, wantFresh)
	}
	if fresh.Stdin != "" {
		t.Fatalf("fresh stdin = %q, want empty", fresh.Stdin)
	}

	// model + effort + skip-perms.
	full, err := (&OpenCode{}).Build(context.Background(), "prompt", Options{
		Model:                "openai/gpt-5.4",
		Effort:               "high",
		DangerouslySkipPerms: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantFull := []string{
		"opencode", "run", "--format", "json", "--print-logs=false",
		"-m", "openai/gpt-5.4",
		"--variant", "high",
		"--auto",
		"prompt",
	}
	if !reflect.DeepEqual(full.Args, wantFull) {
		t.Fatalf("full args = %v, want %v", full.Args, wantFull)
	}
	for _, arg := range full.Args {
		if arg == "--dangerously-skip-permissions" {
			t.Fatalf("OpenCode emitted unsupported flag in %v", full.Args)
		}
	}

	extra, err := (&OpenCode{}).Build(context.Background(), "prompt", Options{ExtraArgs: []string{"--thinking"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := extra.Args[len(extra.Args)-2:], []string{"--thinking", "prompt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("extra args suffix = %v, want %v", got, want)
	}

	resume, err := (&OpenCode{}).Build(context.Background(), "prompt", Options{ResumeSessionID: "ses_abc"})
	if err != nil {
		t.Fatal(err)
	}
	wantResume := []string{
		"opencode", "run", "--format", "json", "--print-logs=false",
		"-m", "anthropic/claude-sonnet-4-6",
		"-s", "ses_abc",
		"prompt",
	}
	if !reflect.DeepEqual(resume.Args, wantResume) {
		t.Fatalf("resume args = %v, want %v", resume.Args, wantResume)
	}

	fork, err := (&OpenCode{}).Build(context.Background(), "prompt", Options{ResumeSessionID: "ses_abc", ForkSession: true})
	if err != nil {
		t.Fatal(err)
	}
	wantFork := []string{
		"opencode", "run", "--format", "json", "--print-logs=false",
		"-m", "anthropic/claude-sonnet-4-6",
		"-s", "ses_abc", "--fork",
		"prompt",
	}
	if !reflect.DeepEqual(fork.Args, wantFork) {
		t.Fatalf("fork args = %v, want %v", fork.Args, wantFork)
	}

	// --fork must not appear without a resume session id.
	noResumeFork, err := (&OpenCode{}).Build(context.Background(), "prompt", Options{ForkSession: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range noResumeFork.Args {
		if a == "--fork" || a == "-s" {
			t.Fatalf("fork without resume produced %v", noResumeFork.Args)
		}
	}
}

// Captured verbatim from `opencode run --format json -m openai/gpt-5.4-fast
// "reply with the single word: pong"` (opencode 1.17.8). See
// .scratch/opencode-provider/issues/01-opencode-provider-adapter.md.
const (
	openCodePongStepStart  = `{"type":"step_start","timestamp":1782103746471,"sessionID":"ses_11256e63bffeghiqNG4CigvD9G","part":{"id":"prt_eeda927a4001D06xxT3q31I2Zl","messageID":"msg_eeda920a6001ji7EqC812YS4vU","sessionID":"ses_11256e63bffeghiqNG4CigvD9G","snapshot":"600710fc572026801953ceda1fd0727fec9353d6","type":"step-start"}}`
	openCodePongText       = `{"type":"text","timestamp":1782103748103,"sessionID":"ses_11256e63bffeghiqNG4CigvD9G","part":{"id":"prt_eeda92dc7001fvJZk2BB8kOf9I","messageID":"msg_eeda920a6001ji7EqC812YS4vU","sessionID":"ses_11256e63bffeghiqNG4CigvD9G","type":"text","text":"pong","time":{"start":1782103748039,"end":1782103748100},"metadata":{"openai":{"itemId":"msg_017e44c90bfdbdda016a38bec3f3d4819181d7e390db9320ac","phase":"final_answer"}}}}`
	openCodePongStepFinish = `{"type":"step_finish","timestamp":1782103748300,"sessionID":"ses_11256e63bffeghiqNG4CigvD9G","part":{"id":"prt_eeda92ebd001el3lmt3IwwcPru","reason":"stop","snapshot":"600710fc572026801953ceda1fd0727fec9353d6","messageID":"msg_eeda920a6001ji7EqC812YS4vU","sessionID":"ses_11256e63bffeghiqNG4CigvD9G","type":"step-finish","tokens":{"total":15722,"input":15702,"output":7,"reasoning":13,"cache":{"write":0,"read":0}},"cost":0}}`

	openCodeErrUnknown       = `{"type":"error","timestamp":1782103701421,"sessionID":"ses_11257908dffemwojbuz95SnPXb","error":{"name":"UnknownError","data":{"message":"Unexpected server error. Check server logs for details.","ref":"err_211b58fd"}}}`
	openCodeErrModelNotFound = `{"type":"error","timestamp":1782103701684,"sessionID":"ses_11257908dffemwojbuz95SnPXb","error":{"name":"UnknownError","data":{"message":"Model not found: openai/gpt-5.2. Did you mean: gpt-5.3-codex-spark, gpt-5.4, gpt-5.4-fast?"}}}`

	// Captured verbatim from `opencode run --format json -m anthropic/claude-sonnet-4-6
	// --dangerously-skip-permissions "Use the bash tool to run exactly: echo
	// hello-from-opencode. Then tell me the output."` (opencode 1.17.8).
	openCodeToolUse = `{"type":"tool_use","timestamp":1782106126676,"sessionID":"ses_1123298e4ffeD7sdss6UkKxKlh","part":{"type":"tool","tool":"bash","callID":"toolu_01WNzibsfnmkufau4mrXrX91","state":{"status":"completed","input":{"command":"echo hello-from-opencode","description":"Run echo command as requested"},"output":"hello-from-opencode\n","metadata":{"output":"hello-from-opencode\n","exit":0,"description":"Run echo command as requested","truncated":false},"title":"Run echo command as requested","time":{"start":1782106126665,"end":1782106126672}},"metadata":{"anthropic":{"caller":{"type":"direct"}}},"id":"prt_eedcd7746001u74W17HbSd1f5D","sessionID":"ses_1123298e4ffeD7sdss6UkKxKlh","messageID":"msg_eedcd6e82001PaGlzLip9Pw061"}}`
)

func TestOpenCodeParseLineSuccess(t *testing.T) {
	p := &OpenCode{}

	// step_start: session id emitted once.
	events := p.ParseLine(openCodePongStepStart)
	if len(events) != 1 || events[0].Type != EventSessionID || events[0].SessionID != "ses_11256e63bffeghiqNG4CigvD9G" {
		t.Fatalf("step_start events = %#v", events)
	}

	// text: EventText + EventResult, both carrying "pong".
	events = p.ParseLine(openCodePongText)
	if len(events) != 2 {
		t.Fatalf("text events = %#v", events)
	}
	if events[0].Type != EventText || events[0].Text != "pong" {
		t.Fatalf("text event = %#v", events[0])
	}
	if events[1].Type != EventResult || events[1].Result != "pong" {
		t.Fatalf("result event = %#v", events[1])
	}

	// step_finish: usage from part.tokens.
	events = p.ParseLine(openCodePongStepFinish)
	if len(events) != 1 || events[0].Type != EventUsage {
		t.Fatalf("step_finish events = %#v", events)
	}
	usage := events[0].Usage
	if usage["input_tokens"] != 15702 || usage["output_tokens"] != 7 || usage["reasoning_tokens"] != 13 {
		t.Fatalf("usage = %#v", usage)
	}
	if usage["cache_write_tokens"] != 0 || usage["cache_read_tokens"] != 0 {
		t.Fatalf("cache usage = %#v", usage)
	}
}

func TestOpenCodeParseLineSessionEmittedOnce(t *testing.T) {
	p := &OpenCode{}
	if got := p.ParseLine(openCodePongStepStart); len(got) != 1 || got[0].Type != EventSessionID {
		t.Fatalf("first line = %#v", got)
	}
	// A subsequent line with the same sessionID must not re-emit it.
	for _, ev := range p.ParseLine(openCodePongText) {
		if ev.Type == EventSessionID {
			t.Fatalf("session id re-emitted on text line")
		}
	}
}

func TestOpenCodeParseLineError(t *testing.T) {
	p := &OpenCode{}

	// First error line also carries the session id (emit once).
	events := p.ParseLine(openCodeErrUnknown)
	if len(events) != 2 {
		t.Fatalf("first error events = %#v", events)
	}
	if events[0].Type != EventSessionID || events[0].SessionID != "ses_11257908dffemwojbuz95SnPXb" {
		t.Fatalf("session event = %#v", events[0])
	}
	if events[1].Type != EventError || events[1].Result != "Unexpected server error. Check server logs for details." {
		t.Fatalf("error event = %#v", events[1])
	}

	// Second error line: the meaningful model-not-found message.
	events = p.ParseLine(openCodeErrModelNotFound)
	if len(events) != 1 || events[0].Type != EventError {
		t.Fatalf("model-not-found events = %#v", events)
	}
	want := "Model not found: openai/gpt-5.2. Did you mean: gpt-5.3-codex-spark, gpt-5.4, gpt-5.4-fast?"
	if events[0].Result != want {
		t.Fatalf("error message = %q, want %q", events[0].Result, want)
	}
}

func TestOpenCodeParseLineToolCall(t *testing.T) {
	p := &OpenCode{}

	events := p.ParseLine(openCodeToolUse)
	// First line also carries the session id (emit once), then the tool call.
	if len(events) != 2 {
		t.Fatalf("tool_use events = %#v", events)
	}
	if events[0].Type != EventSessionID || events[0].SessionID != "ses_1123298e4ffeD7sdss6UkKxKlh" {
		t.Fatalf("session event = %#v", events[0])
	}
	tool := events[1]
	if tool.Type != EventToolCall || tool.ToolName != "bash" || tool.ToolArgs != "echo hello-from-opencode" {
		t.Fatalf("tool event = %#v", tool)
	}
}

func TestOpenCodeParseLineIgnoresNonJSON(t *testing.T) {
	p := &OpenCode{}
	for _, line := range []string{"", "opencode v1.17.8", "  ", "not json {"} {
		if got := p.ParseLine(line); got != nil {
			t.Fatalf("ParseLine(%q) = %#v, want nil", line, got)
		}
	}
}
