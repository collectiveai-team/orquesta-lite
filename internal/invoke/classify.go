package invoke

import "github.com/lionelchamorro/orquestalite/internal/runner"

// Classify determines the fallback disposition for a runner result.
func Classify(r *runner.Result) (shouldFallback bool, reason string) {
	return classify(r)
}

func classify(r *runner.Result) (shouldFallback bool, reason string) {
	// Precedence: rate_limit -> timed_out (agent_crashed) -> (no result:
	// auth_failed | result_missing) -> success.
	//
	// auth_failed is only considered when the agent produced NO result. If the
	// agent wrote its result file it authenticated fine and succeeded — auth-ish
	// text elsewhere in its output (e.g. a FastAPI 401 "Not authenticated" body,
	// or a "login required" string in the code under edit) must NOT bench a
	// working agent. TimedOut is checked before the no-result branch because a
	// timed-out agent always lacks a result; checking that first would shadow the
	// more-specific "agent_crashed" reason.
	switch {
	case r.RateLimited:
		return true, "rate_limit"
	case r.TimedOut:
		return true, "agent_crashed"
	case !r.ResultExists:
		if r.AuthFailed {
			return true, "auth_failed"
		}
		return true, "result_missing"
	// "invalid_contract" is reserved for Phase 3 contract validation.
	default:
		return false, ""
	}
}
