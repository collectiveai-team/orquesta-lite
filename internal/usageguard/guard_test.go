package usageguard

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/config"
)

type fakeReader struct {
	snapshot Snapshot
	err      error
	calls    int
}

func (r *fakeReader) Fetch(context.Context, []string) (Snapshot, error) {
	r.calls++
	return r.snapshot, r.err
}

func TestGuardBlocksConfiguredWindowAndInvalidatesCache(t *testing.T) {
	reader := &fakeReader{snapshot: Snapshot{
		WindowFiveHour: {UsedPercent: 81, ResetsAt: time.Date(2026, 8, 24, 17, 0, 0, 0, time.UTC)},
		WindowSevenDay: {UsedPercent: 20},
	}}
	guard, err := New(config.UsageGuard{CacheTTLSeconds: 60, Providers: map[string]config.UsageProviderBudget{
		"codex": {MaxUsedPercent: map[string]float64{WindowFiveHour: 80, WindowSevenDay: 70}},
	}}, map[string]Reader{"codex": reader})
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Provider: "codex", Env: []string{"CODEX_HOME=/tmp/a"}}
	first := guard.Check(context.Background(), req)
	if first.Allowed || strings.Join(first.Blocked, ",") != WindowFiveHour {
		t.Fatalf("first decision = %+v, want 5h blocked", first)
	}
	if first.ResetsAt.IsZero() {
		t.Fatal("blocked decision did not preserve reset time")
	}
	_ = guard.Check(context.Background(), req)
	if reader.calls != 1 {
		t.Fatalf("reader calls = %d, want cached single call", reader.calls)
	}
	guard.Invalidate(req)
	_ = guard.Check(context.Background(), req)
	if reader.calls != 2 {
		t.Fatalf("reader calls = %d after invalidate, want 2", reader.calls)
	}
}

func TestGuardUnavailableFollowsPolicy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		policy  string
		allowed bool
	}{
		{name: "safe fallback default", allowed: false},
		{name: "explicit allow", policy: "allow", allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard, err := New(config.UsageGuard{OnUnavailable: tc.policy, Providers: map[string]config.UsageProviderBudget{
				"claude": {MaxUsedPercent: map[string]float64{WindowFiveHour: 80}},
			}}, map[string]Reader{"claude": &fakeReader{err: errors.New("not signed in")}})
			if err != nil {
				t.Fatal(err)
			}
			got := guard.Check(context.Background(), Request{Provider: "claude"})
			if got.Allowed != tc.allowed || !got.Unavailable || got.Err == nil {
				t.Fatalf("decision = %+v", got)
			}
		})
	}
}

func TestParseProviderUsage(t *testing.T) {
	codex, err := parseCodexRateLimits([]byte(`{"rateLimits":{"primary":{"usedPercent":41,"windowDurationMins":300,"resetsAt":1787590800},"secondary":{"usedPercent":72,"windowDurationMins":10080,"resetsAt":1788195600}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if codex[WindowFiveHour].UsedPercent != 41 || codex[WindowSevenDay].UsedPercent != 72 {
		t.Fatalf("Codex snapshot = %+v", codex)
	}
	claude, err := parseClaudeUsage(strings.NewReader(`{"five_hour":{"utilization":31,"resets_at":"2026-08-24T17:00:00Z"},"seven_day":{"utilization":65,"resets_at":"2026-08-30T00:00:00Z"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if claude[WindowFiveHour].UsedPercent != 31 || claude[WindowSevenDay].UsedPercent != 65 {
		t.Fatalf("Claude snapshot = %+v", claude)
	}
}

func TestCodexReaderAppServerProtocol(t *testing.T) {
	reader := CodexReader{Command: func(ctx context.Context, _ []string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestCodexReaderHelperProcess", "--")
		cmd.Env = append(os.Environ(), "GO_WANT_CODEX_APP_SERVER=1")
		return cmd
	}}
	snapshot, err := reader.Fetch(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot[WindowFiveHour].UsedPercent != 40 || snapshot[WindowSevenDay].UsedPercent != 70 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestCodexReaderHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_CODEX_APP_SERVER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request rpcRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		switch request.Method {
		case "initialize":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{}})
		case "account/rateLimits/read":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{
				"rateLimits": map[string]any{
					"primary":   map[string]any{"usedPercent": 40, "windowDurationMins": 300, "resetsAt": 1787590800},
					"secondary": map[string]any{"usedPercent": 70, "windowDurationMins": 10080, "resetsAt": 1788195600},
				},
			}})
			return
		}
	}
	os.Exit(2)
}
