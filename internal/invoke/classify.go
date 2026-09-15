package invoke

import "github.com/collectiveai-team/orquesta-lite/internal/runner"

// Classify determines the fallback disposition for a runner result.
func Classify(r *runner.Result) (shouldFallback bool, reason string) {
	return classify(r)
}

func classify(r *runner.Result) (shouldFallback bool, reason string) {
	// rate_limit and auth_failed are read out of the agent's own stdout, so they
	// fire on an agent that merely *printed* "usage limit" or a 401 body while
	// working. A written result disproves them, and that precedence is why this
	// switch exists.
	//
	// A timeout is not a heuristic. It is context.DeadlineExceeded on the
	// subprocess: proof the process was killed mid-turn. A file written before
	// the kill therefore proves nothing about completion — every review prompt in
	// the development pack instructs the agent to write a schema-valid scaffold
	// *before* it starts work, so the file left behind by a killed reviewer is a
	// review that never happened. Accepting it recorded the step as succeeded,
	// silently, and the run only noticed fifteen minutes later at a downstream
	// gate that could name neither the role nor the timeout.
	//
	// So a result file outranks the text heuristics and yields to the clock.
	switch {
	case r.RateLimited && !r.ResultExists:
		return true, "rate_limit"
	case r.TimedOut:
		return true, "timeout"
	case r.ResultExists:
		return false, ""
	case r.AuthFailed:
		return true, "auth_failed"
	default:
		return true, "result_missing"
	}
}
