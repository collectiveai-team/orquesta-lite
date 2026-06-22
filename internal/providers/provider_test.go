package providers

import (
	"context"
	"reflect"
	"testing"
)

type registeredTestProvider struct{}

func (registeredTestProvider) Name() string { return "test-provider" }

func (registeredTestProvider) Build(context.Context, string, Options) (Launch, error) {
	return Launch{}, nil
}

func (registeredTestProvider) ParseLine(string) []Event { return nil }

func TestNewUsesRegisteredProvider(t *testing.T) {
	registerProvider("test-provider", func() Provider { return registeredTestProvider{} })
	t.Cleanup(func() {
		delete(providerRegistry, "test-provider")
	})

	provider, err := New("test-provider")
	if err != nil {
		t.Fatalf("New(%q) error = %v", "test-provider", err)
	}
	if got := provider.Name(); got != "test-provider" {
		t.Fatalf("New(%q).Name() = %q, want %q", "test-provider", got, "test-provider")
	}
	if !IsKnown("test-provider") {
		t.Fatal(`IsKnown("test-provider") = false, want true`)
	}
}

func TestNewRegisteredProviders(t *testing.T) {
	tests := map[string]struct {
		wantName string
		wantType any
	}{
		"claude":   {wantName: "claude", wantType: Claude{}},
		"codex":    {wantName: "codex", wantType: Codex{}},
		"gemini":   {wantName: "gemini", wantType: &Gemini{}},
		"opencode": {wantName: "opencode", wantType: &OpenCode{}},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			provider, err := New(name)
			if err != nil {
				t.Fatalf("New(%q) error = %v", name, err)
			}
			if got := provider.Name(); got != test.wantName {
				t.Fatalf("New(%q).Name() = %q, want %q", name, got, test.wantName)
			}
			if got, want := reflect.TypeOf(provider), reflect.TypeOf(test.wantType); got != want {
				t.Fatalf("New(%q) = %v, want %v", name, got, want)
			}
		})
	}
}

func TestNewRejectsUnregisteredProvider(t *testing.T) {
	if _, err := New("ghost"); err == nil {
		t.Fatal(`New("ghost") error = nil, want unknown provider error`)
	}
}
