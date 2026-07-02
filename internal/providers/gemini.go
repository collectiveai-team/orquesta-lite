package providers

import (
	"context"
	"encoding/json"
	"strings"
)

// Gemini drives the Google Gemini CLI in headless stream-json mode.
//
// Stream-json emits JSONL events of types: init (session_id, model),
// message (role, content, delta), tool_use (tool_name, parameters),
// tool_result, error (severity, message), result (status, stats, error).
// Assistant content arrives as delta chunks, so the provider accumulates
// them and emits the joined text as the final result. Instances are
// created fresh per agent run (see runner.buildLaunch), making this
// per-run state safe.
type Gemini struct {
	assistantText strings.Builder
}

func init() {
	registerProvider("gemini", func() Provider { return &Gemini{} })
}

func (*Gemini) Name() string { return "gemini" }

func (*Gemini) Build(_ context.Context, prompt string, opts Options) (Launch, error) {
	model := opts.Model
	if model == "" {
		model = "gemini-2.5-pro"
	}

	args := []string{
		"gemini",
		"--output-format", "stream-json",
		"--model", model,
	}

	if opts.DangerouslySkipPerms {
		args = append(args, "--yolo")
	}
	if opts.ResumeSessionID != "" {
		args = append(args, "--resume", opts.ResumeSessionID)
	}

	return Launch{Args: args, Stdin: prompt}, nil
}

func (g *Gemini) ParseLine(line string) []Event {
	if line == "" || line[0] != '{' {
		return nil
	}

	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		return nil
	}

	switch obj["type"] {
	case "init":
		if id, ok := obj["session_id"].(string); ok {
			return []Event{{Type: EventSessionID, SessionID: id}}
		}
	case "message":
		return g.parseMessage(obj)
	case "tool_use":
		name, _ := obj["tool_name"].(string)
		params, _ := obj["parameters"].(map[string]any)
		return []Event{{
			Type:     EventToolCall,
			ToolName: name,
			ToolArgs: geminiToolArgs(name, params),
		}}
	case "error":
		if severity, _ := obj["severity"].(string); severity == "warning" {
			return nil
		}
		if msg, ok := obj["message"].(string); ok && msg != "" {
			return []Event{{Type: EventError, Result: msg}}
		}
	case "result":
		return g.parseResult(obj)
	}

	return nil
}

func (g *Gemini) parseMessage(obj map[string]any) []Event {
	if role, _ := obj["role"].(string); role != "assistant" {
		return nil
	}
	text, _ := obj["content"].(string)
	if text == "" {
		return nil
	}
	g.assistantText.WriteString(text)
	return []Event{{Type: EventText, Text: text}}
}

func (g *Gemini) parseResult(obj map[string]any) []Event {
	if status, _ := obj["status"].(string); status == "error" {
		msg := extractProviderErrorMessage(obj)
		if msg == "" {
			msg = "gemini run failed"
		}
		return []Event{{Type: EventError, Result: msg}}
	}
	events := []Event{}
	if usage, ok := parseGeminiUsage(obj); ok {
		events = append(events, Event{Type: EventUsage, Usage: usage})
	}
	return append(events, Event{Type: EventResult, Result: g.assistantText.String()})
}

func parseGeminiUsage(obj map[string]any) (map[string]int, bool) {
	stats, _ := obj["stats"].(map[string]any)
	usageMeta, _ := obj["usage_metadata"].(map[string]any)
	if len(stats) == 0 && len(usageMeta) == 0 {
		return nil, false
	}

	usage := map[string]int{}
	copyTokenField(usage, stats, "input_tokens", "input_tokens")
	copyTokenField(usage, stats, "prompt_tokens", "input_tokens")
	copyTokenField(usage, stats, "output_tokens", "output_tokens")
	copyTokenField(usage, stats, "completion_tokens", "output_tokens")
	copyTokenField(usage, usageMeta, "promptTokenCount", "input_tokens")
	copyTokenField(usage, usageMeta, "candidatesTokenCount", "output_tokens")

	if len(usage) == 0 {
		return nil, false
	}
	return usage, true
}

func geminiToolArgs(name string, params map[string]any) string {
	fieldByTool := map[string]string{
		"run_shell_command": "command",
		"Shell":             "command",
		"google_web_search": "query",
		"web_fetch":         "url",
	}
	if field := fieldByTool[name]; field != "" {
		if val, ok := params[field].(string); ok {
			return val
		}
	}
	if len(params) == 0 {
		return ""
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return ""
	}
	return string(raw)
}
