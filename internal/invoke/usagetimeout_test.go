package invoke

import (
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/providers"
	"github.com/collectiveai-team/orquesta-lite/internal/runner"
)

func usageEvent(kind providers.EventType, in, out, cached int) providers.Event {
	return providers.Event{Type: kind, Usage: map[string]int{
		"input_tokens": in, "output_tokens": out, "cached_input_tokens": cached,
	}}
}

// TestUsageTotalsPrefersTheTerminalTotal keeps completed runs priced exactly as
// before: the provider's final result message is authoritative, and the per-turn
// numbers that led up to it must not be added on top of it.
func TestUsageTotalsPrefersTheTerminalTotal(t *testing.T) {
	res := &runner.Result{Events: []providers.Event{
		usageEvent(providers.EventPartialUsage, 10, 5, 100),
		usageEvent(providers.EventPartialUsage, 10, 5, 100),
		usageEvent(providers.EventUsage, 20, 10, 200),
	}}
	got := usageTotals(res)
	if got.Input != 220 || got.Output != 10 {
		t.Fatalf("usageTotals = %+v, want the terminal total alone", got)
	}
}

// TestUsageTotalsFallsBackToPartialsWhenKilled is the secondary defect in the
// report: an agent killed at its timeout never emits the terminal message, so
// 25 minutes of opus work priced at zero and maxCostUSD never saw it.
func TestUsageTotalsFallsBackToPartialsWhenKilled(t *testing.T) {
	res := &runner.Result{Events: []providers.Event{
		usageEvent(providers.EventPartialUsage, 10, 5, 100),
		usageEvent(providers.EventPartialUsage, 10, 5, 100),
	}}
	got := usageTotals(res)
	if got.Input != 220 || got.Output != 10 {
		t.Fatalf("usageTotals = %+v, want the partial turns summed", got)
	}
}

// TestKilledAttemptIsNotFree is the property that matters to the budget.
func TestKilledAttemptIsNotFree(t *testing.T) {
	res := &runner.Result{TimedOut: true, Events: []providers.Event{
		usageEvent(providers.EventPartialUsage, 50_000, 20_000, 0),
	}}
	if spend := runSpendUSD("claude-opus-5", res); spend <= 0 {
		t.Fatalf("a killed attempt priced at %v", spend)
	}
}
