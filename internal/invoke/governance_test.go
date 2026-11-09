package invoke

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/config"
	"github.com/collectiveai-team/orquesta-lite/internal/promptbudget"
	"github.com/collectiveai-team/orquesta-lite/internal/providers"
	"github.com/collectiveai-team/orquesta-lite/internal/runner"
	"github.com/collectiveai-team/orquesta-lite/internal/sessions"
	"github.com/collectiveai-team/orquesta-lite/internal/supervision"
	"github.com/collectiveai-team/orquesta-lite/internal/telemetry"
)

type governanceRunner struct {
	specs []runner.Spec
	tools int
}

func (r *governanceRunner) Run(_ context.Context, s runner.Spec) (*runner.Result, error) {
	r.specs = append(r.specs, s)
	out := &runner.Result{FinalText: `{"ok":true}`, ExitCode: 0, Events: []providers.Event{{Type: providers.EventUsage, Usage: map[string]int{"input_tokens": 100, "cached_input_tokens": 50, "output_tokens": 10}}}}
	for i := 0; i < r.tools; i++ {
		event := providers.Event{Type: providers.EventToolCall, ToolName: "Read"}
		out.Events = append(out.Events, event)
		if s.ObserveEvent != nil {
			if err := s.ObserveEvent(event); err != nil {
				out.StopReason = err
				break
			}
		}
	}
	return out, nil
}
func governanceInvoker(t *testing.T, r *governanceRunner) *RoleInvoker {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "review.md"), []byte("Review {{OBJECTIVE}} and return {{RESULT_PATH}}"), 0600); err != nil {
		t.Fatal(err)
	}
	return &RoleInvoker{Dir: dir, Runner: r, TelemetryRoot: filepath.Join(dir, "telemetry"), Specs: map[string]config.RoleSpec{"review": {Agents: []config.AgentSpec{{Name: "reviewer", Provider: "codex", Model: "gpt-5", SkipPerms: true}}, PromptPath: "review.md", ResultPath: "result.json", Timeout: time.Minute}}}
}
func TestReadonlyResultWrittenByRuntimeOutsideCandidate(t *testing.T) {
	r := &governanceRunner{}
	inv := governanceInvoker(t, r)
	candidate := t.TempDir()
	out, err := Raw(context.Background(), inv, "review", RoleCall{Dir: candidate, ReadOnly: true, FreshContext: true, ResultPath: "result.json", Vars: map[string]string{"OBJECTIVE": "check candidate"}}, RunContext{TaskID: "T"}, func(raw []byte) error {
		if !json.Valid(raw) {
			return errors.New("invalid")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Output) != `{"ok":true}` || len(r.specs) != 1 || !r.specs[0].ReadOnly || r.specs[0].ResumeSessionID != "" {
		t.Fatalf("unexpected result: %+v %+v", out, r.specs)
	}
	if _, err := os.Stat(filepath.Join(candidate, "result.json")); !os.IsNotExist(err) {
		t.Fatal("checkpoint written inside candidate")
	}
	entries, err := os.ReadDir(candidate)
	if err != nil || len(entries) != 0 {
		t.Fatalf("runtime mutated frozen candidate: %v %v", entries, err)
	}
	calls, err := telemetry.Read(filepath.Join(inv.TelemetryRoot, "usage.jsonl"))
	if err != nil || len(calls) != 1 {
		t.Fatalf("usage missing: %v", err)
	}
}

func TestSingleCorrectionNeverRetriesWriter(t *testing.T) {
	for _, step := range []contractRetryStep{
		{resultExists: true, body: `{"status":"invalid"}`},
		{resultExists: false},
		{rateLimited: true},
		{timedOut: true},
	} {
		r := &scriptedContractRunner{steps: []contractRetryStep{step, {resultExists: true, body: `{"status":"ok"}`}}}
		inv, _ := contractRetryInvoker(t, r, []config.AgentSpec{{Name: "first", Provider: "codex"}, {Name: "second", Provider: "codex"}}, nil)
		if _, err := Raw(context.Background(), inv, "coder", RoleCall{SingleAttempt: true}, RunContext{TaskID: "correction"}, statusOKValidate); err == nil {
			t.Fatal("failed single correction was accepted")
		}
		if len(r.specs) != 1 {
			t.Fatalf("single correction launched %d writers", len(r.specs))
		}
	}
}

func TestReadonlyDefaultCheckpointStaysOutsideCandidate(t *testing.T) {
	r := &governanceRunner{}
	inv := governanceInvoker(t, r)
	candidate := t.TempDir()
	if _, err := Raw(context.Background(), inv, "review", RoleCall{Dir: candidate, ReadOnly: true}, RunContext{TaskID: "default"}, nil); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(candidate)
	if err != nil || len(entries) != 0 {
		t.Fatalf("snapshot changed: %v %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(inv.Dir, "result.json")); err != nil {
		t.Fatal(err)
	}
}

func TestReadonlyRelativeProjectPathsRemainAnchored(t *testing.T) {
	r := &governanceRunner{}
	inv := governanceInvoker(t, r)
	project := inv.Dir
	t.Chdir(project)
	inv.Dir = "."
	candidate := t.TempDir()
	if _, err := Raw(context.Background(), inv, "review", RoleCall{Dir: candidate, ReadOnly: true}, RunContext{TaskID: "relative"}, nil); err != nil {
		t.Fatal(err)
	}
	if r.specs[0].ResultPath != filepath.Join(project, "result.json") {
		t.Fatalf("relative result escaped project: %s", r.specs[0].ResultPath)
	}
	entries, err := os.ReadDir(candidate)
	if err != nil || len(entries) > 0 {
		t.Fatalf("frozen tree changed: %v %v", entries, err)
	}
}

func TestReadonlyCannotChangeSnapshotDirectoryOrSandbox(t *testing.T) {
	for _, arg := range []string{"--cd=/live", "-C/live", "-sworkspace-write", "--sandbox=workspace-write"} {
		r := &governanceRunner{}
		inv := governanceInvoker(t, r)
		spec := inv.Specs["review"]
		spec.Agents[0].ExtraArgs = []string{arg}
		inv.Specs["review"] = spec
		if _, err := Raw(context.Background(), inv, "review", RoleCall{ReadOnly: true}, RunContext{}, nil); err == nil {
			t.Fatalf("accepted unsafe readonly argument %s", arg)
		}
		if len(r.specs) > 0 {
			t.Fatal("unsafe reviewer launched")
		}
	}
}

func TestNativeDelegationWithoutModelControlRequiresDecision(t *testing.T) {
	inv := &RoleInvoker{DisallowNativeDelegation: true}
	observe := inv.toolObserver("coder", RunContext{TaskID: "unit"})
	for _, name := range []string{"Agent", "Task", "spawn_agent", "native_agent"} {
		if err := observe(providers.Event{Type: providers.EventToolCall, ToolName: name}); !errors.Is(err, supervision.ErrCheckpointRequired) {
			t.Fatalf("native child %s accepted: %v", name, err)
		}
	}
	if err := observe(providers.Event{Type: providers.EventToolCall, ToolName: "Read"}); err != nil {
		t.Fatal(err)
	}
}

func TestCorrectionObserverLimitsDoNotDoubleCountUsage(t *testing.T) {
	inv := &RoleInvoker{MaxTokens: 100, MaxToolCalls: 2}
	observe := inv.toolObserver("integrator", RunContext{TaskID: "unit"})
	usage := map[string]int{"input_tokens": 30, "cached_input_tokens": 5, "output_tokens": 5}
	for _, kind := range []providers.EventType{providers.EventPartialUsage, providers.EventUsage} {
		if err := observe(providers.Event{Type: kind, Usage: usage}); err != nil {
			t.Fatalf("partial/final usage double-counted: %v", err)
		}
	}
	if err := observe(providers.Event{Type: providers.EventToolCall, ToolName: "Read"}); err != nil {
		t.Fatal(err)
	}
	if err := observe(providers.Event{Type: providers.EventToolCall, ToolName: "Read"}); !errors.Is(err, ErrInvocationBudget) {
		t.Fatalf("tool ceiling not enforced: %v", err)
	}
	tokens := (&RoleInvoker{MaxTokens: 40}).toolObserver("integrator", RunContext{})
	if err := tokens(providers.Event{Type: providers.EventUsage, Usage: usage}); !errors.Is(err, ErrInvocationBudget) {
		t.Fatalf("token ceiling not enforced: %v", err)
	}
}

func TestCorrectionMissingUsageCannotValidateBudget(t *testing.T) {
	r := &scriptedContractRunner{steps: []contractRetryStep{{resultExists: true, body: `{"status":"ok"}`}}}
	inv, _ := contractRetryInvoker(t, r, []config.AgentSpec{{Name: "writer", Provider: "codex"}}, nil)
	if _, err := Raw(context.Background(), inv, "coder", RoleCall{SingleAttempt: true, MaxTokens: 1000}, RunContext{TaskID: "repair"}, statusOKValidate); !errors.Is(err, ErrInvocationBudget) {
		t.Fatalf("missing usage accepted: %v", err)
	}
	if len(r.specs) != 1 {
		t.Fatalf("budget failure relaunched writer %d times", len(r.specs))
	}
}
func TestStreamingCheckpointPreservesSpend(t *testing.T) {
	r := &governanceRunner{tools: 3}
	inv := governanceInvoker(t, r)
	inv.Supervision = supervision.Policy{MaxToolCalls: 2}
	out, err := Raw(context.Background(), inv, "review", RoleCall{ReadOnly: true, ResultPath: "result.json"}, RunContext{TaskID: "T"}, nil)
	if !errors.Is(err, supervision.ErrCheckpointRequired) || out.CostUSD <= 0 {
		t.Fatalf("checkpoint lost or spend dropped: %+v %v", out, err)
	}
	if len(r.specs) != 1 {
		t.Fatal("checkpoint triggered fallback")
	}
}
func TestPromptBudgetStopsBeforeProvider(t *testing.T) {
	r := &governanceRunner{}
	inv := governanceInvoker(t, r)
	inv.PromptBudgets = map[string]promptbudget.Policy{"review": {MaxTokens: 1}}
	if _, err := Raw(context.Background(), inv, "review", RoleCall{}, RunContext{}, nil); err == nil {
		t.Fatal("over-budget prompt launched")
	}
	if len(r.specs) != 0 {
		t.Fatal("provider invoked before budget check")
	}
}
func TestTelemetryPartialAndFinalNotDoubleCounted(t *testing.T) {
	inv := &RoleInvoker{TelemetryRoot: t.TempDir()}
	r := &runner.Result{Events: []providers.Event{{Type: providers.EventPartialUsage, Usage: map[string]int{"input_tokens": 10, "output_tokens": 3}}, {Type: providers.EventUsage, Usage: map[string]int{"input_tokens": 10, "output_tokens": 3}}}}
	if err := inv.recordUsage("review", "a", config.AgentSpec{Provider: "claude", Model: "claude-opus-4"}, runner.Spec{Prompt: "stable"}, r, RunContext{TaskID: "T"}, "invocation"); err != nil {
		t.Fatal(err)
	}
	calls, err := telemetry.Read(filepath.Join(inv.TelemetryRoot, "usage.jsonl"))
	if err != nil || len(calls) != 1 || calls[0].Input != 10 {
		t.Fatalf("double count: %+v %v", calls, err)
	}
}

func TestAcknowledgedCheckpointUsesNewSessionNamespace(t *testing.T) {
	r := &governanceRunner{}
	inv := governanceInvoker(t, r)
	inv.Sessions = sessions.Load(inv.Dir)
	inv.ResumeRoles = map[string]bool{"review": true}
	if err := inv.Sessions.Set("T", "review", "reviewer", "old-session"); err != nil {
		t.Fatal(err)
	}
	if err := supervision.Acknowledge(inv.TelemetryRoot, "T", "review", "continue", "audit completed in fresh context"); err != nil {
		t.Fatal(err)
	}
	local, err := acknowledgedInvoker(inv, "review", "T")
	if err != nil {
		t.Fatal(err)
	}
	if got := local.resumeSessionID("review", "reviewer", "T"); got != "" {
		t.Fatalf("resumed pre-audit session %q", got)
	}
	if local.SessionNamespace == "" {
		t.Fatal("audit epoch missing")
	}
	again, err := acknowledgedInvoker(inv, "review", "T")
	if err != nil || again.SessionNamespace != local.SessionNamespace {
		t.Fatal("restart lost audit epoch")
	}
}

func TestSupervisorDecisionReachesFreshInvocation(t *testing.T) {
	for _, budgeted := range []bool{false, true} {
		r := &governanceRunner{}
		inv := governanceInvoker(t, r)
		if budgeted {
			inv.PromptBudgets = map[string]promptbudget.Policy{"review": {MaxTokens: 10000}}
		}
		if err := supervision.Acknowledge(inv.TelemetryRoot, "task", "review", "replan", "inspect ownership before editing"); err != nil {
			t.Fatal(err)
		}
		if _, err := Raw(context.Background(), inv, "review", RoleCall{ReadOnly: true}, RunContext{TaskID: "task"}, nil); err != nil {
			t.Fatal(err)
		}
		prompt := r.specs[0].Prompt
		if !strings.Contains(prompt, "inspect ownership before editing") || !strings.Contains(prompt, `"action":"replan"`) {
			t.Fatalf("decision missing from fresh invocation: %s", prompt)
		}
		if budgeted && strings.Index(prompt, "inspect ownership before editing") < strings.Index(prompt, "<run_context>") {
			t.Fatal("supervisor decision leaked into stable prefix")
		}
	}
}

func TestNativeModelControlAndUnsupportedReadonlyFailBeforeLaunch(t *testing.T) {
	for _, native := range []bool{true, false} {
		r := &governanceRunner{}
		inv := governanceInvoker(t, r)
		inv.RequireNativeModelControl = native
		if !native {
			spec := inv.Specs["review"]
			spec.Agents[0].Provider = "claude"
			inv.Specs["review"] = spec
		}
		if _, err := Raw(context.Background(), inv, "review", RoleCall{ReadOnly: !native}, RunContext{}, nil); err == nil {
			t.Fatal("unsupported guarantee accepted")
		}
		if len(r.specs) != 0 {
			t.Fatal("unsupported provider launched")
		}
	}
}
