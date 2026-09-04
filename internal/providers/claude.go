package providers

import (
	"context"
	"encoding/json"
	"fmt"
)

type Claude struct{}

func init() {
	registerProvider("claude", func() Provider { return Claude{} })
}

func (Claude) Name() string { return "claude" }

func (Claude) CLIHelp() CLIHelp {
	return CLIHelp{Args: []string{"claude", "--help"}, Synopsis: "Usage: claude"}
}

func (Claude) ValidateExtraArgs(args []string) error {
	return validateExtraArgs("claude", args, []string{
		"--print", "-p", "--verbose", "--output-format", "--model", "--effort",
		"--dangerously-skip-permissions", "--safe-mode", "--resume", "-r", "--fork-session",
	})
}

func (Claude) Build(_ context.Context, prompt string, opts Options) (Launch, error) {
	model := opts.Model
	if model == "" {
		model = "claude-sonnet-5"
	}

	args := []string{
		"claude",
		"--print",
		"--verbose",
		"--output-format", "stream-json",
		"--model", model,
		"-p", "-",
	}

	if opts.DangerouslySkipPerms {
		args = insertAfter(args, "--verbose", "--dangerously-skip-permissions")
	}
	if opts.SafeMode {
		args = insertAfter(args, "--verbose", "--safe-mode")
	}
	if opts.Effort != "" {
		args = append(args[:len(args)-2], "--effort", opts.Effort, "-p", "-")
	}
	if opts.ResumeSessionID != "" {
		args = append(args[:len(args)-2], "--resume", opts.ResumeSessionID, "-p", "-")
		if opts.ForkSession {
			args = append(args[:len(args)-2], "--fork-session", "-p", "-")
		}
	}
	if len(opts.ExtraArgs) > 0 {
		tail := append([]string(nil), opts.ExtraArgs...)
		tail = append(tail, "-p", "-")
		args = append(args[:len(args)-2], tail...)
	}

	return Launch{Args: args, Stdin: prompt}, nil
}

func (Claude) ParseLine(line string) []Event {
	if line == "" || line[0] != '{' {
		return nil
	}

	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		return nil
	}

	if obj["type"] == "system" && obj["subtype"] == "init" {
		if id, ok := obj["session_id"].(string); ok {
			return []Event{{Type: EventSessionID, SessionID: id}}
		}
	}

	if obj["type"] == "result" {
		events := []Event{}
		if usage, ok := parseClaudeUsage(obj["usage"]); ok {
			events = append(events, Event{Type: EventUsage, Usage: usage})
		}
		if result, ok := obj["result"].(string); ok {
			if isClaudeErrorResult(obj) {
				return append(events, Event{Type: EventError, Result: result})
			}
			return append(events, Event{Type: EventResult, Result: result})
		}
		if len(events) > 0 {
			return events
		}
	}

	if obj["type"] == "error" {
		if msg := extractProviderErrorMessage(obj); msg != "" {
			return []Event{{Type: EventError, Result: msg}}
		}
	}

	if obj["type"] == "assistant" {
		return parseClaudeAssistantMessage(obj)
	}

	return nil
}

func parseClaudeUsage(raw any) (map[string]int, bool) {
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}

	usage := map[string]int{}
	copyTokenField(usage, obj, "input_tokens", "input_tokens")
	copyTokenField(usage, obj, "output_tokens", "output_tokens")
	copyTokenField(usage, obj, "cache_read_input_tokens", "cached_input_tokens")
	copyTokenField(usage, obj, "cache_creation_input_tokens", "cache_creation_input_tokens")

	if len(usage) == 0 {
		return nil, false
	}
	return usage, true
}

func isClaudeErrorResult(obj map[string]any) bool {
	if isErr, ok := obj["is_error"].(bool); ok && isErr {
		return true
	}
	status, ok := obj["api_error_status"]
	return ok && status != nil
}

func parseClaudeAssistantMessage(obj map[string]any) []Event {
	message, _ := obj["message"].(map[string]any)
	content, _ := message["content"].([]any)
	if len(content) == 0 {
		return nil
	}

	events := make([]Event, 0, len(content))
	for _, raw := range content {
		block, _ := raw.(map[string]any)
		switch block["type"] {
		case "text":
			if text, ok := block["text"].(string); ok && text != "" {
				events = append(events, Event{Type: EventText, Text: text})
			}
		case "tool_use":
			name, _ := block["name"].(string)
			input, _ := block["input"].(map[string]any)
			events = append(events, Event{
				Type:     EventToolCall,
				ToolName: name,
				ToolArgs: claudeToolArgs(name, input),
			})
		}
	}
	return events
}

func claudeToolArgs(name string, input map[string]any) string {
	fieldByTool := map[string]string{
		"Bash":      "command",
		"WebSearch": "query",
		"WebFetch":  "url",
		"Agent":     "description",
	}
	if field := fieldByTool[name]; field != "" {
		if val, ok := input[field].(string); ok {
			return val
		}
	}
	if len(input) == 0 {
		return ""
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return fmt.Sprint(input)
	}
	return string(raw)
}
