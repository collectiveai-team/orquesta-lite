package usageguard

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/config"
)

type sequencedReader struct {
	results []func() (Snapshot, error)
	calls   int
}

func (r *sequencedReader) Fetch(context.Context, []string) (Snapshot, error) {
	i := r.calls
	r.calls++
	if i >= len(r.results) {
		return nil, errors.New("sequencedReader: exhausted")
	}
	return r.results[i]()
}

func testGuard(t *testing.T, cfg config.UsageGuard, reader Reader, now func() time.Time) *Guard {
	t.Helper()
	guard, err := New(cfg, map[string]Reader{"claude": reader})
	if err != nil {
		t.Fatal(err)
	}
	if now != nil {
		guard.now = now
	}
	return guard
}

func budget(limit float64) config.UsageGuard {
	return config.UsageGuard{Providers: map[string]config.UsageProviderBudget{
		"claude": {MaxUsedPercent: map[string]float64{WindowFiveHour: limit}},
	}}
}

// A throttled lookup must not abandon the provider: the previous reading is a
// better answer than declaring the subscription unreadable.
func TestCheckFallsBackToLastGoodReadingWhenFetchFails(t *testing.T) {
	reader := &sequencedReader{results: []func() (Snapshot, error){
		func() (Snapshot, error) { return Snapshot{WindowFiveHour: {UsedPercent: 10}}, nil },
		func() (Snapshot, error) { return nil, ErrProviderRateLimited },
	}}
	guard := testGuard(t, budget(80), reader, nil)
	request := Request{Provider: "claude"}

	if d := guard.Check(context.Background(), request); !d.Allowed || d.Unavailable {
		t.Fatalf("first check = %+v, want a plain allow", d)
	}
	guard.Invalidate(request) // what the invoker does after every agent run

	second := guard.Check(context.Background(), request)
	if reader.calls != 2 {
		t.Fatalf("reader calls = %d, want 2 (Invalidate should force a refetch)", reader.calls)
	}
	if !second.Allowed || second.Unavailable {
		t.Fatalf("second check = %+v, want the previous reading to be reused", second)
	}
}

// The retained reading is not a licence to trust it forever - it ages out on
// the same rule as any other measurement.
func TestLastGoodReadingStillExpiresByAge(t *testing.T) {
	clock := time.Now()
	reader := &sequencedReader{results: []func() (Snapshot, error){
		func() (Snapshot, error) { return Snapshot{WindowFiveHour: {UsedPercent: 10}}, nil },
		func() (Snapshot, error) { return nil, ErrProviderRateLimited },
	}}
	cfg := budget(80)
	cfg.MaxReadingAgeSeconds = 600
	guard := testGuard(t, cfg, reader, func() time.Time { return clock })
	request := Request{Provider: "claude"}

	guard.Check(context.Background(), request)
	guard.Invalidate(request)
	clock = clock.Add(20 * time.Minute) // past max_reading_age_seconds

	decision := guard.Check(context.Background(), request)
	if !decision.Unavailable || decision.Allowed {
		t.Fatalf("decision = %+v, want unavailable once the retained reading aged out", decision)
	}
	if len(decision.Stale) != 1 || decision.Stale[0] != WindowFiveHour {
		t.Fatalf("stale = %v, want [5h]", decision.Stale)
	}
}

// A reader that knows its measurement is old (a local cache written hours ago)
// must not have that reading enforced.
func TestCheckRejectsAReadingObservedTooLongAgo(t *testing.T) {
	now := time.Now()
	reader := &sequencedReader{results: []func() (Snapshot, error){
		func() (Snapshot, error) {
			return Snapshot{WindowFiveHour: {UsedPercent: 100, ObservedAt: now.Add(-26 * time.Hour)}}, nil
		},
	}}
	guard := testGuard(t, budget(80), reader, func() time.Time { return now })

	decision := guard.Check(context.Background(), Request{Provider: "claude"})
	if !decision.Unavailable {
		t.Fatalf("decision = %+v, want unavailable rather than a 26h-old 100%% reading", decision)
	}
	if len(decision.Blocked) != 0 {
		t.Fatalf("blocked = %v, want nothing enforced from a stale reading", decision.Blocked)
	}
}

// A 429 must be distinguishable so the caller can skip the equally-throttled
// interactive fallback instead of spending its timeout.
func TestClaudeReaderSurfacesRateLimitWithoutTryingTheCLI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error"}}`))
	}))
	defer server.Close()

	cliCalls := 0
	reader := ClaudeReader{
		HTTPClient:  server.Client(),
		Credentials: func(context.Context, []string) (string, error) { return "token", nil },
		CLI: func(context.Context, []string) (Snapshot, error) {
			cliCalls++
			return Snapshot{WindowFiveHour: {UsedPercent: 5}}, nil
		},
	}
	reader.usageURL = server.URL

	_, err := reader.Fetch(context.Background(), nil)
	if !errors.Is(err, ErrProviderRateLimited) {
		t.Fatalf("err = %v, want ErrProviderRateLimited", err)
	}
	if cliCalls != 0 {
		t.Fatalf("CLI fallback ran %d times; it queries the same throttled endpoint", cliCalls)
	}
}
