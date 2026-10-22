package invoke

import (
	"math"
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
	if got.totalInput() != 220 || got.Output != 10 {
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
	if got.totalInput() != 220 || got.Output != 10 {
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

// TestCacheTokensAreNotPricedAsFreshInput uses the qa attempt of a run that
// tripped its 250 USD budget at 321.92 while agtop priced it at ~65: a
// claude-opus-5 invocation killed at its timeout, whose 27.9M input tokens
// were 99.99% cache reads. Folded into input_tokens they priced at 141.27;
// billed by class they cost 17.30.
func TestCacheTokensAreNotPricedAsFreshInput(t *testing.T) {
	res := &runner.Result{TimedOut: true, Events: []providers.Event{
		{Type: providers.EventPartialUsage, Usage: map[string]int{
			"input_tokens":                446,
			"cached_input_tokens":         27_731_249,
			"cache_creation_input_tokens": 162_632,
			"output_tokens":               72_065,
		}},
	}}
	got := usageTotals(res)
	if got.Input != 446 || got.CacheRead != 27_731_249 || got.CacheWrite != 162_632 {
		t.Fatalf("usageTotals = %+v, want the cache classes kept apart from fresh input", got)
	}
	// 446×5 + 27,731,249×0.50 + 162,632×10 + 72,065×25, per million.
	const want = 17.2957995
	if spend := runSpendUSD("claude-opus-5", res); math.Abs(spend-want) > 1e-6 {
		t.Fatalf("runSpendUSD = %v, want %v", spend, want)
	}
}
