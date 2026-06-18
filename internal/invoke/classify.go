package invoke

import "github.com/lionelchamorro/orquestalite/internal/runner"

// Classify determines the fallback disposition for a runner result.
func Classify(r *runner.Result) (shouldFallback bool, reason string) {
	return classify(r)
}

func classify(r *runner.Result) (shouldFallback bool, reason string) {
	// Precedence: auth_failed -> rate_limit -> timed_out (agent_crashed) ->
	// result_missing -> success. auth_failed is checked first: an interactive
	// auth prompt is the most specific, non-recoverable signal (the agent never
	// produced a result because it could not authenticate headless).
	// TimedOut must be checked before !ResultExists because a timed-out agent
	// always has ResultExists=false; checking !ResultExists first would shadow
	// the more-specific "agent_crashed" reason.
	switch {
	case r.AuthFailed:
		return true, "auth_failed"
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
