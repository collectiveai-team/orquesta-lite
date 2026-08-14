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
	default:
		if r.AuthFailed {
			return true, "auth_failed"
		}
		return true, "result_missing"
	}
}
