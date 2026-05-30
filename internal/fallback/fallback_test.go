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

func TestCallRole_Exhausted(t *testing.T) {
	chain := []string{"a1"}
	cfg := Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: 4 * time.Millisecond, Now: time.Now}
	c := NewCaller(cfg)
	_, _, err := c.Call(context.Background(), chain, func(ctx context.Context, name string) (Outcome, error) {
		return Outcome{RateLimited: true}, nil
	})
	if !errors.Is(err, ErrRateLimitExhausted) {
		t.Fatalf("expected ErrRateLimitExhausted, got %v", err)
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
