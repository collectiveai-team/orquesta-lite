package invoke

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/agenthealth"
	"github.com/collectiveai-team/orquesta-lite/internal/config"
	"github.com/collectiveai-team/orquesta-lite/internal/eventlog"
	"github.com/collectiveai-team/orquesta-lite/internal/fallback"
	"github.com/collectiveai-team/orquesta-lite/internal/opencodeattach"
	"github.com/collectiveai-team/orquesta-lite/internal/runner"
	"github.com/collectiveai-team/orquesta-lite/internal/sessions"
)

// attachServer is a minimal stand-in for `opencode serve` that hands out
// predictable session ids so a test can prove which id reached the CLI.
type attachServer struct {
	*httptest.Server
	mu      sync.Mutex
	creates int
}

func newAttachServer(t *testing.T) *attachServer {
	t.Helper()
	s := &attachServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, "[]")
			return
		}
		// The root's note is also a POST; only session creates are counted, so
		// that "how many sessions did this run mint?" stays a real assertion.
		if strings.HasSuffix(r.URL.Path, "/message") {
			_, _ = io.WriteString(w, "{}")
			return
		}
		s.mu.Lock()
		s.creates++
		id := "ses_minted" + string(rune('0'+s.creates))
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
	}))
	t.Cleanup(s.Close)
	return s
}

// attachRunner records specs and can be told to report an abort.
type attachRunner struct {
	specs   []runner.Spec
	aborted bool
	calls   int
}

func (r *attachRunner) Run(_ context.Context, spec runner.Spec) (*runner.Result, error) {
	r.specs = append(r.specs, spec)
	r.calls++
	if r.aborted {
		// Mirrors the real CLI: aborted runs exit 0 and write no result.
		return &runner.Result{ExitCode: 0, Aborted: true, SessionID: spec.ResumeSessionID}, nil
	}
	if err := os.MkdirAll(filepath.Dir(spec.ResultPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(spec.ResultPath, []byte(`{"status":"ok"}`), 0o644); err != nil {
		return nil, err
	}
	return &runner.Result{ResultExists: true, ExitCode: 0, SessionID: spec.ResumeSessionID}, nil
}

func attachTestInvoker(t *testing.T, fake *attachRunner, provider string, mgr *opencodeattach.Manager) *RoleInvoker {
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
				Agents:     []config.AgentSpec{{Name: "agentA", Provider: provider, Model: "m"}},
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
		Attach:      mgr,
	}
}

func runAttachCoder(inv *RoleInvoker) error {
	_, err := Role[fakeRoleResult](
		context.Background(), inv, "coder",
		RoleCall{AgentOverride: "agentA", Vars: map[string]string{"TASK_ID": "T1"}},
		RunContext{TaskID: "T1", Cycle: 1, Attempt: 1},
		func(string) (*fakeRoleResult, error) {
			var out fakeRoleResult
			out.Status = "ok"
			return &out, nil
		},
	)
	return err
}

func newTestManager(t *testing.T, srv *attachServer) *opencodeattach.Manager {
	t.Helper()
	client, err := opencodeattach.NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return opencodeattach.NewManager(client, "/proj", "run-1", "flow")
}

// TestAttachMintsSessionBeforeRun is the inversion: without attach the session
// id is scraped from stdout after the fact, so the spec goes out empty; with
// attach the id exists first and is handed to the CLI.
func TestAttachMintsSessionBeforeRun(t *testing.T) {
	srv := newAttachServer(t)
	fake := &attachRunner{}
	inv := attachTestInvoker(t, fake, "opencode", newTestManager(t, srv))

	if err := runAttachCoder(inv); err != nil {
		t.Fatal(err)
	}
	spec := fake.specs[0]
	if spec.ResumeSessionID == "" {
		t.Fatal("attach run started with no session id — the id was not minted up front")
	}
	if spec.AttachURL != srv.URL {
		t.Errorf("spec.AttachURL = %q, want %q", spec.AttachURL, srv.URL)
	}
	if spec.AttachDir != "/proj" {
		t.Errorf("spec.AttachDir = %q, want /proj", spec.AttachDir)
	}
}

// A minted child is stored in the ordinary session slot, so the next run of the
// same agent on the same task continues that child rather than creating a
// sibling. This is what keeps a resumed conversation in one place in the tree.
func TestAttachReusesStoredSessionInsteadOfMintingAgain(t *testing.T) {
	srv := newAttachServer(t)
	fake := &attachRunner{}
	inv := attachTestInvoker(t, fake, "opencode", newTestManager(t, srv))

	if err := runAttachCoder(inv); err != nil {
		t.Fatal(err)
	}
	if err := runAttachCoder(inv); err != nil {
		t.Fatal(err)
	}

	first, second := fake.specs[0].ResumeSessionID, fake.specs[1].ResumeSessionID
	if second != first {
		t.Fatalf("second run should continue the minted child %q, got %q", first, second)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	// 1 root + 1 child. A second child would mean the resume path minted again.
	if srv.creates != 2 {
		t.Fatalf("want 2 session creates (root + one child), got %d", srv.creates)
	}
}

// Attach is opencode-specific. A chain that falls back to another provider must
// run normally rather than being handed flags its CLI does not have.
func TestAttachIgnoresNonOpenCodeProvider(t *testing.T) {
	srv := newAttachServer(t)
	fake := &attachRunner{}
	inv := attachTestInvoker(t, fake, "claude", newTestManager(t, srv))

	if err := runAttachCoder(inv); err != nil {
		t.Fatal(err)
	}
	spec := fake.specs[0]
	if spec.AttachURL != "" || spec.AttachDir != "" {
		t.Errorf("attach flags leaked to a non-opencode provider: url=%q dir=%q", spec.AttachURL, spec.AttachDir)
	}
	if spec.ResumeSessionID != "" {
		t.Errorf("minted a session for a non-opencode provider: %q", spec.ResumeSessionID)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.creates != 0 {
		t.Errorf("contacted the attach server for a non-opencode provider (%d creates)", srv.creates)
	}
}

// With attach unset nothing changes: no flags, and the session id still arrives
// the old way. This is the guard that attach mode stays opt-in.
func TestNoAttachLeavesSpecUnchanged(t *testing.T) {
	fake := &attachRunner{}
	inv := attachTestInvoker(t, fake, "opencode", nil)

	if err := runAttachCoder(inv); err != nil {
		t.Fatal(err)
	}
	spec := fake.specs[0]
	if spec.AttachURL != "" || spec.AttachDir != "" || spec.ResumeSessionID != "" {
		t.Fatalf("attach-less run was modified: %+v", spec)
	}
}

// TestAbortIsTerminal is the behavioural consequence of the exit-code finding.
// An aborted run writes no result, which classifies as result_missing — the
// reason that feeds the corrective-retry loop. Without an explicit abort signal
// the runtime would relaunch the work the user just cancelled.
func TestAbortIsTerminal(t *testing.T) {
	srv := newAttachServer(t)
	fake := &attachRunner{aborted: true}
	inv := attachTestInvoker(t, fake, "opencode", newTestManager(t, srv))

	err := runAttachCoder(inv)
	if err == nil {
		t.Fatal("an aborted run was reported as success")
	}
	if !errors.Is(err, ErrAgentAborted) {
		t.Fatalf("want ErrAgentAborted, got %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("abort triggered %d runs; cancelling must not restart the work", fake.calls)
	}
}

// The stored session must survive an abort: the child session still exists on
// the server with the user's partial conversation in it, so a later resume
// should land there rather than orphan it behind a fresh sibling.
// TestAttachDoesNotReportFreshRunAsResumed guards an honesty property of the
// run log. Attach populates ResumeSessionID with a session it just created, so
// deriving "resumed" from that field alone would make every first invocation
// claim to continue a conversation that never existed.
func TestAttachDoesNotReportFreshRunAsResumed(t *testing.T) {
	srv := newAttachServer(t)
	fake := &attachRunner{}
	inv := attachTestInvoker(t, fake, "opencode", newTestManager(t, srv))
	logPath := filepath.Join(inv.Dir, ".orquestalite", "run.log")

	if err := runAttachCoder(inv); err != nil { // fresh: mints a session
		t.Fatal(err)
	}
	if err := runAttachCoder(inv); err != nil { // genuine resume of that session
		t.Fatal(err)
	}
	if err := inv.Log.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	// Records are flat: {"ts":…,"event":"agent_run",<fields>…}.
	var runs []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) == nil && ev["event"] == "agent_run" {
			runs = append(runs, ev)
		}
	}
	if len(runs) != 2 {
		t.Fatalf("want 2 agent_run events, got %d", len(runs))
	}
	if _, claimed := runs[0]["resumed"]; claimed {
		t.Errorf("first attach run was logged as a resume: %v", runs[0])
	}
	if runs[1]["resumed"] != true {
		t.Errorf("second run genuinely resumed but was not logged as one: %v", runs[1])
	}
	// The session the work landed in must still be recorded, so an operator can
	// find this invocation in the TUI.
	if runs[0]["attach_session"] != fake.specs[0].ResumeSessionID {
		t.Errorf("attach_session = %v, want %q", runs[0]["attach_session"], fake.specs[0].ResumeSessionID)
	}
}

func TestAbortDoesNotDropStoredSession(t *testing.T) {
	srv := newAttachServer(t)
	fake := &attachRunner{}
	inv := attachTestInvoker(t, fake, "opencode", newTestManager(t, srv))
	if err := runAttachCoder(inv); err != nil {
		t.Fatal(err)
	}
	minted := fake.specs[0].ResumeSessionID

	fake.aborted = true
	if err := runAttachCoder(inv); !errors.Is(err, ErrAgentAborted) {
		t.Fatalf("want ErrAgentAborted, got %v", err)
	}

	fake.aborted = false
	if err := runAttachCoder(inv); err != nil {
		t.Fatal(err)
	}
	if got := fake.specs[2].ResumeSessionID; got != minted {
		t.Fatalf("session after abort = %q, want the original child %q", got, minted)
	}
}
