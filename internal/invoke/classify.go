package invoke

import "github.com/lionelchamorro/orquestalite/internal/runner"

// Classify determines the fallback disposition for a runner result.
func Classify(r *runner.Result) (shouldFallback bool, reason string) {
	return classify(r)
}

func classify(r *runner.Result) (shouldFallback bool, reason string) {
	// Precedence: rate_limit -> timed_out (agent_crashed) -> result_missing -> success.
	// TimedOut must be checked before !ResultExists because a timed-out agent
	// always has ResultExists=false; checking !ResultExists first would shadow
	// the more-specific "agent_crashed" reason.
	switch {
	case r.RateLimited:
		return true, "rate_limit"
	case r.TimedOut:
		return true, "agent_crashed"
	case !r.ResultExists:
		return true, "result_missing"
	// "invalid_contract" is reserved for Phase 3 contract validation.
	default:
		return false, ""
	}
}
