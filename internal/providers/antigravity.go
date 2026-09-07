package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Antigravity drives the Antigravity CLI in headless print mode. The provider
// is named "agy" after the executable, not after the product: doctor and the
// run preflight both resolve a provider binary by its name, so the two must
// match (see TestProviderNamesMatchTheirExecutable).
//
// Stream-json emits JSONL envelopes keyed by "event" with the payload nested
// under a field of the same name: init (conversation_id, init.model),
// step_update (step_type, state, text_delta, tool_name, tool_info, usage) and
// result (status, response, error, usage). Two shapes differ from the sibling
// providers and drive the code below: the prompt cannot be piped, and the
// final result carries the whole response text, so no delta accumulation is
// needed and the provider holds no per-run state.
type Antigravity struct{}

// antigravityPrintTimeout disables agy's own print-mode deadline, which
// defaults to 5m — far below the 900-2400s role timeouts a flow uses. The
// runner already kills the subprocess at its own deadline, so the invocation
// keeps a single clock instead of racing two.
const antigravityPrintTimeout = "--print-timeout=24h"

func init() {
	registerProvider("agy", func() Provider { return Antigravity{} })
}

func (Antigravity) Name() string { return "agy" }

func (Antigravity) CLIHelp() CLIHelp {
	return CLIHelp{Args: []string{"agy", "--help"}, Synopsis: "Usage of agy:"}
}

func (Antigravity) ValidateExtraArgs(args []string) error {
	return validateExtraArgs("agy", args, []string{
		"--print", "--prompt", "--prompt-interactive", "-p", "-i",
		"--output-format", "--input-format", "--model", "--effort",
		"--print-timeout", "--dangerously-skip-permissions", "--sandbox",
		"--conversation", "--continue", "-c",
	})
}

func (Antigravity) Build(_ context.Context, prompt string, opts Options) (Launch, error) {
	model := opts.Model
	if model == "" {
		model = "gemini-3.8-flash-medium"
	}

	// An agy model id may carry its reasoning effort as a suffix, and the CLI
	// rejects a --effort that disagrees with one ("--model
	// gemini-3.8-flash-high conflicts with --effort=low"). Drop the redundant
	// flag, and refuse a contradiction here so it surfaces in doctor rather
	// than as a failed invocation mid-run.
	effort := opts.Effort
	if suffix := antigravityEffortSuffix(model); suffix != "" {
		if effort != "" && effort != suffix {
			return Launch{}, fmt.Errorf(
				"model %q already selects effort %q, which conflicts with effort %q: drop the effort or use model %q",
				model, suffix, effort, strings.TrimSuffix(model, "-"+suffix))
		}
		effort = ""
	}

	// agy print mode takes the prompt as a flag value and ignores stdin. It
	// stays one argv element: as two, a prompt beginning with "--" reads as an
	// emitted flag to doctor's CLI drift check.
	args := []string{
		"agy",
		"--print=" + prompt,
		"--output-format", "stream-json",
		"--model", model,
		antigravityPrintTimeout,
	}

	if effort != "" {
		args = append(args, "--effort", effort)
	}
	if opts.DangerouslySkipPerms {
		args = append(args, "--dangerously-skip-permissions")
	}
	if opts.SafeMode {
		args = append(args, "--sandbox")
	}
	if opts.ResumeSessionID != "" {
		args = append(args, "--conversation", opts.ResumeSessionID)
	}
	args = append(args, opts.ExtraArgs...)

	return Launch{Args: args}, nil
}

func (Antigravity) ParseLine(line string) []Event {
	if line == "" || line[0] != '{' {
		return nil
	}

	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		return nil
	}

	name, _ := obj["event"].(string)
	body, _ := obj[name].(map[string]any)

	switch name {
	case "init":
		// The conversation id sits on the envelope, not in the payload.
		if id, ok := obj["conversation_id"].(string); ok && id != "" {
			return []Event{{Type: EventSessionID, SessionID: id}}
		}
	case "step_update":
		return antigravityStepEvents(body)
	case "result":
		return antigravityResultEvents(body)
	}

	return nil
}

func antigravityStepEvents(step map[string]any) []Event {
	switch stepType, _ := step["step_type"].(string); stepType {
	case "agent_response":
		if text, _ := step["text_delta"].(string); text != "" {
			return []Event{{Type: EventText, Text: text}}
		}
	case "tool":
		// Every tool step arrives twice, ACTIVE then DONE with its output
		// attached. Only the first is the call; emitting both would double
		// every command in the activity log.
		if state, _ := step["state"].(string); state != "ACTIVE" {
			return nil
		}
		name, _ := step["tool_name"].(string)
		info, _ := step["tool_info"].(map[string]any)
		params, _ := info["parameters"].(map[string]any)
		return []Event{{
			Type:     EventToolCall,
			ToolName: name,
			ToolArgs: antigravityToolArgs(name, params),
		}}
	}
	return nil
}

func antigravityResultEvents(result map[string]any) []Event {
	if status, _ := result["status"].(string); status == "ERROR" {
		msg := extractProviderErrorMessage(result)
		if msg == "" {
			msg = "agy run failed"
		}
		return []Event{{Type: EventError, Result: msg}}
	}

	events := []Event{}
	if usage, ok := antigravityUsage(result); ok {
		events = append(events, Event{Type: EventUsage, Usage: usage})
	}
	text, _ := result["response"].(string)
	return append(events, Event{Type: EventResult, Result: text})
}

// antigravityUsage maps agy's token counts onto the shared slots. Measured on
// a real run: input 5195 + output 472 == total 5667, with thinking 309 and
// cache_read 8128 reported alongside. So thinking_tokens is already inside
// output_tokens, and cache_read_tokens is outside input_tokens — adding the
// first or subtracting the second would misreport the ledger.
func antigravityUsage(result map[string]any) (map[string]int, bool) {
	raw, _ := result["usage"].(map[string]any)
	if len(raw) == 0 {
		return nil, false
	}

	usage := map[string]int{}
	copyTokenField(usage, raw, "input_tokens", "input_tokens")
	copyTokenField(usage, raw, "output_tokens", "output_tokens")
	copyTokenField(usage, raw, "cache_read_tokens", "cached_input_tokens")

	if len(usage) == 0 {
		return nil, false
	}
	return usage, true
}

func antigravityToolArgs(name string, params map[string]any) string {
	// Only tools whose parameter shape is verified get a field mapping; the
	// rest fall back to the raw parameters, which still reads in the log.
	if name == "run_command" {
		if val, ok := params["CommandLine"].(string); ok {
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

func antigravityEffortSuffix(model string) string {
	for _, effort := range []string{"low", "medium", "high"} {
		if strings.HasSuffix(model, "-"+effort) {
			return effort
		}
	}
	return ""
}
