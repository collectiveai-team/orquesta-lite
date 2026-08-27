package providers

import (
	"context"
	"encoding/json"
)

type Codex struct{}

func init() {
	registerProvider("codex", func() Provider { return Codex{} })
}

func (Codex) Name() string { return "codex" }

func (Codex) CLIHelp() CLIHelp {
	return CLIHelp{Args: []string{"codex", "exec", "--help"}, Synopsis: "Usage: codex exec"}
}

func (Codex) ValidateExtraArgs(args []string) error {
	return validateExtraArgs("codex", args, []string{
		"--json", "--dangerously-bypass-approvals-and-sandbox", "-m", "--model", "-c", "--config",
	})
}

func (Codex) Build(_ context.Context, prompt string, opts Options) (Launch, error) {
	model := opts.Model
	if model == "" {
		model = "gpt-5"
	}

	args := []string{"codex", "exec"}
	if opts.ResumeSessionID != "" {
		if opts.ForkSession {
			args = append(args, "fork", opts.ResumeSessionID, "-")
		} else {
			args = append(args, "resume", opts.ResumeSessionID, "-")
		}
	}

	args = append(args, "--json")
	if opts.DangerouslySkipPerms {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	args = append(args, "-m", model)

	if opts.Effort != "" {
		args = append(args, "-c", `model_reasoning_effort="`+opts.Effort+`"`)
	}
	args = append(args, opts.ExtraArgs...)

	return Launch{Args: args, Stdin: prompt}, nil
}

func (Codex) ParseLine(line string) []Event {
	if line == "" || line[0] != '{' {
		return nil
	}

	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		return nil
	}

	switch obj["type"] {
	case "thread.started":
		if id, ok := obj["thread_id"].(string); ok {
			return []Event{{Type: EventSessionID, SessionID: id}}
		}
	case "item.completed":
		item, _ := obj["item"].(map[string]any)
		if item["type"] == "agent_message" {
			if text, ok := item["text"].(string); ok {
				return []Event{
					{Type: EventText, Text: text},
					{Type: EventResult, Result: text},
				}
			}
		}
	case "item.started":
		item, _ := obj["item"].(map[string]any)
		if item["type"] == "command_execution" {
			if command, ok := item["command"].(string); ok {
				return []Event{{Type: EventToolCall, ToolName: "Bash", ToolArgs: command}}
			}
		}
	case "turn.completed":
		if usage, ok := parseCodexUsage(obj["usage"]); ok {
			return []Event{{Type: EventUsage, Usage: usage}}
		}
	case "error":
		if msg := extractProviderErrorMessage(obj); msg != "" {
			return []Event{{Type: EventError, Result: msg}}
		}
	}

	return nil
}

func parseCodexUsage(raw any) (map[string]int, bool) {
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}

	input, ok1 := numberAsInt(obj["input_tokens"])
	cached, ok2 := numberAsInt(obj["cached_input_tokens"])
	output, ok3 := numberAsInt(obj["output_tokens"])
	if !ok1 || !ok2 || !ok3 {
		return nil, false
	}

	return map[string]int{
		"input_tokens":        input - cached,
		"cached_input_tokens": cached,
		"output_tokens":       output,
	}, true
}
