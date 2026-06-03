package providers

import (
	"context"
	"encoding/json"
	"fmt"
)

type EventType string

const (
	EventText      EventType = "text"
	EventResult    EventType = "result"
	EventToolCall  EventType = "tool_call"
	EventSessionID EventType = "session_id"
	EventUsage     EventType = "usage"
	EventError     EventType = "error"
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
	ResumeSessionID      string
	ForkSession          bool
}

type Provider interface {
	Name() string
	Build(ctx context.Context, prompt string, opts Options) (Launch, error)
	ParseLine(line string) []Event
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
