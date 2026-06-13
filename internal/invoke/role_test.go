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

	"github.com/lionelchamorro/orquestalite/internal/agenthealth"
	"github.com/lionelchamorro/orquestalite/internal/config"
	"github.com/lionelchamorro/orquestalite/internal/eventlog"
	"github.com/lionelchamorro/orquestalite/internal/fallback"
	"github.com/lionelchamorro/orquestalite/internal/runner"
)

type fakeRoleResult struct {
	Status         string  `json:"status"`
	NotesForMemory *string `json:"notes_for_memory"`
}

func (r fakeRoleResult) MemoryNote() *string { return r.NotesForMemory }

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

func TestRoleRunsArchivesParsesAndAppendsMemory(t *testing.T) {
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
	logger, err := eventlog.Open(filepath.Join(dir, ".orquestalite", "run.log"), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	fake := &fakeAgentRunner{}
	inv := &RoleInvoker{
		Specs: map[string]config.RoleSpec{
			"tester": {
				Agents: []config.AgentSpec{{
					Name: "agent1",
					Cmd:  []string{"fake", "{{PROMPT}}"},
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
	if !strings.Contains(fake.specs[0].Prompt, "memory=prior note") || !strings.Contains(fake.specs[0].Prompt, "task=T123") {
		t.Fatalf("prompt was not interpolated with memory and vars: %q", fake.specs[0].Prompt)
	}

	archivePath := filepath.Join(dir, ".orquestalite", "results", "by-task", "T123", "tester.c4.a2.json")
	if _, err := os.Stat(archivePath); err != nil {
		t.Fatalf("archive missing: %v", err)
	}

	memRaw, err := os.ReadFile(memPath)
	if err != nil {
		t.Fatal(err)
	}
	mem := string(memRaw)
	if !strings.Contains(mem, "## [cycle 4, task T123, tester]") || !strings.Contains(mem, "remember this") {
		t.Fatalf("memory note not appended with run context: %q", mem)
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
