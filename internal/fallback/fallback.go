package fallback

import (
	"context"
	"errors"
	"time"
)

// Outcome carries the result of a single agent invocation.
type Outcome struct {
	RateLimited  bool
	ResultExists bool
	TimedOut     bool

	// ShouldFallback signals that the caller wants to advance to the next agent
	// in the chain. Set FallbackReason to one of: "rate_limit", "result_missing",
	// "agent_crashed", "invalid_contract" (reserved for Phase 3).
	// For backward compat, Call also treats RateLimited=true as ShouldFallback.
	ShouldFallback bool
	FallbackReason string
}

// Config controls backoff behaviour of a Caller.
type Config struct {
	InitialBackoff time.Duration
	Factor         int
	MaxBackoff     time.Duration
	// MaxAttempts caps total non-rate-limit fallback attempts per Call to prevent
	// infinite loops. Defaults to 2 * len(chain) when zero.
	// Rate-limit fallbacks are governed by the backoff/MaxBackoff path instead.
	MaxAttempts int
	// Now is injectable for testing. Defaults to time.Now.
	Now func() time.Time
}

// Caller iterates an agent chain with cooldown memory and exponential backoff.
type Caller struct {
	cfg      Config
	cooldown map[string]time.Time
}

// NewCaller constructs a Caller with the given Config, applying safe defaults.
func NewCaller(cfg Config) *Caller {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Factor < 2 {
		cfg.Factor = 2
	}
	return &Caller{cfg: cfg, cooldown: map[string]time.Time{}}
}

// ErrRateLimitExhausted is returned when the backoff would exceed MaxBackoff.
var ErrRateLimitExhausted = errors.New("all agents rate-limited past max backoff")

// ErrAllAgentsFailed is returned when every agent in the chain has exceeded
// the MaxAttempts cap without producing a successful (non-fallback) result.
// Only non-rate-limit fallbacks count toward this cap.
var ErrAllAgentsFailed = errors.New("all agents in chain failed")

// AgentFunc is the callback signature for invoking an agent by name.
type AgentFunc func(ctx context.Context, agent string) (Outcome, error)

// Call iterates chain, skipping agents in cooldown. When an agent signals
// ShouldFallback (or the legacy RateLimited=true), the next agent is tried.
// Rate-limit fallbacks place the agent in cooldown with exponential backoff;
// other fallback reasons do not set cooldown.
//
// The cap (MaxAttempts) counts only non-rate-limit fallback invocations.
// Rate-limit exhaustion is still controlled by the MaxBackoff path, which
// returns ErrRateLimitExhausted.
func (c *Caller) Call(ctx context.Context, chain []string, fn AgentFunc) (Outcome, string, error) {
	maxAttempts := c.cfg.MaxAttempts
	if maxAttempts <= 0 {
		n := len(chain)
		if n < 1 {
			n = 1
		}
		maxAttempts = 2 * n
	}

	backoff := c.cfg.InitialBackoff
	// nonRateLimitAttempts counts invocations that result in a non-rate-limit fallback.
	nonRateLimitAttempts := 0

	for {
		anyRateLimited := false
		anyTried := false

		for _, agent := range chain {
			if cd, ok := c.cooldown[agent]; ok && cd.After(c.cfg.Now()) {
				continue
			}

			anyTried = true
			out, err := fn(ctx, agent)
			if err != nil {
				return out, agent, err
			}

			// Normalise: legacy RateLimited=true implies ShouldFallback.
			if out.RateLimited && !out.ShouldFallback {
				out.ShouldFallback = true
				if out.FallbackReason == "" {
					out.FallbackReason = "rate_limit"
				}
			}

			if !out.ShouldFallback {
				// Success — return immediately.
				return out, agent, nil
			}

			if out.FallbackReason == "rate_limit" {
				// Rate-limit: place in cooldown; handled by backoff/MaxBackoff path.
				c.cooldown[agent] = c.cfg.Now().Add(backoff)
				anyRateLimited = true
			} else {
				// Non-rate-limit fallback: count toward cap, no cooldown.
				nonRateLimitAttempts++
				if nonRateLimitAttempts >= maxAttempts {
					return Outcome{}, agent, ErrAllAgentsFailed
				}
			}
		}

		if !anyTried {
			// All agents in cooldown — treat as rate-limited pass.
			anyRateLimited = true
		}

		if !anyRateLimited {
			// All agents were tried and all failed with non-rate-limit reasons,
			// but cap wasn't hit yet (can happen if cap > chain length). Loop again
			// to retry non-rate-limited agents until cap fires.
			continue
		}

		// At least one rate-limited agent: sleep then double backoff.
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return Outcome{}, "", ctx.Err()
		}

		next := backoff * time.Duration(c.cfg.Factor)
		if next > c.cfg.MaxBackoff {
			return Outcome{}, "", ErrRateLimitExhausted
		}
		backoff = next
	}
}
