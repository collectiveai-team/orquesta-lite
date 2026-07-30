package builtin

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/activity"
	"github.com/lionelchamorro/orquestalite/internal/agenthealth"
	"github.com/lionelchamorro/orquestalite/internal/config"
	"github.com/lionelchamorro/orquestalite/internal/eventlog"
	"github.com/lionelchamorro/orquestalite/internal/fallback"
	"github.com/lionelchamorro/orquestalite/internal/invoke"
	"github.com/lionelchamorro/orquestalite/internal/providers"
	"github.com/lionelchamorro/orquestalite/internal/runner"
	"github.com/lionelchamorro/orquestalite/internal/sessions"
)

// usageRunner is a stub agent CLI that reports token usage the way every real
// provider adapter does: an EventUsage carrying input_tokens/output_tokens.
type usageRunner struct {
	input  int
	output int
}

func (r *usageRunner) Run(_ context.Context, spec runner.Spec) (*runner.Result, error) {
	if err := os.MkdirAll(filepath.Dir(spec.ResultPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(spec.ResultPath, []byte(`{"approved":true}`), 0o644); err != nil {
		return nil, err
	}
	return &runner.Result{
		ResultExists: true,
		ExitCode:     0,
		Duration:     time.Second,
		SessionID:    "s1",
		Events: []providers.Event{{
			Type:  providers.EventUsage,
			Usage: map[string]int{"input_tokens": r.input, "output_tokens": r.output},
		}},
	}, nil
}

func usageTestExecutor(t *testing.T, fake *usageRunner) *AgentExecutor {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "coder.md"), []byte("implement the ticket"), 0o644); err != nil {
		t.Fatal(err)
	}
	logger, err := eventlog.Open(filepath.Join(dir, ".orquestalite", "run.log"), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	inv := &invoke.RoleInvoker{
		Specs: map[string]config.RoleSpec{
			"coder": {
				Agents:     []config.AgentSpec{{Name: "agentA", Provider: "claude", Model: "claude-sonnet-4-5", Cmd: []string{"fake", "{{PROMPT}}"}}},
				PromptPath: "prompts/coder.md",
				ResultPath: ".orquestalite/results/coder.json",
				Timeout:    time.Minute,
			},
		},
		Dir:      dir,
		Fallback: fallback.NewCaller(fallback.Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: time.Millisecond}),
		Log:      logger,
		Health:   agenthealth.New(2),
		MemPath:  filepath.Join(dir, ".orquestalite", "memory.md"),
		Runner:   fake,
		Sessions: sessions.Load(dir),
	}
	return &AgentExecutor{Invoker: inv, Validate: func(string, []byte) error { return nil }}
}

// TestAgentInvokeReportsCostUSD pins the only runaway brake that
// policies/development@3.json still has teeth for.
//
// development@3 sets maxAttempts and maxAgentAttempts to 0 ("no limit") and
// replaces them with maxCostUSD: 250. The scheduler enforces that cap against
// RunUsage.CostUSD, which is SUM(attempts.cost_usd), which is written from
// activity.Result.CostUSD. No executor in the tree ever sets that field, so the
// sum is permanently 0 and the cap can never fire — leaving maxDurationSeconds
// as the single real brake on a governed run.
//
// The data is already at hand: the stub runner below reports the same
// EventUsage tokens every provider adapter emits, invoke.usageTotals already
// aggregates them, and cost.EstimateUSD already prices them (that is how
// `orq-lite` reports run cost). Only the hand-off into activity.Result is
// missing.
func TestAgentInvokeReportsCostUSD(t *testing.T) {
	fake := &usageRunner{input: 200_000, output: 20_000}
	executor := usageTestExecutor(t, fake)
	result, err := executor.Execute(context.Background(), activity.Request{
		StepID:  "implement_ticket",
		Attempt: 1,
		Inputs:  []byte(`{"role":"coder","outputSchema":"schema:x@1"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CostUSD <= 0 {
		t.Fatalf("agent.invoke reported CostUSD=%v for an invocation that consumed %d input and %d output tokens; the policy's maxCostUSD budget can never fire", result.CostUSD, fake.input, fake.output)
	}
}
