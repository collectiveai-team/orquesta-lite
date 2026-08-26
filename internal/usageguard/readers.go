package usageguard

import (
	"context"
	"errors"
	"fmt"
)

// DefaultReaders returns the built-in local provider readers. They never emit
// access tokens in errors or events; callers only receive an availability
// decision and (when configured) move to a fallback agent.
//
// Codex is read through its app-server first and its own session bookkeeping
// second, so a busy machine that cannot spare a process still gets a reading.
// Claude has no equivalent local percentage - the CLI keeps only a breach flag,
// not a utilization - so its sole numeric source is the OAuth endpoint, which
// is why the guard leans on retained readings when that endpoint throttles.
func DefaultReaders() map[string]Reader {
	return map[string]Reader{
		"codex":  Chain{CodexReader{}, CodexLocalReader{}},
		"claude": ClaudeReader{},
	}
}

// Chain queries sources in order and returns the first successful reading.
type Chain []Reader

func (c Chain) Fetch(ctx context.Context, env []string) (Snapshot, error) {
	var errs []error
	for _, reader := range c {
		snapshot, err := reader.Fetch(ctx, env)
		if err == nil {
			return snapshot, nil
		}
		errs = append(errs, err)
		// A throttled source says nothing about the others, but every source in
		// a provider's chain is backed by the same account, so stop and let the
		// guard reuse its previous reading instead.
		if errors.Is(err, ErrProviderRateLimited) {
			break
		}
	}
	if len(errs) == 0 {
		return nil, fmt.Errorf("no usage source configured")
	}
	return nil, errors.Join(errs...)
}
