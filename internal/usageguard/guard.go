// Package usageguard reads local provider subscription usage before an agent
// is started, allowing the invocation layer to choose a configured fallback.
package usageguard

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/config"
)

const (
	WindowFiveHour = "5h"
	WindowSevenDay = "7d"
)

// Window is one provider-reported subscription window.
type Window struct {
	UsedPercent float64
	ResetsAt    time.Time
}

// Snapshot is a point-in-time report keyed by WindowFiveHour or WindowSevenDay.
type Snapshot map[string]Window

// Reader retrieves usage for one provider. env is the exact environment that
// will be handed to the agent, so separate credential homes stay isolated.
type Reader interface {
	Fetch(ctx context.Context, env []string) (Snapshot, error)
}

// Request describes the agent about to execute.
type Request struct {
	Provider string
	Env      []string
}

// Decision explains whether starting the agent is safe under the configured
// thresholds. Unavailable means no fresh usage could be obtained.
type Decision struct {
	Allowed     bool
	Unavailable bool
	Blocked     []string
	ResetsAt    time.Time
	Err         error
}

// Checker is the dependency used by the invoker. Keeping it small makes the
// invocation policy independently testable and permits alternative readers.
type Checker interface {
	Check(context.Context, Request) Decision
	Invalidate(Request)
}

type cachedSnapshot struct {
	snapshot Snapshot
	expires  time.Time
}

// Guard applies configured thresholds to locally queried provider usage.
type Guard struct {
	config  config.UsageGuard
	readers map[string]Reader
	now     func() time.Time
	mu      sync.Mutex
	cache   map[string]cachedSnapshot
}

// New constructs an enabled guard. A nil guard is not needed: callers can use
// an absent Checker to disable the feature entirely.
func New(cfg config.UsageGuard, readers map[string]Reader) (*Guard, error) {
	if err := config.ValidateUsageGuard(cfg); err != nil {
		return nil, err
	}
	if readers == nil {
		readers = DefaultReaders()
	}
	return &Guard{config: cfg, readers: readers, now: time.Now, cache: map[string]cachedSnapshot{}}, nil
}

// Enabled reports whether any provider threshold was configured.
func (g *Guard) Enabled() bool { return g != nil && len(g.config.Providers) > 0 }

// Check retrieves a cached or fresh snapshot and evaluates its configured
// windows. Read failures follow OnUnavailable rather than leaking credentials
// or making a provider process error look like a runner failure.
func (g *Guard) Check(ctx context.Context, req Request) Decision {
	if !g.Enabled() {
		return Decision{Allowed: true}
	}
	budget, configured := g.config.Providers[req.Provider]
	if !configured {
		return Decision{Allowed: true}
	}

	key := cacheKey(req)
	now := g.now()
	g.mu.Lock()
	entry, cached := g.cache[key]
	g.mu.Unlock()
	var snapshot Snapshot
	if cached && entry.expires.After(now) {
		snapshot = entry.snapshot
	} else {
		reader := g.readers[req.Provider]
		if reader == nil {
			return g.unavailable(fmt.Errorf("no usage reader for provider %q", req.Provider))
		}
		var err error
		snapshot, err = reader.Fetch(ctx, req.Env)
		if err != nil {
			return g.unavailable(err)
		}
		ttl := time.Duration(g.config.CacheTTLSeconds) * time.Second
		if ttl <= 0 {
			ttl = 30 * time.Second
		}
		g.mu.Lock()
		g.cache[key] = cachedSnapshot{snapshot: snapshot, expires: now.Add(ttl)}
		g.mu.Unlock()
	}

	decision := Decision{Allowed: true}
	for window, limit := range budget.MaxUsedPercent {
		actual, ok := snapshot[window]
		if !ok {
			return g.unavailable(fmt.Errorf("provider %q did not report %s usage", req.Provider, window))
		}
		if actual.UsedPercent >= limit {
			decision.Allowed = false
			decision.Blocked = append(decision.Blocked, window)
			if decision.ResetsAt.IsZero() || (!actual.ResetsAt.IsZero() && actual.ResetsAt.Before(decision.ResetsAt)) {
				decision.ResetsAt = actual.ResetsAt
			}
		}
	}
	sort.Strings(decision.Blocked)
	return decision
}

func (g *Guard) unavailable(err error) Decision {
	if g.config.OnUnavailable == "allow" {
		return Decision{Allowed: true, Unavailable: true, Err: err}
	}
	return Decision{Allowed: false, Unavailable: true, Err: err}
}

// Invalidate drops the snapshot after an agent executes. The next attempt
// (including a contract-correction retry) gets a current reading instead of
// spending against a stale percentage.
func (g *Guard) Invalidate(req Request) {
	if g == nil {
		return
	}
	g.mu.Lock()
	delete(g.cache, cacheKey(req))
	g.mu.Unlock()
}

func cacheKey(req Request) string {
	return req.Provider + "\x00" + envValue(req.Env, "CODEX_HOME") + "\x00" + envValue(req.Env, "CLAUDE_CONFIG_DIR")
}

func envValue(env []string, name string) string {
	prefix := name + "="
	for _, value := range env {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix)
		}
	}
	return os.Getenv(name)
}
