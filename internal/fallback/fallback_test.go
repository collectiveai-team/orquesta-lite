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
