package invoke

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/config"
	"github.com/lionelchamorro/orquestalite/internal/fallback"
	"github.com/lionelchamorro/orquestalite/internal/runner"
	"github.com/lionelchamorro/orquestalite/internal/sessions"
)

// contractRetryStep scripts one runner.Run response for scriptedContractRunner.
type contractRetryStep struct {
	resultExists bool
	body         string // written to spec.ResultPath when resultExists
	sessionID    string
	rateLimited  bool
	timedOut     bool
	authFailed   bool
}

// scriptedContractRunner replays a fixed sequence of results, one per call, and
// records every runner.Spec it was invoked with (for asserting prompts and
// ResumeSessionID overrides across initial + corrective attempts).
type scriptedContractRunner struct {
	steps []contractRetryStep
	specs []runner.Spec
}

func (r *scriptedContractRunner) Run(_ context.Context, spec runner.Spec) (*runner.Result, error) {
	r.specs = append(r.specs, spec)
	i := len(r.specs) - 1
	if i >= len(r.steps) {
		return nil, errors.New("scriptedContractRunner: ran out of scripted steps")
	}
	step := r.steps[i]
	if step.resultExists {
		if err := os.MkdirAll(filepath.Dir(spec.ResultPath), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(spec.ResultPath, []byte(step.body), 0o644); err != nil {
			return nil, err
		}
	}
	return &runner.Result{
		ResultExists: step.resultExists,
		SessionID:    step.sessionID,
		RateLimited:  step.rateLimited,
		TimedOut:     step.timedOut,
		AuthFailed:   step.authFailed,
		ExitCode:     0,
		Duration:     time.Millisecond,
	}, nil
}

// contractRetryInvoker builds a single-agent RoleInvoker for role "coder"
// wired to the given runner, with session tracking and OnAgentSuccess wired
// up so tests can assert on both.
func contractRetryInvoker(t *testing.T, r AgentRunner, agents []config.AgentSpec, bestEffort map[string]bool) (*RoleInvoker, *[]string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "p.md"), []byte("do the task"), 0o644); err != nil {
		t.Fatal(err)
	}
	var successes []string
	inv := &RoleInvoker{
		Specs: map[string]config.RoleSpec{
			"coder": {Agents: agents, PromptPath: "prompts/p.md", ResultPath: ".orquestalite/results/coder.json", Timeout: time.Minute},
		},
		Dir:             dir,
		Runner:          r,
		Fallback:        fallback.NewCaller(fallback.Config{InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}),
		Sessions:        sessions.Load(dir),
		ResumeRoles:     map[string]bool{"coder": true},
		BestEffortRoles: bestEffort,
		OnAgentSuccess:  func(_, agent string) { successes = append(successes, agent) },
	}
	return inv, &successes
}

func statusOKValidate(raw []byte) error {
	var v struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	if v.Status != "ok" {
		return errors.New("status must be ok")
	}
	return nil
}

// (a) invalid_contract fails once, corrective retry succeeds.
func TestContractRetry_InvalidContractRecovers(t *testing.T) {
	r := &scriptedContractRunner{steps: []contractRetryStep{
		{resultExists: true, body: `{"status":"bad"}`, sessionID: "sess-initial"},
		{resultExists: true, body: `{"status":"ok"}`, sessionID: "sess-corrective"},
	}}
	inv, successes := contractRetryInvoker(t, r, []config.AgentSpec{{Name: "agent1"}}, nil)

	raw, err := Raw(context.Background(), inv, "coder", RoleCall{}, RunContext{TaskID: "T1"}, statusOKValidate)
	if err != nil {
		t.Fatalf("expected success, got err=%v", err)
	}
	if string(raw.Output) != `{"status":"ok"}` {
		t.Fatalf("raw = %s", raw.Output)
	}
	if len(r.specs) != 2 {
		t.Fatalf("expected 2 runner calls, got %d", len(r.specs))
	}
	if got := r.specs[1].ResumeSessionID; got != "sess-initial" {
		t.Errorf("corrective ResumeSessionID = %q, want %q (the failed attempt's ephemeral session)", got, "sess-initial")
	}
	if r.specs[1].Prompt == r.specs[0].Prompt {
		t.Error("corrective prompt must differ from the original prompt")
	}
	if len(*successes) != 1 || (*successes)[0] != "agent1" {
		t.Errorf("OnAgentSuccess = %v, want exactly one call for agent1", *successes)
	}
	if got := inv.Sessions.Get(inv.sessionTaskKey("T1"), "coder", "agent1"); got != "sess-corrective" {
		t.Errorf("stored session = %q, want the corrective attempt's session %q", got, "sess-corrective")
	}
}

// (b) result_missing fails once, corrective retry succeeds.
func TestContractRetry_ResultMissingRecovers(t *testing.T) {
	r := &scriptedContractRunner{steps: []contractRetryStep{
		{resultExists: false, sessionID: "sess-init"},
		{resultExists: true, body: `{}`, sessionID: "sess-corrective"},
	}}
	inv, _ := contractRetryInvoker(t, r, []config.AgentSpec{{Name: "agent1"}}, nil)

	err := inv.RunOnce(context.Background(), "coder", RoleCall{}, RunContext{TaskID: "T1"})
	if err != nil {
		t.Fatalf("expected success, got err=%v", err)
	}
	if len(r.specs) != 2 {
		t.Fatalf("expected 2 runner calls, got %d", len(r.specs))
	}
	if got := r.specs[1].ResumeSessionID; got != "sess-init" {
		t.Errorf("corrective ResumeSessionID = %q, want %q", got, "sess-init")
	}
}

// (c) invalid_contract budget exhausted -> ErrInvalidContract, exactly
// 1+contractRetryBudget calls.
func TestContractRetry_InvalidContractExhaustsBudget(t *testing.T) {
	steps := make([]contractRetryStep, 0, 1+contractRetryBudget)
	for i := 0; i < 1+contractRetryBudget; i++ {
		steps = append(steps, contractRetryStep{resultExists: true, body: `{"status":"bad"}`, sessionID: "s"})
	}
	r := &scriptedContractRunner{steps: steps}
	inv, _ := contractRetryInvoker(t, r, []config.AgentSpec{{Name: "agent1"}}, nil)

	_, err := Raw(context.Background(), inv, "coder", RoleCall{}, RunContext{TaskID: "T1"}, statusOKValidate)
	if !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("err = %v, want ErrInvalidContract", err)
	}
	if len(r.specs) != 1+contractRetryBudget {
		t.Fatalf("calls = %d, want %d", len(r.specs), 1+contractRetryBudget)
	}
}

// (d) result_missing budget exhausted -> generic error, not ErrInvalidContract.
func TestContractRetry_ResultMissingExhaustsBudget(t *testing.T) {
	steps := make([]contractRetryStep, 0, 1+contractRetryBudget)
	for i := 0; i < 1+contractRetryBudget; i++ {
		steps = append(steps, contractRetryStep{resultExists: false, sessionID: "s"})
	}
	r := &scriptedContractRunner{steps: steps}
	inv, _ := contractRetryInvoker(t, r, []config.AgentSpec{{Name: "agent1"}}, nil)

	err := inv.RunOnce(context.Background(), "coder", RoleCall{}, RunContext{TaskID: "T1"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, ErrInvalidContract) {
		t.Errorf("err = %v, must not be wrapped as ErrInvalidContract", err)
	}
	if len(r.specs) != 1+contractRetryBudget {
		t.Fatalf("calls = %d, want %d", len(r.specs), 1+contractRetryBudget)
	}
}

// (e) multi-agent chain: agent1 exhausts its own corrective budget on
// invalid_contract, then fc.Call falls through to agent2, which succeeds
// fresh (no resumed session, since it never ran before).
func TestContractRetry_MultiAgentFallbackStillWorks(t *testing.T) {
	steps := []contractRetryStep{
		{resultExists: true, body: `{"status":"bad"}`, sessionID: "a1-s0"},
		{resultExists: true, body: `{"status":"bad"}`, sessionID: "a1-s1"},
		{resultExists: true, body: `{"status":"bad"}`, sessionID: "a1-s2"},
		{resultExists: true, body: `{"status":"ok"}`, sessionID: "a2-s0"},
	}
	r := &scriptedContractRunner{steps: steps}
	inv, successes := contractRetryInvoker(t, r, []config.AgentSpec{{Name: "agent1"}, {Name: "agent2"}}, nil)

	_, err := Raw(context.Background(), inv, "coder", RoleCall{}, RunContext{TaskID: "T1"}, statusOKValidate)
	if err != nil {
		t.Fatalf("expected success via agent2, got err=%v", err)
	}
	if len(r.specs) != 4 {
		t.Fatalf("calls = %d, want 4 (3 for agent1 + 1 for agent2)", len(r.specs))
	}
	if got := r.specs[3].ResumeSessionID; got != "" {
		t.Errorf("agent2's first attempt ResumeSessionID = %q, want empty (fresh start)", got)
	}
	if len(*successes) != 1 || (*successes)[0] != "agent2" {
		t.Errorf("OnAgentSuccess = %v, want exactly one call for agent2", *successes)
	}
}

// (f) rate_limit is never routed through the corrective-retry path: agent1
// rate-limits once, fc.Call's own cooldown/fallback moves to agent2 which
// succeeds immediately — no corrective retry attempted on agent1.
func TestContractRetry_RateLimitNeverRetriesCorrectively(t *testing.T) {
	r := &scriptedContractRunner{steps: []contractRetryStep{
		{resultExists: false, rateLimited: true},
		{resultExists: true, body: `{}`},
	}}
	inv, _ := contractRetryInvoker(t, r, []config.AgentSpec{{Name: "agent1"}, {Name: "agent2"}}, nil)

	err := inv.RunOnce(context.Background(), "coder", RoleCall{}, RunContext{TaskID: "T1"})
	if err != nil {
		t.Fatalf("expected success via agent2, got err=%v", err)
	}
	if len(r.specs) != 2 {
		t.Fatalf("calls = %d, want 2 (agent1 rate-limited once, agent2 succeeds once)", len(r.specs))
	}
}

// (g) timeout is never routed through the corrective-retry path.
func TestContractRetry_TimeoutNeverRetriesCorrectively(t *testing.T) {
	r := &scriptedContractRunner{steps: []contractRetryStep{
		{resultExists: false, timedOut: true},
	}}
	inv, _ := contractRetryInvoker(t, r, []config.AgentSpec{{Name: "agent1"}}, nil)

	err := inv.RunOnce(context.Background(), "coder", RoleCall{}, RunContext{TaskID: "T1"})
	if !errors.Is(err, ErrAgentTimeout) {
		t.Fatalf("err = %v, want ErrAgentTimeout", err)
	}
	if len(r.specs) != 1 {
		t.Fatalf("calls = %d, want 1 (no corrective retry on timeout)", len(r.specs))
	}
}

// (h) auth_failed is never routed through the corrective-retry path.
func TestContractRetry_AuthFailedNeverRetriesCorrectively(t *testing.T) {
	r := &scriptedContractRunner{steps: []contractRetryStep{
		{resultExists: false, authFailed: true},
		{resultExists: true, body: `{}`},
	}}
	inv, _ := contractRetryInvoker(t, r, []config.AgentSpec{{Name: "agent1"}, {Name: "agent2"}}, nil)

	err := inv.RunOnce(context.Background(), "coder", RoleCall{}, RunContext{TaskID: "T1"})
	if err != nil {
		t.Fatalf("expected success via agent2, got err=%v", err)
	}
	if len(r.specs) != 2 {
		t.Fatalf("calls = %d, want 2 (agent1 auth-failed once, agent2 succeeds once)", len(r.specs))
	}
}

// (i) a best-effort role never gets a corrective retry even on result_missing.
func TestContractRetry_BestEffortRoleSkipsRetry(t *testing.T) {
	r := &scriptedContractRunner{steps: []contractRetryStep{
		{resultExists: false},
	}}
	inv, _ := contractRetryInvoker(t, r, []config.AgentSpec{{Name: "agent1"}}, map[string]bool{"coder": true})

	_ = inv.RunOnce(context.Background(), "coder", RoleCall{}, RunContext{TaskID: "T1"})
	if len(r.specs) != 1 {
		t.Fatalf("calls = %d, want 1 (best-effort roles never get a corrective retry)", len(r.specs))
	}
}
