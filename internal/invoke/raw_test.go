package invoke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/config"
	"github.com/collectiveai-team/orquesta-lite/internal/fallback"
	"github.com/collectiveai-team/orquesta-lite/internal/runner"
)

type sequenceResultRunner struct{ calls int }

func (r *sequenceResultRunner) Run(_ context.Context, spec runner.Spec) (*runner.Result, error) {
	r.calls++
	if err := os.MkdirAll(filepath.Dir(spec.ResultPath), 0o755); err != nil {
		return nil, err
	}
	raw := []byte(`{"status":42}`)
	if r.calls > 1 {
		raw = []byte(`{"status":"ok"}`)
	}
	if err := os.WriteFile(spec.ResultPath, raw, 0o644); err != nil {
		return nil, err
	}
	return &runner.Result{ResultExists: true}, nil
}

type timeoutResultRunner struct{ calls int }

func (r *timeoutResultRunner) Run(_ context.Context, _ runner.Spec) (*runner.Result, error) {
	r.calls++
	return &runner.Result{TimedOut: true, ResultExists: false, ExitCode: -1}, nil
}

// TestRawInvalidContractRecoversOnSameAgent covers the corrective-retry path:
// agent "a"'s first attempt fails validation, and its own corrective retry
// (same agent, resumed session) succeeds — agent "b" is never invoked. This
// used to demonstrate fallback to a different agent before runValidated grew
// a same-agent corrective retry for invalid_contract/result_missing; the
// call count (2) is now "a" tried twice, not "a" once then "b" once.
func TestRawInvalidContractRecoversOnSameAgent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "role.md"), []byte("do it"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &sequenceResultRunner{}
	inv := &RoleInvoker{
		Specs: map[string]config.RoleSpec{"custom": {Agents: []config.AgentSpec{{Name: "a"}, {Name: "b"}}, PromptPath: "prompts/role.md", ResultPath: ".orquestalite/results/custom.json", Timeout: time.Minute}},
		Dir:   dir, Runner: runner, Fallback: fallback.NewCaller(fallback.Config{MaxAttempts: 2}),
	}
	validate := func(raw []byte) error {
		var value struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		if value.Status != "ok" {
			return fmt.Errorf("status must be ok")
		}
		return nil
	}
	outcome, err := Raw(context.Background(), inv, "custom", RoleCall{}, RunContext{TaskID: "T"}, validate)
	if err != nil {
		t.Fatal(err)
	}
	if runner.calls != 2 || string(outcome.Output) != `{"status":"ok"}` {
		t.Fatalf("calls=%d raw=%s", runner.calls, outcome.Output)
	}
}

func TestRawAllInvalidReturnsStableContractError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "prompts", "role.md"), []byte("do it"), 0o644)
	runner := &sequenceResultRunner{}
	inv := &RoleInvoker{
		Specs: map[string]config.RoleSpec{"custom": {Agents: []config.AgentSpec{{Name: "a"}, {Name: "b"}}, PromptPath: "prompts/role.md", ResultPath: ".orquestalite/results/custom.json", Timeout: time.Minute}},
		Dir:   dir, Runner: runner, Fallback: fallback.NewCaller(fallback.Config{MaxAttempts: 2}),
	}
	_, err := Raw(context.Background(), inv, "custom", RoleCall{}, RunContext{TaskID: "T"}, func([]byte) error { return fmt.Errorf("always invalid") })
	// Every attempt fails validation, so each agent now exhausts its own
	// corrective-retry budget (1 initial + contractRetryBudget corrective) before
	// fc.Call falls through to the next agent: (1+2)*2 agents = 6 calls total.
	wantCalls := (1 + contractRetryBudget) * 2
	if !errors.Is(err, ErrInvalidContract) || runner.calls != wantCalls {
		t.Fatalf("err=%v calls=%d want=%d", err, runner.calls, wantCalls)
	}
}

func TestRawTimeoutReturnsStableTimeoutError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "role.md"), []byte("do it"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &timeoutResultRunner{}
	inv := &RoleInvoker{
		Specs: map[string]config.RoleSpec{"custom": {Agents: []config.AgentSpec{{Name: "a"}}, PromptPath: "prompts/role.md", ResultPath: ".orquestalite/results/custom.json", Timeout: time.Minute}},
		Dir:   dir, Runner: runner,
	}
	_, err := Raw(context.Background(), inv, "custom", RoleCall{}, RunContext{TaskID: "T"}, nil)
	if !errors.Is(err, ErrAgentTimeout) || runner.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, runner.calls)
	}
}
