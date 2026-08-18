package invoke

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/agenthealth"
	"github.com/collectiveai-team/orquesta-lite/internal/config"
	"github.com/collectiveai-team/orquesta-lite/internal/eventlog"
	"github.com/collectiveai-team/orquesta-lite/internal/fallback"
	"github.com/collectiveai-team/orquesta-lite/internal/runner"
)

// noResultRunner stands in for an agent that exits cleanly but writes no result
// file — exactly the compactor's "the digest is already compact, nothing to
// rewrite" behaviour that classify() reports as result_missing.
type noResultRunner struct{ calls int }

func (r *noResultRunner) Run(_ context.Context, _ runner.Spec) (*runner.Result, error) {
	r.calls++
	return &runner.Result{ResultExists: false, ExitCode: 0, Duration: time.Millisecond}, nil
}

func besteffortInvoker(t *testing.T, role string, health *agenthealth.Tracker, bestEffort map[string]bool) *RoleInvoker {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "p.md"), []byte("go"), 0o644); err != nil {
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
			role: {
				Agents:     []config.AgentSpec{{Name: "claude_sonnet", Cmd: []string{"fake"}}},
				PromptPath: "prompts/p.md",
				ResultPath: ".orquestalite/results/r.json",
				Timeout:    time.Minute,
			},
		},
		Dir:             dir,
		Fallback:        fallback.NewCaller(fallback.Config{InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}),
		Log:             logger,
		Health:          health,
		MemPath:         memPath,
		Runner:          &noResultRunner{},
		BestEffortRoles: bestEffort,
	}
}

// A best-effort role (e.g. the memory compactor) that fails to write a result
// must NOT bench its agent in the shared health tracker — otherwise its failure
// poisons the very providers that the critical roles (coder/tester) depend on.
func TestBestEffortRoleDoesNotMarkAgentSkipped(t *testing.T) {
	health := agenthealth.New(2)
	inv := besteffortInvoker(t, "compactor", health, map[string]bool{"compactor": true})

	_ = inv.RunOnce(context.Background(), "compactor", RoleCall{}, RunContext{TaskID: "_memory", Attempt: 1})

	if _, skipped := health.IsSkipped("claude_sonnet"); skipped {
		t.Fatal("best-effort role must not bench its agent in the shared health tracker")
	}
}

// Control: a normal (non-best-effort) role's result_missing failures across
// distinct activity invocations still bench the agent. A single invocation no
// longer repeats the same broken agent internally.
func TestNonBestEffortRoleStillMarksAgentSkipped(t *testing.T) {
	health := agenthealth.New(2)
	inv := besteffortInvoker(t, "coder", health, nil)

	_ = inv.RunOnce(context.Background(), "coder", RoleCall{}, RunContext{TaskID: "T001", Attempt: 1})
	_ = inv.RunOnce(context.Background(), "coder", RoleCall{}, RunContext{TaskID: "T001", Attempt: 2})

	if _, skipped := health.IsSkipped("claude_sonnet"); !skipped {
		t.Fatal("result_missing across critical-role invocations should bench the agent")
	}
}
