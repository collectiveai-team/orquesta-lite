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

// TestEstimateUSD_Codex56 pins the rates of the two Codex models the
// scaffolded team.json uses as the fallback of every role. They were
// unpriceable when they became the default: "gpt-5" sat in the price map but
// not in the prefix list, so "gpt-5.6-sol" matched nothing, every Codex
// invocation contributed 0 to the workflow cost budget, and a run that fell
// back to Codex could never exhaust its cap.
func TestEstimateUSD_Codex56(t *testing.T) {
	cases := []struct {
		model string
		want  float64
	}{
		{"gpt-5.6-sol", 24.00},            // 4.00 input + 20.00 output
		{"gpt-5.6-terra", 14.00},          // 2.00 input + 12.00 output
		{"gpt-5.6-sol-2026-04-01", 24.00}, // dated snapshot resolves by prefix
		{"gpt-5", 11.25},                  // 1.25 input + 10.00 output
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
	// An unknown gpt-5.x must stay unpriced rather than inherit another
	// model's rate through a bare "gpt-5" prefix.
	if _, ok := EstimateUSD("gpt-5.7-unknown", 100, 100); ok {
		t.Error("an unknown gpt-5.x model must report ok=false, not a guessed rate")
	}
}
