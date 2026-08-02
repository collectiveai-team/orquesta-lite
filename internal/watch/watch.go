// Package watch runs a long-lived per-project daemon that polls GitHub (via
// the already-authenticated `gh` CLI) for new/updated issues and PRs and
// triggers intake (for issues) or review (for PRs) on items it has not
// processed before. A cursor (last_seen per type) and a processed-update set
// persist in .orquestalite/watch.json so the same provider update is never
// processed twice while a newer update can intentionally trigger again.
package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/eventlog"
)

// ItemType selects which GitHub object a watch operates on.
type ItemType string

const (
	ItemIssue ItemType = "issues"
	ItemPR    ItemType = "prs"
)

// Item is one polled GitHub object the daemon may act on.
type Item struct {
	Type      ItemType  `json:"type"`
	Number    string    `json:"number"` // "12" (without #)
	Title     string    `json:"title"`
	Body      string    `json:"body"`   // issue body (issues only; "" for PRs)
	Author    string    `json:"author"` // login; used to skip orq-lite's own PRs
	UpdatedAt time.Time `json:"updated_at"`
}

// Lister is the subset of `gh` the daemon needs. The production implementation
// shells out to `gh`; tests inject a fake so the cursor/processed/dedup
// behavior is asserted without a real GitHub.
type Lister interface {
	// WhoAmI returns the authenticated user's login, used to identify PRs that
	// orq-lite itself opened (skipped unless ReviewOwnPRs is set).
	WhoAmI(ctx context.Context) (string, error)
	ListIssues(ctx context.Context, since time.Time) ([]Item, error)
	ListPRs(ctx context.Context, since time.Time) ([]Item, error)
}

// Config tunes one watch daemon.
type Config struct {
	ProjectDir   string
	Interval     time.Duration // poll cadence; <=0 → DefaultInterval
	Enabled      map[ItemType]bool
	ReviewOwnPRs bool // review PRs orq-lite opened (default: skip them)
	Lister       Lister
	// Trigger receives a versioned flow ref, structured inputs and a stable
	// source idempotency key.
	Trigger  func(context.Context, Trigger) error
	FlowRefs map[ItemType]string
	// Now is the clock; nil → time.Now. Useful for deterministic tests.
	Now func() time.Time
	// Log, when set, receives a `watch_tick_error` event whenever a poll tick
	// fails. nil → tick errors are only written to stderr.
	Log *eventlog.Logger
	// MaxConsecutiveErrors aborts the daemon after this many consecutive
	// failed ticks. <=0 → MaxConsecutiveErrorsDefault.
	MaxConsecutiveErrors int
}

type Trigger struct {
	FlowRef        string         `json:"flow_ref"`
	Inputs         map[string]any `json:"inputs"`
	IdempotencyKey string         `json:"idempotency_key"`
}

// DefaultInterval is the poll cadence when Config.Interval is unset.
const DefaultInterval = 60 * time.Second

// MaxConsecutiveErrorsDefault is how many consecutive tick failures the
// daemon tolerates before aborting (so a permanently broken `gh` does not
// spin forever silently).
const MaxConsecutiveErrorsDefault = 10

// State is the persisted watch progress: which types are enabled, the poll
// interval, the last-seen cursor per type, and the processed-update set per
// type, keyed by provider item/update identity.
type State struct {
	Enabled         map[ItemType]bool            `json:"enabled"`
	IntervalSeconds int                          `json:"interval_seconds"`
	LastSeen        map[ItemType]time.Time       `json:"last_seen"`
	Processed       map[ItemType]map[string]bool `json:"processed"`
}

func statePath(projectDir string) string {
	return filepath.Join(projectDir, ".orquestalite", "watch.json")
}

// LoadState reads the persisted watch state, or returns a fresh default state
// when none exists. A missing .orquestalite directory is created.
func LoadState(projectDir string) (*State, error) {
	raw, err := os.ReadFile(statePath(projectDir))
	if err != nil {
		if os.IsNotExist(err) {
			return &State{
				Enabled:   map[ItemType]bool{},
				LastSeen:  map[ItemType]time.Time{},
				Processed: map[ItemType]map[string]bool{},
			}, nil
		}
		return nil, err
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("parse watch.json: %w", err)
	}
	if s.Enabled == nil {
		s.Enabled = map[ItemType]bool{}
	}
	if s.LastSeen == nil {
		s.LastSeen = map[ItemType]time.Time{}
	}
	if s.Processed == nil {
		s.Processed = map[ItemType]map[string]bool{}
	}
	return &s, nil
}

// Save writes the state to .orquestalite/watch.json atomically.
func (s *State) Save(projectDir string) error {
	path := statePath(projectDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Run drives the long-lived poll loop until ctx is cancelled. Each tick polls
// only the enabled types, triggers intake/review on unseen items, advances the
// cursor, and persists state. Failures in a single poll (list or trigger) are
// logged via the returned error channel-ish: Run never returns for a transient
// error — it logs and waits for the next tick so the daemon stays up.
func Run(ctx context.Context, cfg Config) error {
	interval := cfg.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	state, err := LoadState(cfg.ProjectDir)
	if err != nil {
		return err
	}
	// Persist the configured enablement into the state so a restart resumes the
	// same scope (and `orq-lite watch --status` reflects it).
	for t, on := range cfg.Enabled {
		state.Enabled[t] = on
	}
	state.IntervalSeconds = int(interval / time.Second)
	if err := state.Save(cfg.ProjectDir); err != nil {
		return err
	}

	ownUser, _ := cfg.Lister.WhoAmI(ctx)

	maxConsecutive := cfg.MaxConsecutiveErrors
	if maxConsecutive <= 0 {
		maxConsecutive = MaxConsecutiveErrorsDefault
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// Run one poll immediately (don't wait a full interval for the first pass),
	// then on every tick. A first-tick failure is surfaced like any other, but
	// does not by itself abort: a single transient blip should not kill the
	// daemon. Only sustained failure past the consecutive threshold aborts.
	consecutive := 0
	if err := tick(ctx, cfg, state, ownUser); err != nil {
		consecutive++
		logTickError(cfg, err, consecutive)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := tick(ctx, cfg, state, ownUser); err != nil {
				consecutive++
				logTickError(cfg, err, consecutive)
				if consecutive >= maxConsecutive {
					return fmt.Errorf("watch: %d consecutive tick failures (last: %w)", consecutive, err)
				}
				continue
			}
			consecutive = 0
		}
	}
}

// logTickError emits a watch_tick_error event (when a logger is configured) and
// mirrors the error to stderr so a daemon operator sees it regardless of log
// redaction.
func logTickError(cfg Config, err error, consecutive int) {
	fmt.Fprintf(os.Stderr, "watch: tick error (#%d): %v\n", consecutive, err)
	if cfg.Log != nil {
		cfg.Log.Log(eventlog.Event{
			Type: "watch_tick_error",
			Fields: map[string]any{
				"error":       err.Error(),
				"consecutive": consecutive,
			},
		})
	}
}

func tick(ctx context.Context, cfg Config, state *State, ownUser string) error {
	if cfg.Enabled[ItemIssue] {
		items, err := cfg.Lister.ListIssues(ctx, state.LastSeen[ItemIssue])
		if err != nil {
			return err
		}
		if err := processItems(ctx, cfg, state, ItemIssue, items, ownUser, func(ctx context.Context, it Item) error {
			if cfg.Trigger == nil {
				return nil
			}
			return cfg.Trigger(ctx, triggerFor(cfg, it))
		}); err != nil {
			_ = state.Save(cfg.ProjectDir)
			return err
		}
	}
	if cfg.Enabled[ItemPR] {
		items, err := cfg.Lister.ListPRs(ctx, state.LastSeen[ItemPR])
		if err != nil {
			return err
		}
		if err := processItems(ctx, cfg, state, ItemPR, items, ownUser, func(ctx context.Context, it Item) error {
			if cfg.Trigger == nil {
				return nil
			}
			return cfg.Trigger(ctx, triggerFor(cfg, it))
		}); err != nil {
			_ = state.Save(cfg.ProjectDir)
			return err
		}
	}
	return state.Save(cfg.ProjectDir)
}

func triggerFor(cfg Config, item Item) Trigger {
	return Trigger{FlowRef: cfg.FlowRefs[item.Type], Inputs: map[string]any{
		"type": item.Type, "number": item.Number, "title": item.Title,
		"body": item.Body, "author": item.Author, "updated_at": item.UpdatedAt,
	}, IdempotencyKey: fmt.Sprintf("github/%s/%s/%s", item.Type, item.Number, item.UpdatedAt.UTC().Format(time.RFC3339Nano))}
}

// processItems handles one type's batch: dedup against the processed set, skip
// orq-lite's own PRs unless ReviewOwnPRs, invoke the trigger, mark items
// processed, and advance that type's cursor to the newest item seen.
func processItems(ctx context.Context, cfg Config, state *State, typ ItemType, items []Item, ownUser string, trigger func(ctx context.Context, it Item) error) error {
	// Oldest first so the cursor advances monotonically and an item processed
	// early cannot lower a later cursor.
	sort.Slice(items, func(a, b int) bool { return items[a].UpdatedAt.Before(items[b].UpdatedAt) })
	processed := state.Processed[typ]
	if processed == nil {
		processed = map[string]bool{}
		state.Processed[typ] = processed
	}
	for _, it := range items {
		identity := itemIdentity(it)
		if processed[identity] {
			// Exact provider update was already delivered: skip. A later update
			// gets a new identity and may intentionally trigger a new run.
			continue
		}
		if typ == ItemPR && !cfg.ReviewOwnPRs && ownUser != "" && it.Author == ownUser {
			// Skip PRs orq-lite itself opened (the factory's --pr output), but
			// mark them processed so a later update does not re-trigger review.
			processed[identity] = true
			continue
		}
		if err := trigger(ctx, it); err != nil {
			// Do not cross the failed item's cursor. Already-successful items are
			// deduplicated by Processed when this page is polled again.
			return fmt.Errorf("watch: trigger %s #%s: %w", typ, it.Number, err)
		}
		processed[identity] = true
		if it.UpdatedAt.After(state.LastSeen[typ]) {
			state.LastSeen[typ] = it.UpdatedAt
		}
	}
	return nil
}

func itemIdentity(item Item) string {
	return item.Number + "@" + item.UpdatedAt.UTC().Format(time.RFC3339Nano)
}

// EnabledSummary returns a human description of the enabled watch types for
// `orq-lite watch --status`.
func (s *State) EnabledSummary() string {
	var on []string
	if s.Enabled[ItemIssue] {
		on = append(on, "issues")
	}
	if s.Enabled[ItemPR] {
		on = append(on, "prs")
	}
	if len(on) == 0 {
		return "none"
	}
	return strings.Join(on, ", ")
}
