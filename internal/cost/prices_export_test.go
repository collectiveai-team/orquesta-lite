package cost

import "testing"

func TestEstimateUSD_ExportedWrapper(t *testing.T) {
	usd, ok := EstimateUSD("claude-sonnet-4-6", 1_000_000, 1_000_000)
	if !ok || usd != 18.00 { // 3.00 input + 15.00 output per million
		t.Fatalf("EstimateUSD = %v, %v; want 18.00, true", usd, ok)
	}
	if _, ok := EstimateUSD("unknown-model-xyz", 100, 100); ok {
		t.Fatal("unknown model must report ok=false")
	}
}
