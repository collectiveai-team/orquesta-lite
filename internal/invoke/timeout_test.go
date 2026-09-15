package invoke

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

	"github.com/collectiveai-team/orquesta-lite/internal/agenthealth"
	"github.com/collectiveai-team/orquesta-lite/internal/config"
	"github.com/collectiveai-team/orquesta-lite/internal/eventlog"
	"github.com/collectiveai-team/orquesta-lite/internal/fallback"
	"github.com/collectiveai-team/orquesta-lite/internal/runner"
)

// scaffoldRunner reproduces the shape that caused the incident: the agent writes
// the schema-valid placeholder its prompt tells it to write before starting
// work, then gets killed at the role's timeout with the work unfinished.
type scaffoldRunner struct {
	timeouts int // how many leading agents are killed at their timeout
	calls    []string
}

const reviewScaffold = `{"status":"partial","decision":"inconclusive","approved":false,"summary":"Review in progress"}`
const reviewFinal = `{"status":"complete","decision":"approve","approved":true,"summary":"Looks good"}`

func (r *scaffoldRunner) Run(_ context.Context, spec runner.Spec) (*runner.Result, error) {
	index := len(r.calls)
	r.calls = append(r.calls, spec.ResultPath)
	if err := os.MkdirAll(filepath.Dir(spec.ResultPath), 0o755); err != nil {
		return nil, err
	}
	if index < r.timeouts {
		// Killed mid-turn: the scaffold is on disk, exit code is the kill.
		if err := os.WriteFile(spec.ResultPath, []byte(reviewScaffold), 0o644); err != nil {
			return nil, err
		}
		return &runner.Result{ResultExists: true, TimedOut: true, ExitCode: -1, Duration: spec.Timeout}, nil
	}
	if err := os.WriteFile(spec.ResultPath, []byte(reviewFinal), 0o644); err != nil {
		return nil, err
	}
	return &runner.Result{ResultExists: true, ExitCode: 0, Duration: time.Second}, nil
}

type reviewResult struct {
	Status   string `json:"status"`
	Approved bool   `json:"approved"`
}

func timeoutInvoker(t *testing.T, agents []config.AgentSpec, run AgentRunner) *RoleInvoker {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "adversary.md"), []byte("review it"), 0o644); err != nil {
		t.Fatal(err)
	}
	memPath := filepath.Join(dir, ".orquestalite", "memory.md")
	if err := os.MkdirAll(filepath.Dir(memPath), 0o755); err != nil {
		t.Fatal(err)
	}
	logger, err := eventlog.OpenWithFormat(filepath.Join(dir, ".orquestalite", "run.log"), io.Discard, eventlog.FormatVerbose)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	return &RoleInvoker{
		Specs: map[string]config.RoleSpec{
			"adversary": {
				Agents:     agents,
				PromptPath: "prompts/adversary.md",
				ResultPath: ".orquestalite/results/adversary.json",
				Timeout:    1500 * time.Second,
			},
		},
		Dir:      dir,
		Fallback: fallback.NewCaller(fallback.Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: time.Millisecond}),
		Log:      logger,
		Health:   agenthealth.New(4),
		MemPath:  memPath,
		Runner:   run,
	}
}

func invokeReview(t *testing.T, inv *RoleInvoker) (*reviewResult, error) {
	t.Helper()
	return Role[reviewResult](
		context.Background(), inv, "adversary",
		RoleCall{}, RunContext{TaskID: "T1"},
		func(path string) (*reviewResult, error) {
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			var out reviewResult
			return &out, json.Unmarshal(raw, &out)
		},
	)
}

// TestTimedOutAgentNeverBecomesTheRoleResult is the incident. The adversary role
// was killed at 1500s with the opening scaffold on disk; the step was recorded
// succeeded with a review asserting "not approved, review in progress", and the
// run continued on a review that never happened.
func TestTimedOutAgentNeverBecomesTheRoleResult(t *testing.T) {
	run := &scaffoldRunner{timeouts: 1}
	inv := timeoutInvoker(t, []config.AgentSpec{{Name: "claude_opus", Cmd: []string{"fake", "{{PROMPT}}"}}}, run)
	got, err := invokeReview(t, inv)
	if err == nil {
		t.Fatalf("a killed agent's scaffold was accepted as the role result: %+v", got)
	}
	if !errors.Is(err, ErrAgentTimeout) {
		t.Fatalf("error %v does not carry ErrAgentTimeout", err)
	}
}

// TestTimedOutAgentFallsBackToTheNextAgent proves the ladder fires. `case
// r.TimedOut` existed to trigger exactly this and was unreachable whenever the
// agent had written any file at result_path.
func TestTimedOutAgentFallsBackToTheNextAgent(t *testing.T) {
	run := &scaffoldRunner{timeouts: 1}
	inv := timeoutInvoker(t, []config.AgentSpec{
		{Name: "claude_opus", Cmd: []string{"fake", "{{PROMPT}}"}},
		{Name: "claude_sonnet", Cmd: []string{"fake", "{{PROMPT}}"}},
	}, run)
	got, err := invokeReview(t, inv)
	if err != nil {
		t.Fatalf("the second agent should have produced the review: %v", err)
	}
	if got.Status != "complete" || !got.Approved {
		t.Fatalf("result came from the killed agent: %+v", got)
	}
	if len(run.calls) != 2 {
		t.Fatalf("expected the ladder to run 2 agents, ran %d", len(run.calls))
	}
}

// TestTimeoutErrorNamesTheRoleTheLimitAndTheDiscardedResult covers the reported
// diagnosis gap: the failure surfaced with a message naming neither the role nor
// the timeout, and "did not write" was not even true — the agent had written.
func TestTimeoutErrorNamesTheRoleTheLimitAndTheDiscardedResult(t *testing.T) {
	run := &scaffoldRunner{timeouts: 1}
	inv := timeoutInvoker(t, []config.AgentSpec{{Name: "claude_opus", Cmd: []string{"fake", "{{PROMPT}}"}}}, run)
	_, err := invokeReview(t, inv)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	message := err.Error()
	for _, want := range []string{"adversary", "claude_opus", "timeout", "discard"} {
		if !strings.Contains(message, want) {
			t.Errorf("error message does not mention %q: %s", want, message)
		}
	}
	if strings.Contains(message, "did not write") {
		t.Errorf("message still claims the agent wrote nothing: %s", message)
	}
}
