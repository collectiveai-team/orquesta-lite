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
	UsedPercent Percent
	ResetsAt    time.Time
	// ObservedAt is when the provider actually measured this value, which is
	// not the same as when it was read. A live query observes it now; a local
	// cache written by the agent tool may be hours old. Readers that know the
	// real measurement time set it; the guard stamps the rest at fetch time.
	ObservedAt time.Time
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
	// Missing lists configured windows the provider did not report. A partial
	// snapshot remains usable: observed windows are still enforced. The whole
	// provider is unavailable only when none of its configured windows appear.
	Missing []string
	// Stale lists configured windows whose reading was older than the
	// configured maximum age. They are not enforced: an out-of-date percentage
	// can be wrong in either direction, so it is treated as no reading at all.
	Stale    []string
	ResetsAt time.Time
	Err      error
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
	// lastGood survives both TTL expiry and Invalidate. When a provider query
	// fails - notably when it rate-limits the guard's own polling - a recent
	// previous reading is a far better answer than declaring the provider
	// unreadable and abandoning it. Staleness is enforced per window, so a
	// reading that is too old to trust is rejected on its own merits.
	lastGood map[string]Snapshot
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
	return &Guard{config: cfg, readers: readers, now: time.Now, cache: map[string]cachedSnapshot{}, lastGood: map[string]Snapshot{}}, nil
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
			// No converter covers this provider, so nothing can be assumed
			// about the numbers it would report.
			return g.unavailable(fmt.Errorf("no usage reader for provider %q", req.Provider))
		}
		fresh, err := reader.Fetch(ctx, req.Env)
		if err != nil {
			// Fall back to the previous reading rather than abandoning the
			// provider. Its windows carry their own observation times, so an
			// answer that has aged out is still rejected below.
			g.mu.Lock()
			previous, ok := g.lastGood[key]
			g.mu.Unlock()
			if !ok {
				return g.unavailable(err)
			}
			snapshot = previous
		} else {
			snapshot = stampObserved(fresh, now)
			ttl := time.Duration(g.config.CacheTTLSeconds) * time.Second
			if ttl <= 0 {
				ttl = 30 * time.Second
			}
			g.mu.Lock()
			g.cache[key] = cachedSnapshot{snapshot: snapshot, expires: now.Add(ttl)}
			g.lastGood[key] = snapshot
			g.mu.Unlock()
		}
	}

	decision := Decision{Allowed: true}
	maxAge := g.maxReadingAge()
	observed := 0
	for window, limit := range budget.MaxUsedPercent {
		actual, ok := snapshot[window]
		if !ok {
			decision.Missing = append(decision.Missing, window)
			continue
		}
		if !actual.ObservedAt.IsZero() && now.Sub(actual.ObservedAt) > maxAge {
			decision.Stale = append(decision.Stale, window)
			continue
		}
		observed++
		if actual.UsedPercent >= Percent(limit) {
			decision.Allowed = false
			decision.Blocked = append(decision.Blocked, window)
			if decision.ResetsAt.IsZero() || (!actual.ResetsAt.IsZero() && actual.ResetsAt.Before(decision.ResetsAt)) {
				decision.ResetsAt = actual.ResetsAt
			}
		}
	}
	sort.Strings(decision.Blocked)
	sort.Strings(decision.Missing)
	sort.Strings(decision.Stale)
	if observed == 0 {
		unavailable := g.unavailable(fmt.Errorf("provider %q reported no usable usage window (missing=%v stale=%v)", req.Provider, decision.Missing, decision.Stale))
		unavailable.Missing = decision.Missing
		unavailable.Stale = decision.Stale
		return unavailable
	}
	return decision
}

// maxReadingAge bounds how old a measurement may be and still be enforced. A
// window moves slowly enough that a few minutes of lag is harmless, while a
// reading from hours ago can be wrong in either direction.
func (g *Guard) maxReadingAge() time.Duration {
	if g.config.MaxReadingAgeSeconds > 0 {
		return time.Duration(g.config.MaxReadingAgeSeconds) * time.Second
	}
	return 15 * time.Minute
}

// stampObserved records when a reading was taken for any window whose reader
// did not already know. A live query is observed now; a reader sourcing a local
// cache written by the agent tool sets the real time itself.
func stampObserved(snapshot Snapshot, now time.Time) Snapshot {
	out := make(Snapshot, len(snapshot))
	for window, value := range snapshot {
		if value.ObservedAt.IsZero() {
			value.ObservedAt = now
		}
		out[window] = value
	}
	return out
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
	// Only the TTL entry is dropped. The agent just spent against the
	// subscription so the percentage is stale for enforcement, but the reading
	// stays available as a fallback for when the provider cannot be reached.
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
