package builtin

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/lionelchamorro/orquestalite/internal/runner"
	"github.com/lionelchamorro/orquestalite/internal/sessions"
)

func reviewValidator(_ string, raw []byte) error {
	var value struct {
		Approved *bool `json:"approved"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if value.Approved == nil {
		return fmt.Errorf("approved is required")
	}
	return nil
}

func TestAgentInvokeReturnsValidatedFallbackOutput(t *testing.T) {
	executor := &AgentExecutor{
		Invoker:  &invoke.RoleInvoker{Specs: map[string]config.RoleSpec{}},
		Validate: reviewValidator,
	}
	inputs := []byte(`{
		"role":"critic",
		"outputSchema":"schema:review-result@1",
		"fallbackOutput":{"approved":false,"summary":"critic unavailable","findings":["use QA findings"]}
	}`)
	result, err := executor.Execute(context.Background(), activity.Request{Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output["approved"] != false || output["summary"] != "critic unavailable" {
		t.Fatalf("fallback output=%s", result.Output)
	}
}

// foreachSessionRunner records the ResumeSessionID of every call and always
// returns the same fixed session id, so a test can assert whether a later
// call resumed an earlier one.
type foreachSessionRunner struct {
	specs []runner.Spec
	sid   string
}

func (r *foreachSessionRunner) Run(_ context.Context, spec runner.Spec) (*runner.Result, error) {
	r.specs = append(r.specs, spec)
	if err := os.MkdirAll(filepath.Dir(spec.ResultPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(spec.ResultPath, []byte(`{"approved":true}`), 0o644); err != nil {
		return nil, err
	}
	return &runner.Result{ResultExists: true, ExitCode: 0, Duration: time.Second, SessionID: r.sid}, nil
}

func foreachTestExecutor(t *testing.T, fake *foreachSessionRunner) *AgentExecutor {
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
				Agents:     []config.AgentSpec{{Name: "agentA", Cmd: []string{"fake", "{{PROMPT}}"}}},
				PromptPath: "prompts/coder.md",
				ResultPath: ".orquestalite/results/coder.json",
				Timeout:    time.Minute,
			},
		},
		Dir:         dir,
		Fallback:    fallback.NewCaller(fallback.Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: time.Millisecond}),
		Log:         logger,
		Health:      agenthealth.New(2),
		MemPath:     filepath.Join(dir, ".orquestalite", "memory.md"),
		Runner:      fake,
		Sessions:    sessions.Load(dir),
		ResumeRoles: map[string]bool{"coder": true},
	}
	return &AgentExecutor{Invoker: inv, Validate: func(string, []byte) error { return nil }}
}

// runForeachTicket mirrors how the scheduler actually invokes a step nested
// inside a while-loop's subflow (e.g. "implement_ticket" inside
// develop-ticket@1, called once per while iteration from factory-governed@N):
// StepID and ForeachKey stay constant for every iteration — the iteration's
// identity lives only in ScopePath, which the scheduler extends once per
// subflow instantiation (scheduler.go: child.scope = s.scope + "/" + step.ID +
// keySuffix(foreachKey)) and which then stays fixed across every step inside
// that instance. See scheduler.go's executeWhile -> executeInstance (Subflow
// branch) -> executeActivity chain.
func runForeachTicket(t *testing.T, executor *AgentExecutor, scopePath string) {
	t.Helper()
	req := activity.Request{
		StepID:     "implement_ticket",
		ScopePath:  scopePath,
		ForeachKey: "",
		Attempt:    1,
		Inputs:     []byte(`{"role":"coder","outputSchema":"schema:x@1"}`),
	}
	if _, err := executor.Execute(context.Background(), req); err != nil {
		t.Fatal(err)
	}
}

// TestAgentInvokeDoesNotResumeSessionAcrossForeachIterations reproduces the
// bug where every ticket in a while-loop shared the same session-store key.
// A step nested inside a while-loop's subflow keeps a constant StepID
// ("implement_ticket") and an empty ForeachKey of its own — only ScopePath
// varies per iteration (root/develop_tickets[while-000000] vs
// root/develop_tickets[while-000001]). Using StepID alone (or ForeachKey,
// which is blank here) as the session task key made ticket 2's coder resume
// ticket 1's session, ticket 3 resume ticket 2's (already carrying ticket 1),
// and so on — accumulating the entire prior conversation into every later
// ticket's cached input tokens.
func TestAgentInvokeDoesNotResumeSessionAcrossForeachIterations(t *testing.T) {
	fake := &foreachSessionRunner{sid: "sess-ticket-1"}
	executor := foreachTestExecutor(t, fake)

	runForeachTicket(t, executor, "root/develop_tickets[while-000000]") // ticket 1: no prior session, must be fresh
	runForeachTicket(t, executor, "root/develop_tickets[while-000001]") // ticket 2: DIFFERENT subflow instance, must NOT resume ticket 1's session

	if got := fake.specs[0].ResumeSessionID; got != "" {
		t.Errorf("first ticket should start a fresh session, got resume=%q", got)
	}
	if got := fake.specs[1].ResumeSessionID; got != "" {
		t.Errorf("a different while-iteration (a different ticket) must not resume the previous ticket's session, got resume=%q", got)
	}
}

// TestAgentInvokeResumesSessionWithinSameForeachIteration protects the
// legitimate case this fix must preserve: retrying the SAME ticket (same
// StepID + same ScopePath, only Attempt increments) should still resume
// that ticket's own session so verification-feedback retries keep context.
func TestAgentInvokeResumesSessionWithinSameForeachIteration(t *testing.T) {
	fake := &foreachSessionRunner{sid: "sess-ticket-1"}
	executor := foreachTestExecutor(t, fake)

	runForeachTicket(t, executor, "root/develop_tickets[while-000000]") // ticket 1, attempt 1: fresh
	runForeachTicket(t, executor, "root/develop_tickets[while-000000]") // ticket 1, retry: same scope, must resume

	if got := fake.specs[0].ResumeSessionID; got != "" {
		t.Errorf("first attempt should start a fresh session, got resume=%q", got)
	}
	if got := fake.specs[1].ResumeSessionID; got != "sess-ticket-1" {
		t.Errorf("a retry of the SAME ticket should resume its own session, got resume=%q", got)
	}
}

func TestAgentInvokeRejectsInvalidFallbackOutput(t *testing.T) {
	executor := &AgentExecutor{
		Invoker:  &invoke.RoleInvoker{Specs: map[string]config.RoleSpec{}},
		Validate: reviewValidator,
	}
	inputs := []byte(`{
		"role":"critic",
		"outputSchema":"schema:review-result@1",
		"fallbackOutput":{"summary":"missing approved"}
	}`)
	_, err := executor.Execute(context.Background(), activity.Request{Inputs: inputs})
	if activity.Classify(err) != activity.ErrorInvalidContract {
		t.Fatalf("err=%v", err)
	}
}
