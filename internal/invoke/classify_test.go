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
			name: "auth failure with no result is auth_failed",
			result: &runner.Result{
				AuthFailed:   true,
				ResultExists: false,
			},
			wantFallback: true,
			wantReason:   "auth_failed",
		},
		{
			name: "auth-ish text but result written is success (no false skip)",
			result: &runner.Result{
				// e.g. the agent printed a FastAPI 401 "Not authenticated" body
				// while working, but still wrote its result file.
				AuthFailed:   true,
				ResultExists: true,
			},
			wantFallback: false,
			wantReason:   "",
		},
		{
			name: "rate-limit text but result written is success (no false wait)",
			result: &runner.Result{
				// e.g. the agent edited code containing "usage limit" but still
				// wrote its result file.
				RateLimited:  true,
				ResultExists: true,
			},
			wantFallback: false,
			wantReason:   "",
		},
		{
			name: "rate limit beats auth even with no result",
			result: &runner.Result{
				RateLimited:  true,
				AuthFailed:   true,
				ResultExists: false,
			},
			wantFallback: true,
			wantReason:   "rate_limit",
		},
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
			name: "timed out agent preserves timeout reason",
			result: &runner.Result{
				TimedOut:     true,
				ResultExists: false,
			},
			wantFallback: true,
			wantReason:   "timeout",
		},
		{
			name: "checkpoint survives timeout",
			result: &runner.Result{
				TimedOut:     true,
				ResultExists: true,
			},
			wantFallback: false,
			wantReason:   "",
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
