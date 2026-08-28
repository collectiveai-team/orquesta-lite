package providers

import (
	"context"
	"encoding/json"
	"errors"
)

// OpenCode drives the opencode CLI (`opencode run`) in JSON streaming mode.
//
// opencode emits JSONL events, each with a top-level type, timestamp, and
// sessionID, plus either a `part` (step_start, text, tool_use, step_finish) or
// an `error`. Assistant answers arrive as `text` parts keyed by part.id; a part
// may be re-emitted cumulatively, so the provider tracks the latest text per
// part.id (in encounter order) and emits the reconstructed full answer as the
// EventResult, relying on the runner's last-write-wins FinalText handling.
//
// Instances are created fresh per agent run (see runner.buildLaunch), making
// the per-run text and session-emission state safe.
type OpenCode struct {
	sessionEmitted bool
	partOrder      []string
	partText       map[string]string
}

func init() {
	registerProvider("opencode", func() Provider { return &OpenCode{} })
}

func (*OpenCode) Name() string { return "opencode" }

func (*OpenCode) CLIHelp() CLIHelp {
	return CLIHelp{Args: []string{"opencode", "run", "--help"}, Synopsis: "opencode run"}
}

func (*OpenCode) ValidateExtraArgs(args []string) error {
	return validateExtraArgs("opencode", args, []string{
		"--format", "--print-logs", "-m", "--model", "--variant", "--auto",
		"-s", "--session", "--fork", "-c", "--continue", "--command", "-i", "--interactive",
		// Owned by attach mode: the session tree only holds together if
		// orq-lite decides which server and project the run targets.
		"--attach", "--dir",
	})
}

func (*OpenCode) Build(_ context.Context, prompt string, opts Options) (Launch, error) {
	model := opts.Model
	if model == "" {
		model = "anthropic/claude-sonnet-4-6"
	}

	if opts.AttachURL != "" && opts.AttachDir == "" {
		return Launch{}, errors.New("opencode attach requires a directory: the server resolves paths on its own side")
	}

	args := []string{"opencode", "run", "--format", "json", "--print-logs=false"}

	if model != "" {
		args = append(args, "-m", model)
	}
	if opts.Effort != "" {
		args = append(args, "--variant", opts.Effort)
	}
	if opts.DangerouslySkipPerms {
		args = append(args, "--auto")
	}
	if opts.AttachURL != "" {
		args = append(args, "--attach", opts.AttachURL, "--dir", opts.AttachDir)
	}
	if opts.ResumeSessionID != "" {
		args = append(args, "-s", opts.ResumeSessionID)
		if opts.ForkSession {
			args = append(args, "--fork")
		}
	}
	args = append(args, opts.ExtraArgs...)

	// opencode run takes the message as the final positional argument (not
	// stdin). exec passes argv without a shell, so spaces/newlines are safe.
	args = append(args, prompt)

	return Launch{Args: args, Stdin: ""}, nil
}

func (o *OpenCode) ParseLine(line string) []Event {
	if line == "" || line[0] != '{' {
		return nil
	}

	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		return nil
	}

	var events []Event

	// Every line carries the top-level sessionID; emit it once so even a
	// failed run records the ses_… id.
	if !o.sessionEmitted {
		if id, ok := obj["sessionID"].(string); ok && id != "" {
			events = append(events, Event{Type: EventSessionID, SessionID: id})
			o.sessionEmitted = true
		}
	}

	switch obj["type"] {
	case "text":
		events = append(events, o.parseText(obj)...)
	case "tool_use":
		if ev, ok := o.parseToolUse(obj); ok {
			events = append(events, ev)
		}
	case "step_finish":
		if usage, ok := o.parseUsage(obj); ok {
			events = append(events, Event{Type: EventUsage, Usage: usage})
		}
	case "error":
		msg := o.parseError(obj)
		if openCodeAborted(obj) {
			if msg == "" {
				msg = "aborted"
			}
			events = append(events, Event{Type: EventAborted, Result: msg})
			break
		}
		if msg != "" {
			events = append(events, Event{Type: EventError, Result: msg})
		}
	}

	return events
}

func (o *OpenCode) parseText(obj map[string]any) []Event {
	part, _ := obj["part"].(map[string]any)
	text, _ := part["text"].(string)
	if text == "" {
		return nil
	}

	id, _ := part["id"].(string)
	if o.partText == nil {
		o.partText = map[string]string{}
	}
	if _, seen := o.partText[id]; !seen {
		o.partOrder = append(o.partOrder, id)
	}
	o.partText[id] = text

	return []Event{
		{Type: EventText, Text: text},
		{Type: EventResult, Result: o.joinedText()},
	}
}

func (o *OpenCode) joinedText() string {
	var b []byte
	for _, id := range o.partOrder {
		b = append(b, o.partText[id]...)
	}
	return string(b)
}

func (o *OpenCode) parseToolUse(obj map[string]any) (Event, bool) {
	part, _ := obj["part"].(map[string]any)
	name, _ := part["tool"].(string)
	if name == "" {
		return Event{}, false
	}

	// Tool arguments live under part.state.input.
	var input map[string]any
	if state, ok := part["state"].(map[string]any); ok {
		input, _ = state["input"].(map[string]any)
	}

	return Event{
		Type:     EventToolCall,
		ToolName: name,
		ToolArgs: openCodeToolArgs(name, input),
	}, true
}

// openCodeToolArgs reduces a tool's input object to a single argument string,
// preferring the tool's primary field and falling back to the JSON of the whole
// input. Mirrors geminiToolArgs / claudeToolArgs.
func openCodeToolArgs(name string, input map[string]any) string {
	fieldByTool := map[string]string{
		"bash":     "command",
		"edit":     "filePath",
		"write":    "filePath",
		"read":     "filePath",
		"glob":     "pattern",
		"grep":     "pattern",
		"list":     "path",
		"webfetch": "url",
		"task":     "description",
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
		return ""
	}
	return string(raw)
}

func (o *OpenCode) parseUsage(obj map[string]any) (map[string]int, bool) {
	part, _ := obj["part"].(map[string]any)
	tokens, ok := part["tokens"].(map[string]any)
	if !ok {
		return nil, false
	}

	usage := map[string]int{}
	if v, ok := numberAsInt(tokens["input"]); ok {
		usage["input_tokens"] = v
	}
	if v, ok := numberAsInt(tokens["output"]); ok {
		usage["output_tokens"] = v
	}
	if v, ok := numberAsInt(tokens["reasoning"]); ok {
		usage["reasoning_tokens"] = v
	}
	if cache, ok := tokens["cache"].(map[string]any); ok {
		if v, ok := numberAsInt(cache["write"]); ok {
			usage["cache_write_tokens"] = v
		}
		if v, ok := numberAsInt(cache["read"]); ok {
			usage["cache_read_tokens"] = v
		}
	}

	if len(usage) == 0 {
		return nil, false
	}
	return usage, true
}

// openCodeAborted reports whether an error event is opencode's cancellation
// signal. The server emits {"type":"error","error":{"name":"MessageAbortedError"}}
// when a session is aborted (POST /session/{id}/abort, which is what the TUI's
// cancel does) and the CLI then exits 0 — the same exit code as success. The
// event name is therefore the only thing that distinguishes the two.
func openCodeAborted(obj map[string]any) bool {
	errObj, ok := obj["error"].(map[string]any)
	if !ok {
		return false
	}
	name, _ := errObj["name"].(string)
	return name == "MessageAbortedError"
}

func (o *OpenCode) parseError(obj map[string]any) string {
	errObj, ok := obj["error"].(map[string]any)
	if !ok {
		return ""
	}
	if data, ok := errObj["data"].(map[string]any); ok {
		if msg, ok := data["message"].(string); ok && msg != "" {
			return msg
		}
	}
	return extractProviderErrorMessage(errObj)
}
