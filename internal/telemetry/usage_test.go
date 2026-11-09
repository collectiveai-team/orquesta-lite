package telemetry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWeightedCacheAndRegressionCompatibility(t *testing.T) {
	a := NewCall("a", "task", "coder", "codex", "gpt-5", "stable", "provider_turn", map[string]int{"input_tokens": 1000, "cached_input_tokens": 3000, "output_tokens": 10})
	b := NewCall("b", "task", "coder", "codex", "gpt-5", "stable", "provider_turn", map[string]int{"input_tokens": 3000, "cached_input_tokens": 1000, "output_tokens": 10})
	c := NewCall("c", "task", "coder", "codex", "gpt-5", "different", "provider_turn", map[string]int{"input_tokens": 10, "cached_input_tokens": 0})
	r := Summarize([]Call{a, b, c, a})
	if r.Records != 3 || r.ReuseRegressionTokens != 2000 {
		t.Fatalf("wrong report: %+v", r)
	}
	if r.CacheHitRate == nil || *r.CacheHitRate < 0.49 || *r.CacheHitRate > 0.5 {
		t.Fatal("hit rate not weighted")
	}
	if r.ReuseRegressionEstimatedUSD == nil || *r.ReuseRegressionEstimatedUSD <= 0 {
		t.Fatal("regression estimate missing")
	}
}
func TestUnavailableIsNotMeasuredZero(t *testing.T) {
	unknown := NewCall("unknown", "task", "coder", "custom", "unknown", "", "unavailable", nil)
	zero := NewCall("zero", "task", "coder", "codex", "gpt-5", "stable", "provider_turn", map[string]int{"input_tokens": 0, "cached_input_tokens": 0, "output_tokens": 0})
	r := Summarize([]Call{unknown, zero})
	if r.MeasuredRecords != 1 || r.CacheHitRate != nil {
		t.Fatal("missing conflated with zero")
	}
	root := t.TempDir()
	if err := Append(root, []Call{unknown, zero}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "usage-report.json")); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalPricesRemainRecordedAndCacheEstimatesStayComparable(t *testing.T) {
	oldA := NewCall("old-a", "task", "coder", "codex", "gpt-5", "prefix", "provider_turn", map[string]int{"input_tokens": 1000, "cached_input_tokens": 4000})
	oldB := NewCall("old-b", "task", "coder", "codex", "gpt-5", "prefix", "provider_turn", map[string]int{"input_tokens": 3000, "cached_input_tokens": 2000})
	oldA.PricingVersion = "historical-rates"
	oldB.PricingVersion = "historical-rates"
	one, two := 1.0, 2.0
	oldA.EstimatedUSD = &one
	oldB.EstimatedUSD = &two
	current := NewCall("current", "task", "coder", "codex", "gpt-5", "prefix", "provider_turn", map[string]int{"input_tokens": 4000, "cached_input_tokens": 0})
	report := Summarize([]Call{oldA, oldB, current})
	if report.PricingVersion != "mixed" || len(report.PricingVersions) != 2 {
		t.Fatalf("mixed rates mislabeled: %+v", report)
	}
	if report.ReuseRegressionTokens != 2000 || report.ReuseRegressionUnpricedTokens != 2000 || report.ReuseRegressionEstimatedUSD != nil {
		t.Fatalf("historical cache loss repriced or cross-version comparison made: %+v", report)
	}
	if current.EstimatedUSD == nil || report.EstimatedUSD != 3+*current.EstimatedUSD {
		t.Fatalf("historical recorded prices recomputed: %+v", report)
	}
	oldOnly := Summarize([]Call{oldA, oldB})
	if oldOnly.PricingVersion != "historical-rates" || oldOnly.EstimatedUSD != 3 {
		t.Fatal("historical-only report claims current pricing")
	}
}
