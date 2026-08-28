package invoke

import "github.com/collectiveai-team/orquesta-lite/internal/runner"

// Classify determines the fallback disposition for a runner result.
func Classify(r *runner.Result) (shouldFallback bool, reason string) {
	return classify(r)
}

func classify(r *runner.Result) (shouldFallback bool, reason string) {
	// A valid result file is a durable checkpoint and wins even if the provider
	// process was terminated immediately afterwards. Contract validation still
	// runs in RoleInvoker before the attempt is accepted.
	//
	switch {
	case r.ResultExists:
		return false, ""
	case r.RateLimited:
		return true, "rate_limit"
	case r.TimedOut:
		return true, "timeout"
	case r.Aborted:
		// Someone cancelled this session by hand. Retrying or falling back to
		// another agent would restart exactly the work they stopped, so this
		// reason is terminal — see RoleInvoker, which turns it into an error
		// rather than another attempt.
		return true, "aborted"
	default:
		if r.AuthFailed {
			return true, "auth_failed"
		}
		return true, "result_missing"
	}
}
