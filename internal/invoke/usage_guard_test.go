package invoke

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/config"
	"github.com/collectiveai-team/orquesta-lite/internal/eventlog"
	"github.com/collectiveai-team/orquesta-lite/internal/usageguard"
)

type scriptedUsageGuard struct {
	decisions  []usageguard.Decision
	checkCalls int
	invalidate int
	requests   []usageguard.Request
}

func (g *scriptedUsageGuard) Check(_ context.Context, request usageguard.Request) usageguard.Decision {
	i := g.checkCalls
	g.checkCalls++
	g.requests = append(g.requests, request)
	if i >= len(g.decisions) {
		return usageguard.Decision{Allowed: true}
	}
	return g.decisions[i]
}

func TestUsageGuardProtectsCustomCommandAgent(t *testing.T) {
	r := &scriptedContractRunner{}
	inv, _ := contractRetryInvoker(t, r, []config.AgentSpec{{
		Name: "wrapped-claude", Cmd: []string{"wrapper", "{{PROMPT}}"}, UsageProvider: "claude",
	}}, nil)
	guard := &scriptedUsageGuard{decisions: []usageguard.Decision{{Allowed: false, Blocked: []string{"7d"}}}}
	inv.UsageGuard = guard

	_, err := Raw(context.Background(), inv, "coder", RoleCall{}, RunContext{TaskID: "T1"}, statusOKValidate)
	if !errors.Is(err, ErrUsageThreshold) {
		t.Fatalf("err = %v, want ErrUsageThreshold", err)
	}
	if len(r.specs) != 0 || len(guard.requests) != 1 || guard.requests[0].Provider != "claude" {
		t.Fatalf("runner calls=%d guard requests=%+v", len(r.specs), guard.requests)
	}
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
	if err == nil || errors.Is(err, ErrUsageThreshold) {
		t.Fatalf("err = %v, want mixed execution/usage failure, not ErrUsageThreshold", err)
	}
	if len(r.specs) != 1 {
		t.Fatalf("runner calls = %d, want initial call only", len(r.specs))
	}
	if guard.checkCalls != 2 || guard.invalidate != 1 {
		t.Fatalf("guard checks=%d invalidations=%d, want 2 and 1", guard.checkCalls, guard.invalidate)
	}
}

func TestUsageGuardReturnsThresholdErrorOnlyWhenNoAgentExecuted(t *testing.T) {
	r := &scriptedContractRunner{}
	inv, _ := contractRetryInvoker(t, r, []config.AgentSpec{
		{Name: "claude", Provider: "claude"},
		{Name: "codex", Provider: "codex"},
	}, nil)
	inv.UsageGuard = &scriptedUsageGuard{decisions: []usageguard.Decision{
		{Allowed: false, Blocked: []string{"5h"}},
		{Allowed: false, Unavailable: true, Missing: []string{"5h", "7d"}, Err: errors.New("no windows")},
	}}

	_, err := Raw(context.Background(), inv, "coder", RoleCall{}, RunContext{TaskID: "T1"}, statusOKValidate)
	if !errors.Is(err, ErrUsageThreshold) {
		t.Fatalf("err = %v, want ErrUsageThreshold", err)
	}
	if len(r.specs) != 0 {
		t.Fatalf("runner calls = %d, want none", len(r.specs))
	}
}

func TestUsageGuardLogsUnavailableAllowDecision(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "run.log")
	logger, err := eventlog.OpenWithFormat(logPath, io.Discard, eventlog.FormatVerbose)
	if err != nil {
		t.Fatal(err)
	}
	guardErr := errors.New("usage endpoint unavailable")
	inv := &RoleInvoker{Log: logger, UsageGuard: &scriptedUsageGuard{decisions: []usageguard.Decision{{
		Allowed: true, Unavailable: true, Missing: []string{"5h"}, Err: guardErr,
	}}}}
	decision := inv.usageBlocked(context.Background(), "coder", "claude", usageguard.Request{Provider: "claude"})
	if !decision.Allowed {
		t.Fatal("unavailable allow policy should permit execution")
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, `"event":"provider_usage_unavailable"`) || !strings.Contains(text, `"action":"allow"`) {
		t.Fatalf("log = %s", text)
	}
}
