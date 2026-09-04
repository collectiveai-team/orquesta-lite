package invoke

import (
	"context"
	"errors"
	"math"
	"os"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/config"
	"github.com/collectiveai-team/orquesta-lite/internal/usageguard"
)

// These probes talk to the real provider endpoint with the operator's own
// credentials, so they are opt-in via ORQ_LIVE_USAGE_PROBE=1 and never run in
// CI. They derive their thresholds from the live reading itself, so they stay
// meaningful as actual consumption moves during the day.
func liveClaudeFiveHour(t *testing.T) usageguard.Window {
	t.Helper()
	if os.Getenv("ORQ_LIVE_USAGE_PROBE") != "1" {
		t.Skip("set ORQ_LIVE_USAGE_PROBE=1 to run live provider probes")
	}
	snapshot, err := usageguard.ClaudeReader{}.Fetch(context.Background(), nil)
	if err != nil {
		t.Fatalf("live Claude usage read failed: %v", err)
	}
	window, ok := snapshot[usageguard.WindowFiveHour]
	if !ok {
		t.Fatalf("live snapshot has no 5h window: %+v", snapshot)
	}
	t.Logf("LIVE claude 5h: used=%.4f%% resets=%s", window.UsedPercent, window.ResetsAt)
	return window
}

func liveGuard(t *testing.T, limit float64) *usageguard.Guard {
	t.Helper()
	guard, err := usageguard.New(config.UsageGuard{
		OnUnavailable: "fallback",
		// Only Claude is budgeted, so the Codex fallback is unconfigured and
		// always allowed - isolating the switch to the Claude threshold.
		Providers: map[string]config.UsageProviderBudget{
			"claude": {MaxUsedPercent: map[string]float64{usageguard.WindowFiveHour: limit}},
		},
	}, nil) // nil readers => the real DefaultReaders()
	if err != nil {
		t.Fatal(err)
	}
	return guard
}

// Threshold pinned just BELOW the live reading: the real subscription is over
// budget, so the role must skip Claude and complete on Codex.
func TestLiveUsageGuardSwitchesWhenThresholdJustBelowCurrent(t *testing.T) {
	live := liveClaudeFiveHour(t)
	limit := float64(live.UsedPercent) - 0.5
	if limit <= 0 {
		limit = math.Nextafter(float64(live.UsedPercent), 0)
	}
	if limit <= 0 {
		t.Skip("live 5h usage is 0%; cannot pin a threshold below it")
	}
	t.Logf("threshold %.4f%% vs live %.4f%% -> expect switch", limit, live.UsedPercent)

	r := &scriptedContractRunner{steps: []contractRetryStep{
		{resultExists: true, body: `{"status":"ok"}`, sessionID: "codex-session"},
	}}
	inv, successes := contractRetryInvoker(t, r, []config.AgentSpec{
		{Name: "claude-primary", Provider: "claude"},
		{Name: "codex-backup", Provider: "codex"},
	}, nil)
	inv.UsageGuard = liveGuard(t, limit)

	if _, err := Raw(context.Background(), inv, "coder", RoleCall{}, RunContext{TaskID: "T-live-switch"}, statusOKValidate); err != nil {
		t.Fatalf("task should complete on the fallback provider: %v", err)
	}
	if len(r.specs) != 1 || r.specs[0].Provider != "codex" {
		t.Fatalf("runner specs = %+v, want a single Codex run", r.specs)
	}
	if len(*successes) != 1 || (*successes)[0] != "codex-backup" {
		t.Fatalf("successes = %v, want [codex-backup]", *successes)
	}
}

// Threshold pinned at EXACTLY the live reading. Check is at-or-above, so this
// must still block - this is the boundary case against real data.
func TestLiveUsageGuardBlocksAtExactlyCurrentUsage(t *testing.T) {
	live := liveClaudeFiveHour(t)
	if live.UsedPercent <= 0 {
		t.Skip("live 5h usage is 0%; max_used_percent must be > 0")
	}
	t.Logf("threshold %.4f%% == live %.4f%% -> expect block", live.UsedPercent, live.UsedPercent)

	r := &scriptedContractRunner{}
	inv, _ := contractRetryInvoker(t, r, []config.AgentSpec{{Name: "claude-only", Provider: "claude"}}, nil)
	inv.UsageGuard = liveGuard(t, float64(live.UsedPercent))

	_, err := Raw(context.Background(), inv, "coder", RoleCall{}, RunContext{TaskID: "T-live-exact"}, statusOKValidate)
	if !errors.Is(err, ErrUsageThreshold) {
		t.Fatalf("err = %v, want ErrUsageThreshold at exactly the live reading", err)
	}
	if len(r.specs) != 0 {
		t.Fatalf("runner invocations = %d, want none", len(r.specs))
	}
}

// Threshold pinned just ABOVE the live reading: still in budget, Claude runs.
// Without this the two tests above would pass even if the guard blocked
// unconditionally.
func TestLiveUsageGuardAllowsWhenThresholdJustAboveCurrent(t *testing.T) {
	live := liveClaudeFiveHour(t)
	limit := float64(live.UsedPercent) + 0.5
	if limit > 100 {
		t.Skip("live 5h usage is at 100%; cannot pin a threshold above it")
	}
	t.Logf("threshold %.4f%% vs live %.4f%% -> expect Claude to run", limit, live.UsedPercent)

	r := &scriptedContractRunner{steps: []contractRetryStep{
		{resultExists: true, body: `{"status":"ok"}`, sessionID: "claude-session"},
	}}
	inv, successes := contractRetryInvoker(t, r, []config.AgentSpec{
		{Name: "claude-primary", Provider: "claude"},
		{Name: "codex-backup", Provider: "codex"},
	}, nil)
	inv.UsageGuard = liveGuard(t, limit)

	if _, err := Raw(context.Background(), inv, "coder", RoleCall{}, RunContext{TaskID: "T-live-allow"}, statusOKValidate); err != nil {
		t.Fatal(err)
	}
	if len(r.specs) != 1 || r.specs[0].Provider != "claude" {
		t.Fatalf("runner specs = %+v, want a single Claude run", r.specs)
	}
	if len(*successes) != 1 || (*successes)[0] != "claude-primary" {
		t.Fatalf("successes = %v, want [claude-primary]", *successes)
	}
}
