package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type EventType string

const (
	EventText      EventType = "text"
	EventResult    EventType = "result"
	EventToolCall  EventType = "tool_call"
	EventSessionID EventType = "session_id"
	EventUsage     EventType = "usage"
	EventError     EventType = "error"
	// EventAborted marks a run cancelled out from under the CLI — in practice,
	// someone hitting abort on the session in the opencode TUI. It is separate
	// from EventError because the CLI exits 0 either way: without a distinct
	// signal, a deliberate cancellation is indistinguishable from a clean run
	// that happened to write no result, and the corrective-retry loop
	// immediately relaunches the very work the user just stopped.
	EventAborted EventType = "aborted"
)

type Event struct {
	Type      EventType      `json:"type"`
	Text      string         `json:"text,omitempty"`
	Result    string         `json:"result,omitempty"`
	ToolName  string         `json:"tool_name,omitempty"`
	ToolArgs  string         `json:"tool_args,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	Usage     map[string]int `json:"usage,omitempty"`
}

type Launch struct {
	Args  []string
	Stdin string
}

type Options struct {
	Model                string
	Effort               string
	DangerouslySkipPerms bool
	SafeMode             bool
	ResumeSessionID      string
	ForkSession          bool
	ExtraArgs            []string
	// AttachURL, when set, points the CLI at an already-running opencode server
	// instead of the private one it would otherwise start. AttachDir is then
	// mandatory: the server resolves paths on its own side, so the launching
	// process's working directory stops being enough to locate the project.
	AttachURL string
	AttachDir string
}

// CLIHelp describes the provider CLI help page that declares the flags emitted
// by Build. Doctor uses it to catch adapter/CLI version drift before a run.
type CLIHelp struct {
	Args     []string
	Synopsis string
}

type Provider interface {
	Name() string
	Build(ctx context.Context, prompt string, opts Options) (Launch, error)
	ParseLine(line string) []Event
	CLIHelp() CLIHelp
	ValidateExtraArgs(args []string) error
}

var providerRegistry = map[string]func() Provider{}

func registerProvider(name string, factory func() Provider) {
	if name == "" {
		panic("provider name cannot be empty")
	}
	if factory == nil {
		panic(fmt.Sprintf("provider %q factory cannot be nil", name))
	}
	if _, exists := providerRegistry[name]; exists {
		panic(fmt.Sprintf("provider %q already registered", name))
	}
	providerRegistry[name] = factory
}

func New(name string) (Provider, error) {
	factory, ok := providerRegistry[name]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", name)
	}
	return factory(), nil
}

func IsKnown(name string) bool {
	_, ok := providerRegistry[name]
	return ok
}

func validateExtraArgs(provider string, args, controlled []string) error {
	for _, arg := range args {
		flag := arg
		if i := strings.IndexByte(flag, '='); i >= 0 {
			flag = flag[:i]
		}
		for _, reserved := range controlled {
			if flag == reserved || (len(reserved) == 2 && reserved[0] == '-' && strings.HasPrefix(flag, reserved)) {
				return fmt.Errorf("provider %q controls flag %q; remove it from extra_args", provider, reserved)
			}
		}
	}
	return nil
}

func insertAfter(args []string, after string, values ...string) []string {
	out := make([]string, 0, len(args)+len(values))
	for _, arg := range args {
		out = append(out, arg)
		if arg == after {
			out = append(out, values...)
		}
	}
	return out
}

func numberAsInt(raw any) (int, bool) {
	switch v := raw.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	default:
		return 0, false
	}
}

func copyTokenField(dst map[string]int, src map[string]any, from, to string) {
	if v, ok := numberAsInt(src[from]); ok {
		dst[to] += v
	}
}

func extractProviderErrorMessage(obj map[string]any) string {
	for _, key := range []string{"message", "error", "result"} {
		switch v := obj[key].(type) {
		case string:
			return v
		case map[string]any:
			if msg, ok := v["message"].(string); ok {
				return msg
			}
			raw, err := json.Marshal(v)
			if err != nil {
				return fmt.Sprint(v)
			}
			return string(raw)
		}
	}
	return ""
}
