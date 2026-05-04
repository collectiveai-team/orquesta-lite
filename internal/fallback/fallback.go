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
}

// Config controls backoff behaviour of a Caller.
type Config struct {
	InitialBackoff time.Duration
	Factor         int
	MaxBackoff     time.Duration
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

// AgentFunc is the callback signature for invoking an agent by name.
type AgentFunc func(ctx context.Context, agent string) (Outcome, error)

// Call iterates chain, skipping agents in cooldown. When an agent is rate-limited
// its cooldown is set to Now+backoff. When all agents in a pass are skipped or
// rate-limited, Call sleeps for the current backoff, then doubles it. If the
// doubled value exceeds MaxBackoff, ErrRateLimitExhausted is returned.
func (c *Caller) Call(ctx context.Context, chain []string, fn AgentFunc) (Outcome, string, error) {
	backoff := c.cfg.InitialBackoff
	for {
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
			if out.RateLimited {
				c.cooldown[agent] = c.cfg.Now().Add(backoff)
				continue
			}
			return out, agent, nil
		}

		_ = anyTried // both paths do the same thing

		// All agents either in cooldown or rate-limited this pass — wait then backoff.
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
