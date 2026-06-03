package invoke

import (
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/runner"
)

func TestClassifyFallbackDisposition(t *testing.T) {
	tests := []struct {
		name         string
		result       *runner.Result
		wantFallback bool
		wantReason   string
	}{
		{
			name: "rate limit takes precedence",
			result: &runner.Result{
				RateLimited:  true,
				TimedOut:     true,
				ResultExists: false,
			},
			wantFallback: true,
			wantReason:   "rate_limit",
		},
		{
			name: "timed out agent crashed",
			result: &runner.Result{
				TimedOut:     true,
				ResultExists: false,
			},
			wantFallback: true,
			wantReason:   "agent_crashed",
		},
		{
			name: "result missing",
			result: &runner.Result{
				ResultExists: false,
			},
			wantFallback: true,
			wantReason:   "result_missing",
		},
		{
			name: "success",
			result: &runner.Result{
				ResultExists: true,
			},
			wantFallback: false,
			wantReason:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotFallback, gotReason := classify(tt.result)
			if gotFallback != tt.wantFallback || gotReason != tt.wantReason {
				t.Fatalf("classify() = (%v, %q), want (%v, %q)", gotFallback, gotReason, tt.wantFallback, tt.wantReason)
			}
		})
	}
}
