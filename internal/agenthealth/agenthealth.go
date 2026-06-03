// Package agenthealth tracks per-agent failure counts within a single
// `orq-lite run` so that an agent that keeps returning result_missing
// (broken model name, missing auth, etc.) gets skipped instead of being
// retried on every role invocation.
package agenthealth

import (
	"sort"
	"sync"
)

// Reason categorises why an agent was skipped. Free-form, not validated.
type Reason string

const (
	ReasonUnreachable      Reason = "unreachable"
	ReasonResultMissing    Reason = "result_missing"
	ReasonRepeatedFailures Reason = "repeated_failures"
)

// Tracker is safe for concurrent use. Zero value is not usable — call New.
type Tracker struct {
	mu        sync.Mutex
	failures  map[string]int
	skipped   map[string]Reason
	threshold int
}

// New returns a Tracker that marks an agent as skipped after `threshold`
// consecutive non-rate-limit failures. A threshold of 0 or less disables
// the auto-skip behaviour; only explicit Skip calls take effect.
func New(threshold int) *Tracker {
	return &Tracker{
		failures:  map[string]int{},
		skipped:   map[string]Reason{},
		threshold: threshold,
	}
}

// MarkSuccess resets the failure count for agent. Idempotent.
func (t *Tracker) MarkSuccess(agent string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.failures, agent)
}

// MarkFailure increments the failure count for agent and returns true if the
// agent was just promoted to skipped status (i.e. crossed the threshold on
// this call). Rate-limit failures should NOT be tracked here — they have
// their own backoff path in the fallback caller.
func (t *Tracker) MarkFailure(agent string, reason Reason) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.threshold <= 0 {
		return false
	}
	if _, already := t.skipped[agent]; already {
		return false
	}
	t.failures[agent]++
	if t.failures[agent] >= t.threshold {
		t.skipped[agent] = reason
		return true
	}
	return false
}

// Skip marks agent as skipped with the given reason, bypassing the failure
// count threshold. Used by the startup preflight when an agent is known to
// be unreachable.
func (t *Tracker) Skip(agent string, reason Reason) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.skipped[agent] = reason
}

// IsSkipped reports whether agent has been marked skipped, and the reason.
func (t *Tracker) IsSkipped(agent string) (Reason, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.skipped[agent]
	return r, ok
}

// Filter returns chain with all currently-skipped agents removed, preserving
// order. The original slice is not modified.
func (t *Tracker) Filter(chain []string) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, 0, len(chain))
	for _, a := range chain {
		if _, skipped := t.skipped[a]; skipped {
			continue
		}
		out = append(out, a)
	}
	return out
}

// Skipped returns a stable, sorted snapshot of the currently-skipped agents
// with their reasons. Convenient for logging and assertions.
func (t *Tracker) Skipped() map[string]Reason {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]Reason, len(t.skipped))
	for k, v := range t.skipped {
		out[k] = v
	}
	return out
}

// SkippedAgents returns the skipped agent names sorted alphabetically.
func (t *Tracker) SkippedAgents() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	names := make([]string, 0, len(t.skipped))
	for k := range t.skipped {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
