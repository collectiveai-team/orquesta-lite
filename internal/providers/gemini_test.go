package providers

import (
	"context"
	"reflect"
	"testing"
)

func TestGeminiBuild(t *testing.T) {
	fresh, err := (&Gemini{}).Build(context.Background(), "prompt", Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gemini", "--output-format", "stream-json", "--model", "gemini-2.5-pro"}
	if !reflect.DeepEqual(fresh.Args, want) {
		t.Fatalf("args = %v, want %v", fresh.Args, want)
	}
	if fresh.Stdin != "prompt" {
		t.Fatalf("stdin = %q", fresh.Stdin)
	}

	yolo, err := (&Gemini{}).Build(context.Background(), "p", Options{Model: "gemini-3-pro", DangerouslySkipPerms: true})
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"gemini", "--output-format", "stream-json", "--model", "gemini-3-pro", "--yolo"}
	if !reflect.DeepEqual(yolo.Args, want) {
		t.Fatalf("yolo args = %v, want %v", yolo.Args, want)
	}

	resume, err := (&Gemini{}).Build(context.Background(), "p", Options{ResumeSessionID: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := resume.Args[len(resume.Args)-2:]; !reflect.DeepEqual(got, []string{"--resume", "sess-1"}) {
		t.Fatalf("resume suffix = %v", got)
	}
}

func TestGeminiParseLineEvents(t *testing.T) {
	p := &Gemini{}

	events := p.ParseLine(`{"type":"init","timestamp":"2026-06-11T00:00:00Z","session_id":"sess-1","model":"gemini-2.5-pro"}`)
	if len(events) != 1 || events[0].Type != EventSessionID || events[0].SessionID != "sess-1" {
		t.Fatalf("init events = %#v", events)
	}

	events = p.ParseLine(`{"type":"message","role":"user","content":"the prompt"}`)
	if len(events) != 0 {
		t.Fatalf("user message should be ignored, got %#v", events)
	}

	events = p.ParseLine(`{"type":"message","role":"assistant","content":"hello ","delta":true}`)
	if len(events) != 1 || events[0].Type != EventText || events[0].Text != "hello " {
		t.Fatalf("assistant delta events = %#v", events)
	}
	p.ParseLine(`{"type":"message","role":"assistant","content":"world","delta":true}`)

	events = p.ParseLine(`{"type":"tool_use","tool_name":"run_shell_command","tool_id":"t1","parameters":{"command":"go test ./..."}}`)
	if len(events) != 1 || events[0].Type != EventToolCall || events[0].ToolArgs != "go test ./..." {
		t.Fatalf("tool events = %#v", events)
	}

	events = p.ParseLine(`{"type":"error","severity":"warning","message":"minor"}`)
	if len(events) != 0 {
		t.Fatalf("warnings should be ignored, got %#v", events)
	}

	events = p.ParseLine(`{"type":"result","status":"success","stats":{"total_tokens":10}}`)
	if len(events) != 1 || events[0].Type != EventResult || events[0].Result != "hello world" {
		t.Fatalf("result events = %#v", events)
	}
}

func TestGeminiParseLineErrors(t *testing.T) {
	p := &Gemini{}

	events := p.ParseLine(`{"type":"error","severity":"error","message":"quota exceeded"}`)
	if len(events) != 1 || events[0].Type != EventError || events[0].Result != "quota exceeded" {
		t.Fatalf("error events = %#v", events)
	}

	events = p.ParseLine(`{"type":"result","status":"error","error":{"type":"FatalError","message":"boom"}}`)
	if len(events) != 1 || events[0].Type != EventError || events[0].Result != "boom" {
		t.Fatalf("result error events = %#v", events)
	}
}
