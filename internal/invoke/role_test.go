package invoke

import (
	"context"
	"encoding/json"
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
	"github.com/collectiveai-team/orquesta-lite/internal/providers"
	"github.com/collectiveai-team/orquesta-lite/internal/runner"
	"github.com/collectiveai-team/orquesta-lite/internal/sessions"
)

type fakeRoleResult struct {
	Status         string  `json:"status"`
	NotesForMemory *string `json:"notes_for_memory"`
}

func (r fakeRoleResult) MemoryNote() *string { return r.NotesForMemory }

// Role preserves the old typed test call shape while exercising the v2 Raw
// invocation path used by activity:agent.invoke@1.
func Role[T any](ctx context.Context, inv *RoleInvoker, roleName string, call RoleCall, rc RunContext, parse func(path string) (*T, error)) (*T, error) {
	resultPath := call.ResultPath
	if resultPath == "" {
		resultPath = inv.Specs[roleName].ResultPath
	}
	var parsed *T
	_, err := Raw(ctx, inv, roleName, call, rc, func([]byte) error {
		value, parseErr := parse(absPath(inv.Dir, resultPath))
		if parseErr == nil {
			parsed = value
		}
		return parseErr
	})
	return parsed, err
}

type fakeAgentRunner struct {
	specs []runner.Spec
}

func (r *fakeAgentRunner) Run(_ context.Context, spec runner.Spec) (*runner.Result, error) {
	r.specs = append(r.specs, spec)
	if err := os.MkdirAll(filepath.Dir(spec.ResultPath), 0o755); err != nil {
		return nil, err
	}
	raw := []byte(`{"status":"ok","notes_for_memory":"remember this"}`)
	if err := os.WriteFile(spec.ResultPath, raw, 0o644); err != nil {
		return nil, err
	}
	return &runner.Result{ResultExists: true, ExitCode: 0, Duration: time.Second}, nil
}

type usageAgentRunner struct{}

func (usageAgentRunner) Run(_ context.Context, spec runner.Spec) (*runner.Result, error) {
	if err := os.MkdirAll(filepath.Dir(spec.ResultPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(spec.ResultPath, []byte(`{"status":"ok"}`), 0o644); err != nil {
		return nil, err
	}
	return &runner.Result{
		ResultExists: true,
		ExitCode:     0,
		Duration:     time.Second,
		Events: []providers.Event{{Type: providers.EventUsage, Usage: map[string]int{
			"input_tokens":                100,
			"cache_creation_input_tokens": 25,
			"cached_input_tokens":         10,
			"output_tokens":               40,
		}}},
	}, nil
}

func TestRawRunsArchivesParsesAndInjectsMemory(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "tester.md"), []byte("memory={{MEMORY}}\ntask={{TASK_ID}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	memPath := filepath.Join(dir, ".orquestalite", "memory.md")
	if err := os.MkdirAll(filepath.Dir(memPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memPath, []byte("prior note"), 0o644); err != nil {
		t.Fatal(err)
	}
	logger, err := eventlog.OpenWithFormat(filepath.Join(dir, ".orquestalite", "run.log"), io.Discard, eventlog.FormatVerbose)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	fake := &fakeAgentRunner{}
	inv := &RoleInvoker{
		Specs: map[string]config.RoleSpec{
			"tester": {
				Agents: []config.AgentSpec{{
					Name:     "agent1",
					Cmd:      []string{"fake", "{{PROMPT}}"},
					SafeMode: true,
				}},
				PromptPath: "prompts/tester.md",
				ResultPath: ".orquestalite/results/tester.json",
				Timeout:    time.Minute,
			},
		},
		Dir:      dir,
		Fallback: fallback.NewCaller(fallback.Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: time.Millisecond}),
		Log:      logger,
		Health:   agenthealth.New(2),
		MemPath:  memPath,
		Runner:   fake,
	}

	got, err := Role[fakeRoleResult](
		context.Background(),
		inv,
		"tester",
		RoleCall{Vars: map[string]string{"TASK_ID": "T123"}},
		RunContext{TaskID: "T123", Cycle: 4, Attempt: 2},
		func(path string) (*fakeRoleResult, error) {
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			var out fakeRoleResult
			return &out, json.Unmarshal(raw, &out)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "ok" {
		t.Fatalf("Status = %q, want ok", got.Status)
	}
	if len(fake.specs) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(fake.specs))
	}
	if !fake.specs[0].SafeMode {
		t.Fatal("safe mode was not propagated to runner spec")
	}
	if !strings.Contains(fake.specs[0].Prompt, "memory=prior note") || !strings.Contains(fake.specs[0].Prompt, "task=T123") {
		t.Fatalf("prompt was not interpolated with memory and vars: %q", fake.specs[0].Prompt)
	}

	archivePath := filepath.Join(dir, ".orquestalite", "results", "by-task", "T123", "tester.c4.a2.json")
	if _, err := os.Stat(archivePath); err != nil {
		t.Fatalf("archive missing: %v", err)
	}

}

func TestRunOnceLogsUsageTokens(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "tester.md"), []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, ".orquestalite", "run.log")
	logger, err := eventlog.OpenWithFormat(logPath, io.Discard, eventlog.FormatVerbose)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	inv := &RoleInvoker{
		Specs: map[string]config.RoleSpec{
			"tester": {
				Agents: []config.AgentSpec{{
					Name:     "agent1",
					Provider: "claude",
					Model:    "claude-sonnet-4-6",
				}},
				PromptPath: "prompts/tester.md",
				ResultPath: ".orquestalite/results/tester.json",
				Timeout:    time.Minute,
			},
		},
		Dir:      dir,
		Fallback: fallback.NewCaller(fallback.Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: time.Millisecond}),
		Log:      logger,
		MemPath:  filepath.Join(dir, ".orquestalite", "memory.md"),
		Runner:   usageAgentRunner{},
	}

	if err := inv.RunOnce(context.Background(), "tester", RoleCall{}, RunContext{TaskID: "T999", Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var ev struct {
		Event             string `json:"event"`
		InputTokens       int    `json:"input_tokens"`
		CachedInputTokens int    `json:"cached_input_tokens"`
		OutputTokens      int    `json:"output_tokens"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("parse log %q: %v", raw, err)
	}
	if ev.Event != "agent_run" || ev.InputTokens != 135 || ev.CachedInputTokens != 35 || ev.OutputTokens != 40 {
		t.Fatalf("event = %+v; raw=%s", ev, raw)
	}
}

func TestRoleCallUsesExplicitControlFields(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "default.md"), []byte("default {{TASK_ID}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "override.md"), []byte("override {{TASK_ID}}"), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := &fakeAgentRunner{}
	var successfulAgent string
	inv := &RoleInvoker{
		Specs: map[string]config.RoleSpec{
			"tester": {
				Agents: []config.AgentSpec{{
					Name: "default-agent",
					Cmd:  []string{"fake", "{{PROMPT}}"},
				}},
				EscalationLadder: []config.AgentSpec{{
					Name: "override-agent",
					Cmd:  []string{"fake", "{{PROMPT}}"},
				}},
				PromptPath: "prompts/default.md",
				ResultPath: ".orquestalite/results/default.json",
				Timeout:    time.Minute,
			},
		},
		Dir:      dir,
		Fallback: fallback.NewCaller(fallback.Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: time.Millisecond}),
		MemPath:  filepath.Join(dir, ".orquestalite", "memory.md"),
		Runner:   fake,
		OnAgentSuccess: func(_, agent string) {
			successfulAgent = agent
		},
	}

	_, err := Role[fakeRoleResult](
		context.Background(),
		inv,
		"tester",
		RoleCall{
			AgentOverride: "override-agent",
			ArchiveRole:   "tester-override",
			PromptPath:    "prompts/override.md",
			ResultPath:    ".orquestalite/results/override.json",
			Vars:          map[string]string{"TASK_ID": "T456"},
		},
		RunContext{TaskID: "T456", Cycle: 1, Attempt: 3},
		func(path string) (*fakeRoleResult, error) {
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			var out fakeRoleResult
			return &out, json.Unmarshal(raw, &out)
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if successfulAgent != "override-agent" {
		t.Fatalf("successful agent = %q, want override-agent", successfulAgent)
	}
	if len(fake.specs) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(fake.specs))
	}
	if fake.specs[0].ResultPath != filepath.Join(dir, ".orquestalite", "results", "override.json") {
		t.Fatalf("ResultPath = %q, want override result path", fake.specs[0].ResultPath)
	}
	if !strings.Contains(fake.specs[0].Prompt, "override T456") {
		t.Fatalf("prompt = %q, want override prompt with vars", fake.specs[0].Prompt)
	}
	archivePath := filepath.Join(dir, ".orquestalite", "results", "by-task", "T456", "tester-override.c1.a3.json")
	if _, err := os.Stat(archivePath); err != nil {
		t.Fatalf("archive missing: %v", err)
	}
}

func TestRoleInjectsConventionsFromFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "coder.md"), []byte("style:\n{{CONVENTIONS}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CONVENTIONS.md"), []byte("use snake_case everywhere"), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := &fakeAgentRunner{}
	inv := &RoleInvoker{
		Specs: map[string]config.RoleSpec{
			"coder": {
				Agents:     []config.AgentSpec{{Name: "a1", Cmd: []string{"fake", "{{PROMPT}}"}}},
				PromptPath: "prompts/coder.md",
				ResultPath: ".orquestalite/results/coder.json",
				Timeout:    time.Minute,
			},
		},
		Dir:             dir,
		Fallback:        fallback.NewCaller(fallback.Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: time.Millisecond}),
		MemPath:         filepath.Join(dir, ".orquestalite", "memory.md"),
		Runner:          fake,
		ConventionsPath: "CONVENTIONS.md",
	}

	if _, err := Role[fakeRoleResult](context.Background(), inv, "coder",
		RoleCall{Vars: map[string]string{}}, RunContext{TaskID: "T1", Cycle: 1, Attempt: 1},
		func(path string) (*fakeRoleResult, error) {
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			var out fakeRoleResult
			return &out, json.Unmarshal(raw, &out)
		}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fake.specs[0].Prompt, "use snake_case everywhere") {
		t.Fatalf("conventions not injected: %q", fake.specs[0].Prompt)
	}
}

func TestRoleConventionsFallbackWhenUnset(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "coder.md"), []byte("{{CONVENTIONS}}"), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := &fakeAgentRunner{}
	inv := &RoleInvoker{
		Specs: map[string]config.RoleSpec{
			"coder": {
				Agents:     []config.AgentSpec{{Name: "a1", Cmd: []string{"fake", "{{PROMPT}}"}}},
				PromptPath: "prompts/coder.md",
				ResultPath: ".orquestalite/results/coder.json",
				Timeout:    time.Minute,
			},
		},
		Dir:      dir,
		Fallback: fallback.NewCaller(fallback.Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: time.Millisecond}),
		MemPath:  filepath.Join(dir, ".orquestalite", "memory.md"),
		Runner:   fake,
		// ConventionsPath unset.
	}

	if _, err := Role[fakeRoleResult](context.Background(), inv, "coder",
		RoleCall{Vars: map[string]string{}}, RunContext{TaskID: "T1", Cycle: 1, Attempt: 1},
		func(path string) (*fakeRoleResult, error) {
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			var out fakeRoleResult
			return &out, json.Unmarshal(raw, &out)
		}); err != nil {
		t.Fatal(err)
	}
	// The literal marker must be replaced, and the fallback guidance present.
	if strings.Contains(fake.specs[0].Prompt, "{{CONVENTIONS}}") {
		t.Fatalf("marker left un-interpolated: %q", fake.specs[0].Prompt)
	}
	if !strings.Contains(fake.specs[0].Prompt, "infer the house style") {
		t.Fatalf("fallback guidance missing: %q", fake.specs[0].Prompt)
	}
}

func TestRoleInjectsSkillsFromSkillsDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "coder.md"), []byte("skills:\n{{SKILLS}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "tdd.md"), []byte("---\nname: tdd\ndescription: TDD.\n---\nred green refactor"), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := &fakeAgentRunner{}
	inv := &RoleInvoker{
		Specs: map[string]config.RoleSpec{
			"coder": {
				Agents:     []config.AgentSpec{{Name: "a1", Cmd: []string{"fake", "{{PROMPT}}"}}},
				PromptPath: "prompts/coder.md",
				ResultPath: ".orquestalite/results/coder.json",
				Timeout:    time.Minute,
			},
		},
		Dir:      dir,
		Fallback: fallback.NewCaller(fallback.Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: time.Millisecond}),
		MemPath:  filepath.Join(dir, ".orquestalite", "memory.md"),
		Runner:   fake,
	}

	if _, err := Role[fakeRoleResult](context.Background(), inv, "coder",
		RoleCall{Skills: []string{"tdd"}, Vars: map[string]string{}}, RunContext{TaskID: "T1", Cycle: 1, Attempt: 1},
		func(path string) (*fakeRoleResult, error) {
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			var out fakeRoleResult
			return &out, json.Unmarshal(raw, &out)
		}); err != nil {
		t.Fatal(err)
	}
	prompt := fake.specs[0].Prompt
	if strings.Contains(prompt, "{{SKILLS}}") {
		t.Fatalf("marker left un-interpolated: %q", prompt)
	}
	for _, want := range []string{"Skill: tdd", "red green refactor"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("skills not injected: missing %q in %q", want, prompt)
		}
	}
}

func TestRoleSkillsPlaceholderWhenNoneRequested(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "coder.md"), []byte("{{SKILLS}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAgentRunner{}
	inv := &RoleInvoker{
		Specs: map[string]config.RoleSpec{
			"coder": {
				Agents:     []config.AgentSpec{{Name: "a1", Cmd: []string{"fake", "{{PROMPT}}"}}},
				PromptPath: "prompts/coder.md",
				ResultPath: ".orquestalite/results/coder.json",
				Timeout:    time.Minute,
			},
		},
		Dir:      dir,
		Fallback: fallback.NewCaller(fallback.Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: time.Millisecond}),
		MemPath:  filepath.Join(dir, ".orquestalite", "memory.md"),
		Runner:   fake,
	}
	if _, err := Role[fakeRoleResult](context.Background(), inv, "coder",
		RoleCall{Vars: map[string]string{}}, RunContext{TaskID: "T1", Cycle: 1, Attempt: 1},
		func(path string) (*fakeRoleResult, error) {
			raw, _ := os.ReadFile(path)
			var out fakeRoleResult
			return &out, json.Unmarshal(raw, &out)
		}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fake.specs[0].Prompt, "no skills requested") {
		t.Fatalf("placeholder missing: %q", fake.specs[0].Prompt)
	}
}

func TestRoleMissingSkillIsClearError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "coder.md"), []byte("{{SKILLS}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No skills/ directory at all.
	fake := &fakeAgentRunner{}
	inv := &RoleInvoker{
		Specs: map[string]config.RoleSpec{
			"coder": {
				Agents:     []config.AgentSpec{{Name: "a1", Cmd: []string{"fake", "{{PROMPT}}"}}},
				PromptPath: "prompts/coder.md",
				ResultPath: ".orquestalite/results/coder.json",
				Timeout:    time.Minute,
			},
		},
		Dir:      dir,
		Fallback: fallback.NewCaller(fallback.Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: time.Millisecond}),
		MemPath:  filepath.Join(dir, ".orquestalite", "memory.md"),
		Runner:   fake,
	}
	_, err := Role[fakeRoleResult](context.Background(), inv, "coder",
		RoleCall{Skills: []string{"nope"}, Vars: map[string]string{}}, RunContext{TaskID: "T1", Cycle: 1, Attempt: 1},
		func(path string) (*fakeRoleResult, error) {
			raw, _ := os.ReadFile(path)
			var out fakeRoleResult
			return &out, json.Unmarshal(raw, &out)
		})
	if err == nil {
		t.Fatal("expected error for missing skill, got nil")
	}
	if !strings.Contains(err.Error(), `"nope" not found`) {
		t.Fatalf("error not clear about missing skill: %v", err)
	}
	if len(fake.specs) != 0 {
		t.Fatalf("agent must not run when a skill is missing, got %d calls", len(fake.specs))
	}
}

func TestSessionTaskKey_NamespacesByFeature(t *testing.T) {
	if got := (&RoleInvoker{}).sessionTaskKey("T001"); got != "T001" {
		t.Errorf("no namespace: got %q, want T001", got)
	}
	if got := (&RoleInvoker{SessionNamespace: "F002"}).sessionTaskKey("T001"); got != "F002/T001" {
		t.Errorf("namespaced: got %q, want F002/T001", got)
	}
}

func TestSessionNamespaceIsolatesFeatures(t *testing.T) {
	dir := t.TempDir()
	st := sessions.Load(dir)
	invA := &RoleInvoker{Sessions: st, ResumeRoles: map[string]bool{"coder": true}, SessionNamespace: "F001"}
	invB := &RoleInvoker{Sessions: st, ResumeRoles: map[string]bool{"coder": true}, SessionNamespace: "F002"}
	if err := st.Set(invA.sessionTaskKey("T001"), "coder", "claude_sonnet", "sid-A"); err != nil {
		t.Fatal(err)
	}
	if got := invB.resumeSessionID("coder", "claude_sonnet", "T001"); got != "" {
		t.Errorf("F002 must not see F001's session, got %q", got)
	}
	if got := invA.resumeSessionID("coder", "claude_sonnet", "T001"); got != "sid-A" {
		t.Errorf("F001 should resume its own session, got %q", got)
	}
}
