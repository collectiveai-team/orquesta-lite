package fallback

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeRun struct {
	rateLimited  bool
	resultExists bool
	timedOut     bool
}

func TestCallRole_FirstAgentWins(t *testing.T) {
	calls := 0
	chain := []string{"a1", "a2"}
	cfg := Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: 10 * time.Millisecond, Now: time.Now}
	c := NewCaller(cfg)

	res, agentUsed, err := c.Call(context.Background(), chain, func(ctx context.Context, agent string) (Outcome, error) {
		calls++
		if agent == "a1" {
			return Outcome{ResultExists: true}, nil
		}
		return Outcome{ResultExists: false}, errors.New("should not run")
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || agentUsed != "a1" {
		t.Errorf("calls=%d agent=%s", calls, agentUsed)
	}
	if !res.ResultExists {
		t.Errorf("expected result")
	}
}

func TestCallRole_FallsOverOnRateLimit(t *testing.T) {
	chain := []string{"a1", "a2"}
	cfg := Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: 10 * time.Millisecond, Now: time.Now}
	c := NewCaller(cfg)
	_, agent, err := c.Call(context.Background(), chain, func(ctx context.Context, name string) (Outcome, error) {
		if name == "a1" {
			return Outcome{RateLimited: true}, nil
		}
		return Outcome{ResultExists: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if agent != "a2" {
		t.Errorf("expected a2, got %s", agent)
	}
}

func TestCallRole_AllRateLimitedThenSucceeds(t *testing.T) {
	chain := []string{"a1"}
	pass := 0
	cfg := Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: 10 * time.Millisecond, Now: time.Now}
	c := NewCaller(cfg)
	_, _, err := c.Call(context.Background(), chain, func(ctx context.Context, name string) (Outcome, error) {
		pass++
		if pass < 3 {
			return Outcome{RateLimited: true}, nil
		}
		return Outcome{ResultExists: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if pass != 3 {
		t.Errorf("expected 3 passes, got %d", pass)
	}
}

// TestCallRole_RateLimitLoopsUntilAvailable verifies the loop no longer gives
// up on a rate-limited agent: it keeps sleeping and retrying until the agent
// recovers, rather than returning ErrRateLimitExhausted once backoff exceeds
// MaxBackoff.
func TestCallRole_RateLimitLoopsUntilAvailable(t *testing.T) {
	chain := []string{"a1"}
	cfg := Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: 4 * time.Millisecond, Now: time.Now}
	c := NewCaller(cfg)
	pass := 0
	_, agent, err := c.Call(context.Background(), chain, func(ctx context.Context, name string) (Outcome, error) {
		pass++
		if pass < 5 {
			return Outcome{RateLimited: true}, nil // limited well past MaxBackoff
		}
		return Outcome{ResultExists: true}, nil
	})
	if err != nil {
		t.Fatalf("expected the loop to wait out the rate limit, got %v", err)
	}
	if agent != "a1" || pass != 5 {
		t.Fatalf("agent=%s pass=%d; expected to retry a1 until it recovered", agent, pass)
	}
}

// TestCallRole_RateLimitRespectsContextCancel verifies an indefinitely
// rate-limited agent does not hang forever: ctx cancellation breaks the wait.
func TestCallRole_RateLimitRespectsContextCancel(t *testing.T) {
	chain := []string{"a1"}
	cfg := Config{InitialBackoff: time.Hour, Factor: 2, MaxBackoff: time.Hour, Now: time.Now}
	c := NewCaller(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	_, _, err := c.Call(ctx, chain, func(ctx context.Context, name string) (Outcome, error) {
		return Outcome{RateLimited: true}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// TestCallRole_WaitsForRateLimitedWhenFallbacksBroken is the regression for the
// reported failure: a primary that is rate-limited plus a fallback that is
// non-recoverably broken should wait for the primary to recover, not fail the
// role.
func TestCallRole_WaitsForRateLimitedWhenFallbacksBroken(t *testing.T) {
	chain := []string{"codex", "gemini"}
	cfg := Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: 4 * time.Millisecond, Now: time.Now}
	c := NewCaller(cfg)
	codexCalls := 0
	_, agent, err := c.Call(context.Background(), chain, func(ctx context.Context, name string) (Outcome, error) {
		switch name {
		case "codex":
			codexCalls++
			if codexCalls < 3 {
				return Outcome{RateLimited: true}, nil
			}
			return Outcome{ResultExists: true}, nil // recovers
		default: // gemini: permanently broken (auth failure)
			return Outcome{ShouldFallback: true, FallbackReason: "result_missing"}, nil
		}
	})
	if err != nil {
		t.Fatalf("expected to wait for codex to recover, got %v", err)
	}
	if agent != "codex" {
		t.Fatalf("expected codex to win after recovery, got %s", agent)
	}
}

// TestCallRole_ResetAtKeysCooldown verifies a parsed ResetAt drives the
// cooldown expiry (so the loop sleeps until the real reset, not now+backoff).
func TestCallRole_ResetAtKeysCooldown(t *testing.T) {
	nowT := time.Unix(1_700_000_000, 0)
	reset := nowT.Add(90 * time.Minute)
	chain := []string{"a", "b"}
	cfg := Config{
		InitialBackoff: time.Second,
		Factor:         2,
		MaxBackoff:     30 * time.Minute,
		Now:            func() time.Time { return nowT },
	}
	c := NewCaller(cfg)
	_, agentUsed, err := c.Call(context.Background(), chain, func(ctx context.Context, name string) (Outcome, error) {
		if name == "a" {
			return Outcome{RateLimited: true, ShouldFallback: true, FallbackReason: "rate_limit", ResetAt: reset}, nil
		}
		return Outcome{ResultExists: true}, nil
	})
	if err != nil || agentUsed != "b" {
		t.Fatalf("agent=%s err=%v; expected fallback to b", agentUsed, err)
	}
	if cd := c.cooldown["a"]; !cd.Equal(reset) {
		t.Fatalf("cooldown for a = %v, want ResetAt %v", cd, reset)
	}
}

// TestCall_FallbackOnResultMissing verifies that ShouldFallback=true advances
// to the next agent in the chain, and the successful second agent is returned.
func TestCall_FallbackOnResultMissing(t *testing.T) {
	chain := []string{"a", "b"}
	cfg := Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: 10 * time.Millisecond, Now: time.Now}
	c := NewCaller(cfg)

	out, agentUsed, err := c.Call(context.Background(), chain, func(ctx context.Context, name string) (Outcome, error) {
		if name == "a" {
			return Outcome{ShouldFallback: true, FallbackReason: "result_missing"}, nil
		}
		return Outcome{ResultExists: true}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agentUsed != "b" {
		t.Errorf("expected agent b, got %s", agentUsed)
	}
	if !out.ResultExists {
		t.Errorf("expected ResultExists=true from agent b")
	}
}

// TestCall_NoCooldownOnNonRateLimit verifies that a result_missing fallback
// does NOT place the agent in cooldown (agent a is retryable immediately after).
func TestCall_NoCooldownOnNonRateLimit(t *testing.T) {
	chain := []string{"a", "b"}
	cfg := Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: 10 * time.Millisecond, Now: time.Now}
	c := NewCaller(cfg)

	// First call: a falls back (result_missing), b succeeds.
	_, _, err := c.Call(context.Background(), chain, func(ctx context.Context, name string) (Outcome, error) {
		if name == "a" {
			return Outcome{ShouldFallback: true, FallbackReason: "result_missing"}, nil
		}
		return Outcome{ResultExists: true}, nil
	})
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Second call immediately: a should be tried again (no cooldown), and succeed this time.
	var triedA bool
	_, _, err = c.Call(context.Background(), chain, func(ctx context.Context, name string) (Outcome, error) {
		if name == "a" {
			triedA = true
			return Outcome{ResultExists: true}, nil
		}
		return Outcome{ResultExists: true}, nil
	})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if !triedA {
		t.Errorf("expected agent a to be tried on second call (no cooldown), but it was skipped")
	}
}

// TestCall_AllAgentsFailMaxAttempts verifies that when every agent returns
// ShouldFallback=true indefinitely, ErrAllAgentsFailed is returned after
// the MaxAttempts cap is hit.
func TestCall_AllAgentsFailMaxAttempts(t *testing.T) {
	chain := []string{"a", "b"}
	cfg := Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: 100 * time.Millisecond, Now: time.Now}
	c := NewCaller(cfg)

	_, _, err := c.Call(context.Background(), chain, func(ctx context.Context, name string) (Outcome, error) {
		return Outcome{ShouldFallback: true, FallbackReason: "result_missing"}, nil
	})
	if !errors.Is(err, ErrAllAgentsFailed) {
		t.Fatalf("expected ErrAllAgentsFailed, got %v", err)
	}
}

// TestCall_RateLimitStillCausesCooldown verifies backward compat: when
// RateLimited=true the agent is placed in cooldown as before.
func TestCall_RateLimitStillCausesCooldown(t *testing.T) {
	nowT := time.Now()
	chain := []string{"a", "b"}
	cfg := Config{
		InitialBackoff: time.Hour, // large so cooldown is obvious
		Factor:         2,
		MaxBackoff:     2 * time.Hour,
		Now:            func() time.Time { return nowT },
	}
	c := NewCaller(cfg)

	// Call once: a is rate-limited, b succeeds.
	_, agentUsed, err := c.Call(context.Background(), chain, func(ctx context.Context, name string) (Outcome, error) {
		if name == "a" {
			return Outcome{RateLimited: true, ShouldFallback: true, FallbackReason: "rate_limit"}, nil
		}
		return Outcome{ResultExists: true}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agentUsed != "b" {
		t.Errorf("expected b, got %s", agentUsed)
	}

	// Immediately after, a should be in cooldown (its entry is in the future).
	cd, ok := c.cooldown["a"]
	if !ok {
		t.Fatal("agent a not in cooldown map after rate_limit fallback")
	}
	if !cd.After(nowT) {
		t.Errorf("cooldown for a (%v) is not after now (%v)", cd, nowT)
	}
}
