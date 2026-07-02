package watch

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/eventlog"
)

// fakeLister serves pre-baked issues/PRs across ticks and reports a fixed own
// user. Lists are returned in the order given (the daemon sorts internally).
type fakeLister struct {
	own      string
	issues   [][]Item // per-call batches
	prs      [][]Item
	issueIdx int
	prIdx    int
}

func (f *fakeLister) WhoAmI(_ context.Context) (string, error) { return f.own, nil }

func (f *fakeLister) ListIssues(_ context.Context, _ time.Time) ([]Item, error) {
	if f.issueIdx >= len(f.issues) {
		return nil, nil
	}
	b := f.issues[f.issueIdx]
	f.issueIdx++
	return b, nil
}

func (f *fakeLister) ListPRs(_ context.Context, _ time.Time) ([]Item, error) {
	if f.prIdx >= len(f.prs) {
		return nil, nil
	}
	b := f.prs[f.prIdx]
	f.prIdx++
	return b, nil
}

func newConfig(dir string, enabled map[ItemType]bool, l lister) (Config, *State) {
	state, _ := LoadState(dir)
	return Config{
		ProjectDir: dir,
		Interval:   time.Hour, // unused; tests call Tick directly
		Enabled:    enabled,
		Lister:     l,
		Intake:     func(context.Context, string) error { return nil },
		Review:     func(context.Context, string) error { return nil },
	}, state
}

type lister = interface {
	WhoAmI(context.Context) (string, error)
	ListIssues(context.Context, time.Time) ([]Item, error)
	ListPRs(context.Context, time.Time) ([]Item, error)
}

func at(t time.Time, n, title, author string) Item {
	return Item{Type: ItemIssue, Number: n, Title: title, Author: author, UpdatedAt: t}
}

func prAt(t time.Time, n, author string) Item {
	return Item{Type: ItemPR, Number: n, Author: author, UpdatedAt: t}
}

func TestTick_AdvancesCursorAndDoesNotReprocess(t *testing.T) {
	dir := t.TempDir()
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)
	l := &fakeLister{
		own:    "me",
		issues: [][]Item{{at(t1, "10", "rep", "u"), at(t2, "11", "rep", "u")}},
	}
	var called []string
	enabled := map[ItemType]bool{ItemIssue: true}
	cfg, state := newConfig(dir, enabled, l)
	cfg.Intake = func(_ context.Context, body string) error {
		called = append(called, body)
		return nil
	}
	// Supply issue bodies.
	l.issues[0][0].Body = "b-10"
	l.issues[0][1].Body = "b-11"

	if err := Tick(context.Background(), cfg, state); err != nil {
		t.Fatal(err)
	}
	if len(called) != 2 {
		t.Fatalf("first tick called intake %d times, want 2", len(called))
	}
	if !state.LastSeen[ItemIssue].Equal(t2) {
		t.Fatalf("cursor = %v, want %v", state.LastSeen[ItemIssue], t2)
	}
	if got := len(state.Processed[ItemIssue]); got != 2 {
		t.Fatalf("processed = %d, want 2", got)
	}

	// Second tick returns no new items: nothing reprocessed, cursor unchanged.
	called = nil
	if err := Tick(context.Background(), cfg, state); err != nil {
		t.Fatal(err)
	}
	if len(called) != 0 {
		t.Fatalf("second tick reprocessed %d items", len(called))
	}
	if !state.LastSeen[ItemIssue].Equal(t2) {
		t.Fatalf("cursor drifted: %v", state.LastSeen[ItemIssue])
	}
	// State persisted to watch.json.
	raw, _ := os.ReadFile(filepath.Join(dir, ".orquestalite", "watch.json"))
	if string(raw) == "" || !contains(raw, "processed") {
		t.Fatalf("watch.json not persisted: %s", raw)
	}
}

func TestTick_OnlyIssuesEnabledDoesNotPollPRs(t *testing.T) {
	dir := t.TempDir()
	l := &fakeLister{
		own: "me",
		prs: [][]Item{{prAt(time.Now(), "3", "me")}},
		// No issues batch configured → ListIssues returns nil.
	}
	var prsPolled, issuesCalled bool
	lWrap := &wrapLister{l: l, onPRs: func() { prsPolled = true }, onIssues: func() { issuesCalled = true }}
	enabled := map[ItemType]bool{ItemIssue: true}
	cfg, state := newConfig(dir, enabled, lWrap)
	if err := Tick(context.Background(), cfg, state); err != nil {
		t.Fatal(err)
	}
	if prsPolled {
		t.Error("ListPRs must not be called when only issues are enabled")
	}
	if !issuesCalled {
		t.Error("ListIssues should have been called")
	}
}

func TestTick_NewIssueTriggersIntake_NewPRTriggersReview(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := &fakeLister{
		own: "me",
		issues: [][]Item{{
			Item{Type: ItemIssue, Number: "5", Body: "please build login", Author: "u", UpdatedAt: now},
		}},
		prs: [][]Item{{
			Item{Type: ItemPR, Number: "9", Author: "someone", UpdatedAt: now},
		}},
	}
	var intakes, reviews []string
	enabled := map[ItemType]bool{ItemIssue: true, ItemPR: true}
	cfg, state := newConfig(dir, enabled, l)
	cfg.Intake = func(_ context.Context, body string) error {
		intakes = append(intakes, body)
		return nil
	}
	cfg.Review = func(_ context.Context, pr string) error {
		reviews = append(reviews, pr)
		return nil
	}
	if err := Tick(context.Background(), cfg, state); err != nil {
		t.Fatal(err)
	}
	if len(intakes) != 1 || intakes[0] != "please build login" {
		t.Fatalf("intake = %v", intakes)
	}
	if len(reviews) != 1 || reviews[0] != "9" {
		t.Fatalf("reviews = %v", reviews)
	}
}

func TestTick_AlreadyProcessedItemSkipped(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// The same item returned again the next tick (e.g. updated) must not retrigger.
	l := &fakeLister{
		own: "me",
		issues: [][]Item{
			{Item{Type: ItemIssue, Number: "5", Body: "b", Author: "u", UpdatedAt: now}},
			{Item{Type: ItemIssue, Number: "5", Body: "b", Author: "u", UpdatedAt: now.Add(time.Second)}},
		},
	}
	var intakes int
	enabled := map[ItemType]bool{ItemIssue: true}
	cfg, state := newConfig(dir, enabled, l)
	cfg.Intake = func(context.Context, string) error { intakes++; return nil }
	if err := Tick(context.Background(), cfg, state); err != nil {
		t.Fatal(err)
	}
	if intakes != 1 {
		t.Fatalf("first tick intakes = %d, want 1", intakes)
	}
	if !state.Processed[ItemIssue]["5"] {
		t.Fatal("item 5 not marked processed")
	}
	// Second tick re-lists item 5 (it was updated past the prior cursor): skip.
	if err := Tick(context.Background(), cfg, state); err != nil {
		t.Fatal(err)
	}
	if intakes != 1 {
		t.Fatalf("already-processed item was re-processed: intakes = %d", intakes)
	}
}

func TestTick_SkipsOwnPRsUnlessReviewOwnPRs(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Two PRs: one by "me" (own), one by "alice".
	l := &fakeLister{
		own: "me",
		prs: [][]Item{{
			prAt(now, "1", "me"),
			prAt(now, "2", "alice"),
		}},
	}
	var reviews []string
	enabled := map[ItemType]bool{ItemPR: true}
	cfg, state := newConfig(dir, enabled, l)
	cfg.Review = func(_ context.Context, pr string) error {
		reviews = append(reviews, pr)
		return nil
	}
	cfg.ReviewOwnPRs = false
	if err := Tick(context.Background(), cfg, state); err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 1 || reviews[0] != "2" {
		t.Fatalf("own PR must be skipped, reviews = %v", reviews)
	}
	if !state.Processed[ItemPR]["1"] {
		t.Error("own PR should be marked processed (not retried later)")
	}

	// With ReviewOwnPRs, the own PR is reviewed too.
	l2 := &fakeLister{
		own: "me",
		prs: [][]Item{{prAt(now.Add(time.Second), "3", "me")}},
	}
	reviews = nil
	cfg2, state2 := newConfig(dir, enabled, l2)
	cfg2.ReviewOwnPRs = true
	cfg2.Review = func(_ context.Context, pr string) error {
		reviews = append(reviews, pr)
		return nil
	}
	if err := Tick(context.Background(), cfg2, state2); err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 1 || reviews[0] != "3" {
		t.Fatalf("own PR should be reviewed with flag, got %v", reviews)
	}
}

// errorLister always fails ListIssues; WhoAmI still works.
type errorLister struct{ own string }

func (e *errorLister) WhoAmI(context.Context) (string, error) { return e.own, nil }
func (e *errorLister) ListIssues(context.Context, time.Time) ([]Item, error) {
	return nil, fmt.Errorf("boom")
}
func (e *errorLister) ListPRs(context.Context, time.Time) ([]Item, error) {
	return nil, fmt.Errorf("boom")
}

// TestRun_AbortsAfterConsecutiveTickErrors verifies that Run emits a
// watch_tick_error event per failed tick and aborts after the injected
// threshold of consecutive failures (instead of swallowing them silently).
func TestRun_AbortsAfterConsecutiveTickErrors(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "run.log")
	logger, err := eventlog.OpenWithFormat(logPath, io.Discard, eventlog.FormatVerbose)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	cfg := Config{
		ProjectDir:            dir,
		Interval:              time.Millisecond,
		Enabled:               map[ItemType]bool{ItemIssue: true},
		Lister:                &errorLister{own: "me"},
		Intake:                func(context.Context, string) error { return nil },
		Log:                   logger,
		MaxConsecutiveErrors:  3,
	}
	err = Run(context.Background(), cfg)
	if err == nil {
		t.Fatal("Run should abort with an error after consecutive failures")
	}
	if !strings.Contains(err.Error(), "consecutive") {
		t.Fatalf("unexpected error: %v", err)
	}
	// At least 3 watch_tick_error events persisted to the log.
	raw, _ := os.ReadFile(logPath)
	if got := strings.Count(string(raw), "\"event\":\"watch_tick_error\""); got < 3 {
		t.Fatalf("expected >=3 watch_tick_error events, got %d\n%s", got, raw)
	}
}

// TestRun_RecoversFromTransientErrors verifies a successful tick resets the
// consecutive error counter so a single blip does not accumulate toward abort.
func TestRun_RecoversFromTransientErrors(t *testing.T) {
	dir := t.TempDir()
	logger, _ := eventlog.OpenWithFormat(filepath.Join(dir, "run.log"), io.Discard, eventlog.FormatVerbose)
	defer logger.Close()
	l := &flakyLister{own: "me", failUntil: 2}
	cfg := Config{
		ProjectDir:            dir,
		Interval:              time.Millisecond,
		Enabled:               map[ItemType]bool{ItemIssue: true},
		Lister:                l,
		Intake:                func(context.Context, string) error { return nil },
		Log:                   logger,
		MaxConsecutiveErrors:  5,
	}
	// Run for a bounded time; it must NOT abort because the transient errors
	// (two) stay below the threshold and then recover.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := Run(ctx, cfg)
	// The expected stop is the context deadline (cancelled), not a consecutive
	// abort: that proves the transient errors did not accumulate past threshold.
	if err != nil && !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("expected context deadline stop, got: %v", err)
	}
}

// flakyLister fails ListIssues until calls reach failUntil, then succeeds.
type flakyLister struct {
	own       string
	calls     int
	failUntil int
}

func (f *flakyLister) WhoAmI(context.Context) (string, error) { return f.own, nil }
func (f *flakyLister) ListIssues(context.Context, time.Time) ([]Item, error) {
	f.calls++
	if f.calls <= f.failUntil {
		return nil, fmt.Errorf("flaky %d", f.calls)
	}
	return nil, nil
}
func (f *flakyLister) ListPRs(context.Context, time.Time) ([]Item, error) {
	return nil, nil
}

func TestEnabledSummary(t *testing.T) {
	s := &State{Enabled: map[ItemType]bool{ItemIssue: true}}
	if s.EnabledSummary() != "issues" {
		t.Errorf("got %q", s.EnabledSummary())
	}
	s.Enabled[ItemPR] = true
	if s.EnabledSummary() != "issues, prs" {
		t.Errorf("got %q", s.EnabledSummary())
	}
	delete(s.Enabled, ItemIssue)
	delete(s.Enabled, ItemPR)
	if s.EnabledSummary() != "none" {
		t.Errorf("got %q", s.EnabledSummary())
	}
}

// wrapLister forwards to an inner lister, tapping the poll methods so tests
// assert whether each type was polled.
type wrapLister struct {
	l        lister
	onPRs    func()
	onIssues func()
}

func (w *wrapLister) WhoAmI(ctx context.Context) (string, error) { return w.l.WhoAmI(ctx) }
func (w *wrapLister) ListIssues(ctx context.Context, since time.Time) ([]Item, error) {
	if w.onIssues != nil {
		w.onIssues()
	}
	return w.l.ListIssues(ctx, since)
}
func (w *wrapLister) ListPRs(ctx context.Context, since time.Time) ([]Item, error) {
	if w.onPRs != nil {
		w.onPRs()
	}
	return w.l.ListPRs(ctx, since)
}

func contains(b []byte, s string) bool {
	return string(b) != "" && (indexOf(string(b), s) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
