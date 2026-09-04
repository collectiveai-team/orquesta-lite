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

// TestEstimateUSD_Claude5 pins the Claude 5 rates the scaffolded team.json
// now defaults to, and the prefix order that resolves a dated snapshot id.
// claude-sonnet-5 must not fall through to the claude-sonnet-4 entry.
func TestEstimateUSD_Claude5(t *testing.T) {
	cases := []struct {
		model string
		want  float64
	}{
		{"claude-opus-5", 30.00},            // 5.00 input + 25.00 output
		{"claude-sonnet-5", 12.00},          // 2.00 input + 10.00 output
		{"claude-opus-5-20260401", 30.00},   // dated snapshot resolves by prefix
		{"claude-sonnet-5-20260401", 12.00}, // must not match claude-sonnet-4
	}
	for _, tc := range cases {
		usd, ok := EstimateUSD(tc.model, 1_000_000, 1_000_000)
		if !ok {
			t.Errorf("EstimateUSD(%q) reported ok=false; want a price", tc.model)
			continue
		}
		if usd != tc.want {
			t.Errorf("EstimateUSD(%q) = %v, want %v", tc.model, usd, tc.want)
		}
	}
}
