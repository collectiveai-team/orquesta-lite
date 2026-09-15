package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/agenthealth"
	"github.com/collectiveai-team/orquesta-lite/internal/config"
	"github.com/collectiveai-team/orquesta-lite/internal/eventlog"
	"github.com/collectiveai-team/orquesta-lite/internal/fallback"
	"github.com/collectiveai-team/orquesta-lite/internal/invoke"
	"github.com/collectiveai-team/orquesta-lite/internal/runner"
	"github.com/collectiveai-team/orquesta-lite/internal/sessions"
)

// killedReviewerRunner writes the opening scaffold every review prompt in the
// development pack asks for, then reports the kill the role's timeout caused.
type killedReviewerRunner struct{ calls int }

func (r *killedReviewerRunner) Run(_ context.Context, spec runner.Spec) (*runner.Result, error) {
	r.calls++
	if err := os.MkdirAll(filepath.Dir(spec.ResultPath), 0o755); err != nil {
		return nil, err
	}
	scaffold := `{"version":2,"status":"partial","decision":"inconclusive","approved":false,"summary":"Review in progress","findings":[],"limitations":["Required checks pending"],"reviewed_revision":"","evidence":[]}`
	if err := os.WriteFile(spec.ResultPath, []byte(scaffold), 0o644); err != nil {
		return nil, err
	}
	return &runner.Result{ResultExists: true, TimedOut: true, ExitCode: -1, Duration: spec.Timeout}, nil
}

func killedReviewerExecutor(t *testing.T, run invoke.AgentRunner) *AgentExecutor {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "adversary.md"), []byte("review"), 0o644); err != nil {
		t.Fatal(err)
	}
	logger, err := eventlog.OpenWithFormat(filepath.Join(dir, ".orquestalite", "run.log"), io.Discard, eventlog.FormatVerbose)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	inv := &invoke.RoleInvoker{
		Specs: map[string]config.RoleSpec{
			"adversary": {
				Agents:     []config.AgentSpec{{Name: "claude_opus", Cmd: []string{"fake", "{{PROMPT}}"}}},
				PromptPath: "prompts/adversary.md",
				ResultPath: ".orquestalite/results/adversary.json",
				Timeout:    1500 * time.Second,
			},
		},
		Dir:      dir,
		Fallback: fallback.NewCaller(fallback.Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: time.Millisecond}),
		Log:      logger,
		Health:   agenthealth.New(2),
		MemPath:  filepath.Join(dir, ".orquestalite", "memory.md"),
		Runner:   run,
		Sessions: sessions.Load(dir),
	}
	return &AgentExecutor{Invoker: inv, Validate: func(string, []byte) error { return nil }}
}

// TestKilledReviewerYieldsTheDeclaredFallbackNotItsScaffold is the incident at
// the boundary that matters. integrated-review@1 declares, for every review
// role, a fallbackOutput that says plainly "Provider did not produce a valid
// review checkpoint". What the run recorded instead was the agent's own opening
// scaffold — `status: partial`, `summary: "Review in progress"` — which reads
// like a real reviewer's incomplete verdict and was handed to governance as
// ADVERSARY_REVIEW.
func TestKilledReviewerYieldsTheDeclaredFallbackNotItsScaffold(t *testing.T) {
	executor := killedReviewerExecutor(t, &killedReviewerRunner{})
	inputs := []byte(`{
		"role":"adversary",
		"outputSchema":"schema:review-result@2",
		"fallbackOutput":{"version":2,"status":"unavailable","decision":"inconclusive","approved":false,
			"summary":"Required reviewer unavailable","findings":[],
			"limitations":["Provider did not produce a valid review checkpoint"],
			"reviewed_revision":"","evidence":[]}
	}`)
	result, err := executor.Execute(context.Background(), activity.Request{Inputs: inputs})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got struct {
		Status  string `json:"status"`
		Summary string `json:"summary"`
	}
	if err = json.Unmarshal(result.Output, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "unavailable" {
		t.Fatalf("status=%q summary=%q — the killed agent's scaffold became the step output", got.Status, got.Summary)
	}
}

// TestKilledReviewerWithoutAFallbackFailsAsTimeout covers the steps that declare
// no fallback: the run must stop at the role that timed out, carrying the
// timeout class, instead of continuing on a review that never happened.
func TestKilledReviewerWithoutAFallbackFailsAsTimeout(t *testing.T) {
	executor := killedReviewerExecutor(t, &killedReviewerRunner{})
	inputs := []byte(`{"role":"adversary","outputSchema":"schema:review-result@2"}`)
	_, err := executor.Execute(context.Background(), activity.Request{Inputs: inputs})
	if err == nil {
		t.Fatal("a killed reviewer produced a successful step")
	}
	var activityErr *activity.Error
	if !errors.As(err, &activityErr) || activityErr.Class != activity.ErrorTimeout {
		t.Fatalf("error %v is not classified as a timeout", err)
	}
	if !strings.Contains(err.Error(), "adversary") {
		t.Errorf("error does not name the role: %v", err)
	}
}
