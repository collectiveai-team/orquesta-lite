package usageguard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The session log uses snake_case field names, unlike the app-server RPC.
const codexRolloutLine = `{"timestamp":"2026-08-25T18:35:20.640Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"limit_id":"codex","primary":{"used_percent":46.0,"window_minutes":300,"resets_at":1787686641},"secondary":{"used_percent":7.0,"window_minutes":10080,"resets_at":1788273441},"plan_type":"plus"}}}`

func TestParseCodexRolloutRecordCarriesItsObservationTime(t *testing.T) {
	snapshot, err := parseCodexRolloutRecord([]byte(codexRolloutLine))
	if err != nil {
		t.Fatal(err)
	}
	five, ok := snapshot[WindowFiveHour]
	if !ok || five.UsedPercent != 46 {
		t.Fatalf("5h = %+v, want 46%% used", five)
	}
	if seven := snapshot[WindowSevenDay]; seven.UsedPercent != 7 {
		t.Fatalf("7d = %+v, want 7%% used", seven)
	}
	want := time.Date(2026, 8, 25, 18, 35, 20, 640000000, time.UTC)
	if !five.ObservedAt.Equal(want) {
		t.Fatalf("ObservedAt = %s, want the record timestamp %s", five.ObservedAt, want)
	}
	if five.ResetsAt.Unix() != 1787686641 {
		t.Fatalf("ResetsAt = %s", five.ResetsAt)
	}
}

// Later records supersede earlier ones within a session.
func TestCodexLocalReaderUsesTheNewestRecord(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions", "2026", "08", "25")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := `{"timestamp":"2026-08-25T10:00:00.000Z","payload":{"rate_limits":{"primary":{"used_percent":5,"window_minutes":300,"resets_at":1}}}}`
	body := stale + "\n" + `{"type":"other"}` + "\n" + codexRolloutLine + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-08-25T09-00-00-abc.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	snapshot, err := CodexLocalReader{Root: root}.Fetch(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot[WindowFiveHour].UsedPercent != 46 {
		t.Fatalf("5h = %v, want the last record (46), not the first (5)", snapshot[WindowFiveHour].UsedPercent)
	}
}

func TestCodexLocalReaderReportsMissingSessions(t *testing.T) {
	if _, err := (CodexLocalReader{Root: t.TempDir()}).Fetch(context.Background(), nil); err == nil {
		t.Fatal("an empty Codex home should not yield a reading")
	}
}

// Chain must stop at a throttled source rather than spending the alternatives.
func TestChainStopsAtARateLimitedSource(t *testing.T) {
	second := 0
	chain := Chain{
		readerFunc(func() (Snapshot, error) { return nil, ErrProviderRateLimited }),
		readerFunc(func() (Snapshot, error) { second++; return Snapshot{WindowFiveHour: {}}, nil }),
	}
	_, err := chain.Fetch(context.Background(), nil)
	if !errors.Is(err, ErrProviderRateLimited) {
		t.Fatalf("err = %v, want ErrProviderRateLimited", err)
	}
	if second != 0 {
		t.Fatalf("second source ran %d times, want 0", second)
	}
}

func TestChainFallsThroughOnOrdinaryFailure(t *testing.T) {
	chain := Chain{
		readerFunc(func() (Snapshot, error) { return nil, errors.New("app-server unavailable") }),
		readerFunc(func() (Snapshot, error) { return Snapshot{WindowFiveHour: {UsedPercent: 12}}, nil }),
	}
	snapshot, err := chain.Fetch(context.Background(), nil)
	if err != nil || snapshot[WindowFiveHour].UsedPercent != 12 {
		t.Fatalf("snapshot = %+v, err = %v", snapshot, err)
	}
}

type readerFunc func() (Snapshot, error)

func (f readerFunc) Fetch(context.Context, []string) (Snapshot, error) { return f() }

// Opt-in: reads the operator's real Codex session files.
func TestLiveCodexLocalReader(t *testing.T) {
	if os.Getenv("ORQ_LIVE_USAGE_PROBE") != "1" {
		t.Skip("set ORQ_LIVE_USAGE_PROBE=1 to read the real Codex session files")
	}
	snapshot, err := CodexLocalReader{}.Fetch(context.Background(), nil)
	if err != nil {
		t.Skipf("no local Codex usage available: %v", err)
	}
	for window, value := range snapshot {
		t.Logf("LOCAL codex %s: used=%.1f%% observed=%s (age %s) resets=%s",
			window, float64(value.UsedPercent), value.ObservedAt.Format(time.RFC3339),
			time.Since(value.ObservedAt).Truncate(time.Minute), value.ResetsAt.Format(time.RFC3339))
	}
	if len(snapshot) == 0 {
		t.Fatal("empty snapshot")
	}
}
