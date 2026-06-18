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
	// Both rate_limit and auth_failed are only considered when the agent produced
	// NO result. A written result file means the agent ran fine, so rate-limit or
	// auth text elsewhere in its output (e.g. "usage limit" / "session limit" in
	// the code it edited, or a FastAPI 401 "Not authenticated" body) must NOT
	// bench a working agent. TimedOut is checked before the no-result branch
	// because a timed-out agent always lacks a result; checking that first would
	// shadow the more-specific "agent_crashed" reason.
	switch {
	case r.RateLimited && !r.ResultExists:
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
		// A result was written: the agent ran fine. Rate-limit/auth text
		// elsewhere in its output (e.g. "usage limit" in code it edited, or a
		// 401 body) must not bench a successful agent.
		return false, ""
	}
}
