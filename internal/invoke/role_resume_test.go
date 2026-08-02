package invoke

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/agenthealth"
	"github.com/lionelchamorro/orquestalite/internal/config"
	"github.com/lionelchamorro/orquestalite/internal/eventlog"
	"github.com/lionelchamorro/orquestalite/internal/fallback"
	"github.com/lionelchamorro/orquestalite/internal/runner"
	"github.com/lionelchamorro/orquestalite/internal/sessions"
)

// sessionRunner records the spec of every call (so a test can inspect the
// ResumeSessionID it received) and returns a fixed session id.
type sessionRunner struct {
	specs []runner.Spec
	sid   string
}

func (r *sessionRunner) Run(_ context.Context, spec runner.Spec) (*runner.Result, error) {
	r.specs = append(r.specs, spec)
	if err := os.MkdirAll(filepath.Dir(spec.ResultPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(spec.ResultPath, []byte(`{"status":"ok"}`), 0o644); err != nil {
		return nil, err
	}
	return &runner.Result{ResultExists: true, ExitCode: 0, Duration: time.Second, SessionID: r.sid}, nil
}

func resumeTestInvoker(t *testing.T, fake *sessionRunner, resumeRoles map[string]bool) *RoleInvoker {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "coder.md"), []byte("do {{TASK_ID}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	logger, err := eventlog.OpenWithFormat(filepath.Join(dir, ".orquestalite", "run.log"), io.Discard, eventlog.FormatVerbose)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	return &RoleInvoker{
		Specs: map[string]config.RoleSpec{
			"coder": {
				Agents: []config.AgentSpec{
					{Name: "agentA", Cmd: []string{"fake", "{{PROMPT}}"}},
					{Name: "agentB", Cmd: []string{"fake", "{{PROMPT}}"}},
				},
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
		ResumeRoles: resumeRoles,
	}
}

func runCoder(t *testing.T, inv *RoleInvoker, agent string) {
	t.Helper()
	_, err := Role[fakeRoleResult](
		context.Background(), inv, "coder",
		RoleCall{AgentOverride: agent, Vars: map[string]string{"TASK_ID": "T1"}},
		RunContext{TaskID: "T1", Cycle: 1, Attempt: 1},
		func(path string) (*fakeRoleResult, error) {
			var out fakeRoleResult
			out.Status = "ok"
			return &out, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRoleResumesSameAgentFreshOnSwitch(t *testing.T) {
	fake := &sessionRunner{sid: "sess1"}
	inv := resumeTestInvoker(t, fake, map[string]bool{"coder": true})

	runCoder(t, inv, "agentA") // first run on T1 with agentA → fresh
	runCoder(t, inv, "agentA") // same agent again → resume
	runCoder(t, inv, "agentB") // different agent → fresh (switch provider)

	if got := fake.specs[0].ResumeSessionID; got != "" {
		t.Errorf("first run should be fresh, got resume=%q", got)
	}
	if got := fake.specs[1].ResumeSessionID; got != "sess1" {
		t.Errorf("second run on same agent should resume sess1, got %q", got)
	}
	if got := fake.specs[2].ResumeSessionID; got != "" {
		t.Errorf("run on a different agent should be fresh, got resume=%q", got)
	}
}

func TestRoleNoResumeWhenRoleDisabled(t *testing.T) {
	fake := &sessionRunner{sid: "sess1"}
	inv := resumeTestInvoker(t, fake, nil) // resume disabled (no roles)

	runCoder(t, inv, "agentA")
	runCoder(t, inv, "agentA")

	for i, s := range fake.specs {
		if s.ResumeSessionID != "" {
			t.Errorf("run %d should never resume when disabled, got %q", i, s.ResumeSessionID)
		}
	}
}
