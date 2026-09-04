package invoke

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/config"
	"github.com/collectiveai-team/orquesta-lite/internal/usageguard"
)

// stubUsageReader stands in for the live provider endpoint. It is the only
// faked seam: everything downstream of it (Guard, thresholds, the fallback
// chain) is the real implementation, so these tests exercise the actual
// config -> guard -> provider-switch path rather than a scripted Checker.
type stubUsageReader struct {
	snapshot usageguard.Snapshot
	calls    int
}

func (r *stubUsageReader) Fetch(context.Context, []string) (usageguard.Snapshot, error) {
	r.calls++
	return r.snapshot, nil
}

// realGuard wires the production Guard to stub readers using the same
// config shape a team.json would supply.
func realGuard(t *testing.T, maxUsed map[string]map[string]float64, snapshots map[string]usageguard.Snapshot) (*usageguard.Guard, map[string]*stubUsageReader) {
	t.Helper()
	providers := map[string]config.UsageProviderBudget{}
	for provider, windows := range maxUsed {
		providers[provider] = config.UsageProviderBudget{MaxUsedPercent: windows}
	}
	readers := map[string]usageguard.Reader{}
	stubs := map[string]*stubUsageReader{}
	for provider, snapshot := range snapshots {
		stub := &stubUsageReader{snapshot: snapshot}
		stubs[provider] = stub
		readers[provider] = stub
	}
	guard, err := usageguard.New(config.UsageGuard{
		CacheTTLSeconds: 30,
		OnUnavailable:   "fallback",
		Providers:       providers,
	}, readers)
	if err != nil {
		t.Fatal(err)
	}
	if !guard.Enabled() {
		t.Fatal("guard should be enabled with configured providers")
	}
	return guard, stubs
}

// The headline case: the Claude 5h window sits exactly on the configured
// limit with ~1% headroom left on the subscription, and the role must switch
// to the Codex agent and still complete the task.
func TestUsageGuardSwitchesProviderAt5hExactLimit(t *testing.T) {
	resetsAt := time.Now().Add(37 * time.Minute).UTC().Truncate(time.Second)
	r := &scriptedContractRunner{steps: []contractRetryStep{
		{resultExists: true, body: `{"status":"ok"}`, sessionID: "codex-session"},
	}}
	inv, successes := contractRetryInvoker(t, r, []config.AgentSpec{
		{Name: "claude-primary", Provider: "claude"},
		{Name: "codex-backup", Provider: "codex"},
	}, nil)
	guard, stubs := realGuard(t,
		map[string]map[string]float64{
			"claude": {usageguard.WindowFiveHour: 99, usageguard.WindowSevenDay: 90},
			"codex":  {usageguard.WindowFiveHour: 80},
		},
		map[string]usageguard.Snapshot{
			// Exactly at the limit: 99% used, 1% of the 5h window left.
			"claude": {
				usageguard.WindowFiveHour: {UsedPercent: 99, ResetsAt: resetsAt},
				usageguard.WindowSevenDay: {UsedPercent: 41},
			},
			"codex": {usageguard.WindowFiveHour: {UsedPercent: 12}},
		})
	inv.UsageGuard = guard

	_, err := Raw(context.Background(), inv, "coder", RoleCall{}, RunContext{TaskID: "T-switch"}, statusOKValidate)
	if err != nil {
		t.Fatalf("task should complete on the fallback provider, got %v", err)
	}
	if len(r.specs) != 1 {
		t.Fatalf("runner invocations = %d, want exactly 1 (Claude must never spawn)", len(r.specs))
	}
	if r.specs[0].Provider != "codex" {
		t.Fatalf("executed provider = %q, want codex", r.specs[0].Provider)
	}
	if len(*successes) != 1 || (*successes)[0] != "codex-backup" {
		t.Fatalf("successes = %v, want [codex-backup]", *successes)
	}
	if stubs["claude"].calls != 1 || stubs["codex"].calls != 1 {
		t.Fatalf("reader calls claude=%d codex=%d, want 1 each", stubs["claude"].calls, stubs["codex"].calls)
	}
}

// The boundary must discriminate: one tenth of a percent below the limit is
// still allowed, so the switch above is caused by the threshold and not by
// the guard blocking unconditionally.
func TestUsageGuardRunsPrimaryJustUnder5hLimit(t *testing.T) {
	r := &scriptedContractRunner{steps: []contractRetryStep{
		{resultExists: true, body: `{"status":"ok"}`, sessionID: "claude-session"},
	}}
	inv, successes := contractRetryInvoker(t, r, []config.AgentSpec{
		{Name: "claude-primary", Provider: "claude"},
		{Name: "codex-backup", Provider: "codex"},
	}, nil)
	guard, _ := realGuard(t,
		map[string]map[string]float64{"claude": {usageguard.WindowFiveHour: 99}},
		map[string]usageguard.Snapshot{"claude": {usageguard.WindowFiveHour: {UsedPercent: 98.9}}})
	inv.UsageGuard = guard

	_, err := Raw(context.Background(), inv, "coder", RoleCall{}, RunContext{TaskID: "T-under"}, statusOKValidate)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.specs) != 1 || r.specs[0].Provider != "claude" {
		t.Fatalf("runner specs = %+v, want a single Claude run", r.specs)
	}
	if len(*successes) != 1 || (*successes)[0] != "claude-primary" {
		t.Fatalf("successes = %v, want [claude-primary]", *successes)
	}
}

// With both subscriptions at their limit there is nowhere to switch to: the
// role must fail with the threshold error and spawn no agent at all.
func TestUsageGuardBlocksWhenBothProvidersAtLimit(t *testing.T) {
	r := &scriptedContractRunner{}
	inv, _ := contractRetryInvoker(t, r, []config.AgentSpec{
		{Name: "claude-primary", Provider: "claude"},
		{Name: "codex-backup", Provider: "codex"},
	}, nil)
	guard, _ := realGuard(t,
		map[string]map[string]float64{
			"claude": {usageguard.WindowFiveHour: 99},
			"codex":  {usageguard.WindowFiveHour: 80},
		},
		map[string]usageguard.Snapshot{
			"claude": {usageguard.WindowFiveHour: {UsedPercent: 99}},
			"codex":  {usageguard.WindowFiveHour: {UsedPercent: 80}},
		})
	inv.UsageGuard = guard

	_, err := Raw(context.Background(), inv, "coder", RoleCall{}, RunContext{TaskID: "T-blocked"}, statusOKValidate)
	if !errors.Is(err, ErrUsageThreshold) {
		t.Fatalf("err = %v, want ErrUsageThreshold", err)
	}
	if len(r.specs) != 0 {
		t.Fatalf("runner invocations = %d, want none", len(r.specs))
	}
}
