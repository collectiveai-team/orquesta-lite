package commands

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

// writeFakeTeamJSONFullSuite writes a fake-CLI team.json whose merge-gate
// full_test_command is an arbitrary script, with a configurable fix-iteration
// cap, so a full-suite failure path can be exercised deterministically.
func writeFakeTeamJSONFullSuite(t *testing.T, dir, cliPath, fullTestCmd string, maxFix int) {
	t.Helper()
	resultDir := filepath.Join(dir, ".orquestalite", "results")
	type agentDef struct {
		Cmd []string `json:"cmd"`
	}
	type roleDef struct {
		Agents         []string `json:"agents"`
		Prompt         string   `json:"prompt"`
		ResultPath     string   `json:"result_path"`
		TimeoutSeconds int      `json:"timeout_seconds"`
	}
	type teamJSON struct {
		Agents           map[string]agentDef `json:"agents"`
		Roles            map[string]roleDef  `json:"roles"`
		Limits           map[string]int      `json:"limits"`
		RateLimitBackoff map[string]any      `json:"rate_limit_backoff"`
		FullTestCommand  string              `json:"full_test_command"`
	}
	makeAgent := func(kind string) agentDef {
		return agentDef{Cmd: []string{"sh", cliPath, "-p", "{{PROMPT}}", "--result", filepath.Join(resultDir, kind+".json"), "--kind", kind}}
	}
	rel := func(kind string) string { return filepath.Join(".orquestalite", "results", kind+".json") }
	team := teamJSON{
		Agents: map[string]agentDef{
			"fake_coder": makeAgent("coder"), "fake_tester": makeAgent("tester"),
			"fake_critic": makeAgent("critic"), "fake_reviewer": makeAgent("reviewer"),
			"fake_parser": makeAgent("parser"),
		},
		Roles: map[string]roleDef{
			"coder":    {Agents: []string{"fake_coder"}, Prompt: "prompts/coder.md", ResultPath: rel("coder"), TimeoutSeconds: 30},
			"tester":   {Agents: []string{"fake_tester"}, Prompt: "prompts/tester.md", ResultPath: rel("tester"), TimeoutSeconds: 30},
			"critic":   {Agents: []string{"fake_critic"}, Prompt: "prompts/critic.md", ResultPath: rel("critic"), TimeoutSeconds: 30},
			"reviewer": {Agents: []string{"fake_reviewer"}, Prompt: "prompts/reviewer.md", ResultPath: rel("reviewer"), TimeoutSeconds: 30},
			"parser":   {Agents: []string{"fake_parser"}, Prompt: "prompts/parser.md", ResultPath: rel("parser"), TimeoutSeconds: 30},
		},
		Limits:           map[string]int{"max_review_cycles": 1, "max_fix_iterations": maxFix},
		RateLimitBackoff: map[string]any{"initial_seconds": 1, "factor": 2, "max_seconds": 4, "default_pattern": "rate_?limit"},
		FullTestCommand:  fullTestCmd,
	}
	raw, err := json.MarshalIndent(team, "", "  ")
	if err != nil {
		t.Fatalf("marshal team.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "team.json"), raw, 0o644); err != nil {
		t.Fatalf("write team.json: %v", err)
	}
}

// TestRunFastBatch_FullSuiteFailureRetriesWithFeedback proves the fix for the
// blind-repair bug: when the merge-gate suite fails (something the per-feature
// tester scopes out), the coder must be retried with the actual suite output as
// feedback — not bailed on after a single attempt with a generic message.
//
// The fake coder/tester/critic always "pass", and the full_test_command always
// fails with a unique marker. Pre-fix, the loop returned after the first
// full-suite failure: the coder ran once and never saw the output. Post-fix, the
// loop retries up to max_fix_iterations, surfacing the marker each time.
func TestRunFastBatch_FullSuiteFailureRetriesWithFeedback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell script test, skipping on windows")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test User"},
		{"config", "commit.gpgsign", "false"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}

	cliPath := filepath.Join(dir, "fakecli.sh")
	if err := os.WriteFile(cliPath, []byte(fakeCLI), 0o755); err != nil {
		t.Fatalf("write fakecli.sh: %v", err)
	}

	const marker = "SUITE_MARKER_7f3a9c FAILED test/test_docker.py::test_real"
	failScript := filepath.Join(dir, "failsuite.sh")
	if err := os.WriteFile(failScript, []byte("#!/bin/sh\necho '"+marker+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write failsuite.sh: %v", err)
	}

	const maxFix = 2
	writeFakeTeamJSONFullSuite(t, dir, cliPath, failScript, maxFix)

	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Title: "demo", Description: "demo task", Status: tasks.StatusPending, Priority: 1},
	}}
	raw, _ := json.MarshalIndent(tl, "", "  ")
	tasksPath := filepath.Join(dir, ".orquestalite", "tasks.json")
	if err := os.WriteFile(tasksPath, raw, 0o644); err != nil {
		t.Fatalf("write tasks.json: %v", err)
	}

	err := Run(context.Background(), RunOptions{
		ProjectDir: dir,
		TeamPath:   filepath.Join(dir, "team.json"),
		FastMode:   true,
	})
	if err == nil {
		t.Fatal("expected fast batch to fail when the full suite fails")
	}

	logRaw, rerr := os.ReadFile(filepath.Join(dir, ".orquestalite", "run.log"))
	if rerr != nil {
		t.Fatalf("read run.log: %v", rerr)
	}
	log := string(logRaw)

	// Retry happened: the coder ran once per fix iteration, not just once.
	if got := strings.Count(log, `"role":"coder"`); got != maxFix {
		t.Errorf("coder ran %d times, want %d (full-suite failure must retry in-loop, not bail)", got, maxFix)
	}
	// The new per-attempt event is emitted each iteration.
	if got := strings.Count(log, `"event":"feature_fast_full_suite_failed"`); got != maxFix {
		t.Errorf("feature_fast_full_suite_failed logged %d times, want %d", got, maxFix)
	}

	// The actual suite output reached the coder's feedback (persisted as the
	// failed task's LastFeedback), instead of a generic "full test suite failed".
	final, lerr := tasks.Load(tasksPath)
	if lerr != nil {
		t.Fatalf("load tasks: %v", lerr)
	}
	if len(final.Tasks) == 0 || final.Tasks[0].LastFeedback == nil {
		t.Fatal("expected failed task to carry feedback")
	}
	if !strings.Contains(*final.Tasks[0].LastFeedback, marker) {
		t.Errorf("task feedback missing the suite output marker; got:\n%s", *final.Tasks[0].LastFeedback)
	}
}
