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

	// ResetAt, when non-zero, is the time the agent's rate limit is expected to
	// lift (parsed from a "try again at …" / "retry after …" hint). It is only
	// meaningful when FallbackReason == "rate_limit". When set, the agent's
	// cooldown is keyed to this time so the loop sleeps until the real reset
	// instead of guessing with exponential backoff.
	ResetAt time.Time
}

// Config controls backoff behaviour of a Caller.
type Config struct {
	InitialBackoff time.Duration
	Factor         int
	MaxBackoff     time.Duration
	// MaxAttempts caps non-rate-limit fallback attempts per agent per Call to
	// prevent infinite loops on a permanently broken agent. Defaults to
	// 2*len(chain) total (i.e. ~2 per agent) when zero. Rate-limited agents are
	// NOT capped: the loop waits for them to recover (see Call).
	MaxAttempts int
	// Now is injectable for testing. Defaults to time.Now.
	Now func() time.Time
	// OnWait, when set, is called before the loop sleeps waiting for a
	// rate-limited agent to recover. It reports the agent, the fallback reason,
	// and the time the loop will wake up. Used for operator-visible logging so a
	// long wait is not silent.
	OnWait func(agent, reason string, until time.Time)
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

// ErrRateLimitExhausted is retained for backward compatibility but is no longer
// returned: the loop now waits for a rate-limited agent to recover rather than
// abandoning the role once backoff exceeds MaxBackoff.
//
// Deprecated: rate limits no longer terminate a Call; they are waited out.
var ErrRateLimitExhausted = errors.New("all agents rate-limited past max backoff")

// ErrAllAgentsFailed is returned when every agent in the chain has failed for a
// non-recoverable reason (result_missing, agent_crashed, …) and none is merely
// rate-limited. A rate-limited agent is recoverable, so its presence keeps the
// loop waiting instead of returning this error.
var ErrAllAgentsFailed = errors.New("all agents in chain failed")

// AgentFunc is the callback signature for invoking an agent by name.
type AgentFunc func(ctx context.Context, agent string) (Outcome, error)

// Call iterates chain trying every immediately-available agent. A success
// returns at once. When no agent succeeds, the disposition depends on why:
//
//   - rate_limit: the agent is placed in cooldown until its parsed ResetAt (or
//     now+backoff when no reset hint is available). As long as at least one
//     agent is cooling down, the loop SLEEPS until the soonest reset and retries
//     — it waits for a rate-limited agent to become available rather than
//     abandoning the role. Sleeps are chunked to MaxBackoff so other agents'
//     cooldowns are rechecked promptly and ctx cancellation stays responsive.
//   - non-rate-limit fallback (result_missing, agent_crashed): counted per
//     agent toward MaxAttempts. Waiting will not help these, so once every
//     agent is exhausted (and none is rate-limited) the loop returns
//     ErrAllAgentsFailed.
func (c *Caller) Call(ctx context.Context, chain []string, fn AgentFunc) (Outcome, string, error) {
	maxAttempts := c.cfg.MaxAttempts
	if maxAttempts <= 0 {
		n := len(chain)
		if n < 1 {
			n = 1
		}
		maxAttempts = 2 * n
	}
	perAgent := maxAttempts / max(1, len(chain))
	if perAgent < 1 {
		perAgent = 1
	}

	backoff := c.cfg.InitialBackoff
	if backoff <= 0 {
		backoff = time.Second
	}

	// failures counts non-recoverable fallbacks per agent within this Call.
	failures := make(map[string]int, len(chain))
	var lastAgent string

	for {
		triedThisPass := false
		rateLimitedThisPass := false

		for _, agent := range chain {
			if cd, ok := c.cooldown[agent]; ok && cd.After(c.cfg.Now()) {
				continue // cooling down (rate-limited) — wait for it below
			}
			if failures[agent] >= perAgent {
				continue // non-recoverably broken this Call
			}

			triedThisPass = true
			lastAgent = agent
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
				return out, agent, nil // success
			}

			if out.FallbackReason == "rate_limit" {
				until := c.cfg.Now().Add(backoff)
				if !out.ResetAt.IsZero() && out.ResetAt.After(until) {
					until = out.ResetAt
				}
				c.cooldown[agent] = until
				rateLimitedThisPass = true
			} else {
				failures[agent]++
			}
		}

		// Soonest future cooldown among chain agents = an agent we can wait for.
		wake, recoverable := c.soonestCooldown(chain)
		if recoverable {
			wait := wake.Sub(c.cfg.Now())
			if wait < 0 {
				wait = 0
			}
			// Chunk the sleep so other cooldowns are rechecked promptly.
			if c.cfg.MaxBackoff > 0 && wait > c.cfg.MaxBackoff {
				wait = c.cfg.MaxBackoff
			}
			if c.cfg.OnWait != nil {
				c.cfg.OnWait(lastAgent, "rate_limit", wake)
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return Outcome{}, lastAgent, ctx.Err()
			}
			// Grow the blind-backoff window only when an agent actually
			// rate-limited this pass with no reset hint to key off.
			if rateLimitedThisPass {
				next := backoff * time.Duration(c.cfg.Factor)
				if c.cfg.MaxBackoff > 0 && next > c.cfg.MaxBackoff {
					next = c.cfg.MaxBackoff
				}
				backoff = next
			}
			continue
		}

		// Nothing is recoverable. If any agent still has retries left, loop to
		// retry it; otherwise every agent is exhausted and the role has failed.
		anyRetryable := false
		for _, agent := range chain {
			if failures[agent] < perAgent {
				anyRetryable = true
				break
			}
		}
		if anyRetryable && triedThisPass {
			continue
		}
		return Outcome{}, lastAgent, ErrAllAgentsFailed
	}
}

// soonestCooldown returns the earliest still-future cooldown expiry among the
// chain's agents, and whether any such agent exists. A cooling-down agent is
// rate-limited and therefore recoverable by waiting.
func (c *Caller) soonestCooldown(chain []string) (time.Time, bool) {
	now := c.cfg.Now()
	var soonest time.Time
	found := false
	for _, agent := range chain {
		cd, ok := c.cooldown[agent]
		if !ok || !cd.After(now) {
			continue
		}
		if !found || cd.Before(soonest) {
			soonest = cd
			found = true
		}
	}
	return soonest, found
}
