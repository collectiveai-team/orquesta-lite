package invoke

import (
	"context"
	"errors"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/config"
	"github.com/collectiveai-team/orquesta-lite/internal/usageguard"
)

type scriptedUsageGuard struct {
	decisions  []usageguard.Decision
	checkCalls int
	invalidate int
}

func (g *scriptedUsageGuard) Check(_ context.Context, _ usageguard.Request) usageguard.Decision {
	i := g.checkCalls
	g.checkCalls++
	if i >= len(g.decisions) {
		return usageguard.Decision{Allowed: true}
	}
	return g.decisions[i]
}

func (g *scriptedUsageGuard) Invalidate(usageguard.Request) { g.invalidate++ }

func TestUsageGuardSkipsAgentAndUsesFallback(t *testing.T) {
	r := &scriptedContractRunner{steps: []contractRetryStep{{resultExists: true, body: `{"status":"ok"}`, sessionID: "fallback-session"}}}
	inv, successes := contractRetryInvoker(t, r, []config.AgentSpec{
		{Name: "limited", Provider: "claude"},
		{Name: "available", Provider: "codex"},
	}, nil)
	guard := &scriptedUsageGuard{decisions: []usageguard.Decision{
		{Allowed: false, Blocked: []string{"5h"}},
		{Allowed: true},
	}}
	inv.UsageGuard = guard

	_, err := Raw(context.Background(), inv, "coder", RoleCall{}, RunContext{TaskID: "T1"}, statusOKValidate)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.specs) != 1 || r.specs[0].Provider != "codex" {
		t.Fatalf("runner specs = %+v, want only Codex fallback", r.specs)
	}
	if guard.checkCalls != 2 || guard.invalidate != 1 {
		t.Fatalf("guard checks=%d invalidations=%d, want 2 and 1", guard.checkCalls, guard.invalidate)
	}
	if len(*successes) != 1 || (*successes)[0] != "available" {
		t.Fatalf("successes = %v", *successes)
	}
}

func TestUsageGuardRechecksBeforeCorrectiveRetry(t *testing.T) {
	r := &scriptedContractRunner{steps: []contractRetryStep{{resultExists: true, body: `{"status":"bad"}`, sessionID: "initial"}}}
	inv, _ := contractRetryInvoker(t, r, []config.AgentSpec{{Name: "agent1", Provider: "claude"}}, nil)
	guard := &scriptedUsageGuard{decisions: []usageguard.Decision{
		{Allowed: true},
		{Allowed: false, Blocked: []string{"5h"}},
	}}
	inv.UsageGuard = guard

	_, err := Raw(context.Background(), inv, "coder", RoleCall{}, RunContext{TaskID: "T1"}, statusOKValidate)
	if !errors.Is(err, ErrUsageThreshold) {
		t.Fatalf("err = %v, want ErrUsageThreshold", err)
	}
	if len(r.specs) != 1 {
		t.Fatalf("runner calls = %d, want initial call only", len(r.specs))
	}
	if guard.checkCalls != 2 || guard.invalidate != 1 {
		t.Fatalf("guard checks=%d invalidations=%d, want 2 and 1", guard.checkCalls, guard.invalidate)
	}
}
