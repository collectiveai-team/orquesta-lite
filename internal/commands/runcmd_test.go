package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/agenthealth"
	"github.com/lionelchamorro/orquestalite/internal/config"
	"github.com/lionelchamorro/orquestalite/internal/eventlog"
	"github.com/lionelchamorro/orquestalite/internal/fallback"
	"github.com/lionelchamorro/orquestalite/internal/invoke"
	"github.com/lionelchamorro/orquestalite/internal/loops"
	"github.com/lionelchamorro/orquestalite/internal/runner"
	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

// fakeCLI is a POSIX shell script that acts as every role's agent.
// It accepts: -p <prompt> --result <path> --kind <role>
// and writes an appropriate result JSON to <path>.
const fakeCLI = `#!/bin/sh
# args: -p <prompt> --result <path> --kind <coder|tester|critic|reviewer|parser>
prompt="$2"
shift 2
kind=""
result=""
while [ $# -gt 0 ]; do
  case "$1" in
    --result) result="$2"; shift 2 ;;
    --kind)   kind="$2";   shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$(dirname "$result")"
case "$kind" in
  coder)    printf '%s' '{"status":"completed","summary":"ok","files_changed":["x.txt"],"notes_for_memory":null}' > "$result" ;;
  tester)   printf '%s' '{"status":"pass","command_run":"true","failures":[],"notes_for_memory":null}' > "$result" ;;
  critic)   printf '%s' '{"status":"approved","concerns":[],"notes_for_memory":null}' > "$result" ;;
  reviewer) printf '%s' '{"summary_of_cycle":"done","new_tasks":[],"should_stop":true,"notes_for_memory":null}' > "$result" ;;
  parser)   printf '%s' '{"tasks":[{"title":"t","description":"d","priority":1}],"notes_for_memory":null}' > "$result" ;;
esac
exit 0
`

// writeFakeTeamJSON writes a team.json that routes all five roles to fakecli.sh
// with role-specific --result and --kind flags.
//
// Each role gets its own agent so that each agent's cmd carries a unique
// --result path. Every agent's cmd includes {{PROMPT}} (via the -p flag)
// to satisfy config validation.
func writeFakeTeamJSON(t *testing.T, dir, cliPath string) {
	t.Helper()

	// Absolute result paths so the shell script writes to the right place
	// regardless of working directory.
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
		resultPath := filepath.Join(resultDir, kind+".json")
		return agentDef{
			Cmd: []string{
				"sh", cliPath,
				"-p", "{{PROMPT}}",
				"--result", resultPath,
				"--kind", kind,
			},
		}
	}

	// Relative result paths for config (used by ParseX calls which join dir+resultPath).
	relResult := func(kind string) string {
		return filepath.Join(".orquestalite", "results", kind+".json")
	}

	team := teamJSON{
		Agents: map[string]agentDef{
			"fake_coder":    makeAgent("coder"),
			"fake_tester":   makeAgent("tester"),
			"fake_critic":   makeAgent("critic"),
			"fake_reviewer": makeAgent("reviewer"),
			"fake_parser":   makeAgent("parser"),
		},
		Roles: map[string]roleDef{
			"coder":    {Agents: []string{"fake_coder"}, Prompt: "prompts/coder.md", ResultPath: relResult("coder"), TimeoutSeconds: 30},
			"tester":   {Agents: []string{"fake_tester"}, Prompt: "prompts/tester.md", ResultPath: relResult("tester"), TimeoutSeconds: 30},
			"critic":   {Agents: []string{"fake_critic"}, Prompt: "prompts/critic.md", ResultPath: relResult("critic"), TimeoutSeconds: 30},
			"reviewer": {Agents: []string{"fake_reviewer"}, Prompt: "prompts/reviewer.md", ResultPath: relResult("reviewer"), TimeoutSeconds: 30},
			"parser":   {Agents: []string{"fake_parser"}, Prompt: "prompts/parser.md", ResultPath: relResult("parser"), TimeoutSeconds: 30},
		},
		Limits: map[string]int{
			"max_review_cycles":  1,
			"max_fix_iterations": 3,
		},
		RateLimitBackoff: map[string]any{
			"initial_seconds": 1,
			"factor":          2,
			"max_seconds":     4,
			"default_pattern": "rate_?limit",
		},
		FullTestCommand: "true",
	}

	raw, err := json.MarshalIndent(team, "", "  ")
	if err != nil {
		t.Fatalf("marshal team.json: %v", err)
	}
	teamPath := filepath.Join(dir, "team.json")
	if err := os.WriteFile(teamPath, raw, 0o644); err != nil {
		t.Fatalf("write team.json: %v", err)
	}
}

type decomposeArchiveRunner struct{}

func (decomposeArchiveRunner) Run(_ context.Context, spec runner.Spec) (*runner.Result, error) {
	if err := os.MkdirAll(filepath.Dir(spec.ResultPath), 0o755); err != nil {
		return nil, err
	}
	raw := []byte(`{"tasks":[{"title":"smaller","description":"smaller task","priority":1}],"notes_for_memory":null}`)
	if err := os.WriteFile(spec.ResultPath, raw, 0o644); err != nil {
		return nil, err
	}
	return &runner.Result{ResultExists: true, ExitCode: 0, Duration: time.Millisecond}, nil
}

type reviewerPromptRunner struct {
	prompt string
}

func (r *reviewerPromptRunner) Run(_ context.Context, spec runner.Spec) (*runner.Result, error) {
	r.prompt = spec.Prompt
	if err := os.MkdirAll(filepath.Dir(spec.ResultPath), 0o755); err != nil {
		return nil, err
	}
	raw := []byte(`{"summary_of_cycle":"done","new_tasks":[],"should_stop":true,"notes_for_memory":null}`)
	if err := os.WriteFile(spec.ResultPath, raw, 0o644); err != nil {
		return nil, err
	}
	return &runner.Result{ResultExists: true, ExitCode: 0, Duration: time.Millisecond}, nil
}

func TestRunReviewerPassesCycleBaseSHAToPrompt(t *testing.T) {
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

	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "reviewer.md"), []byte("base={{CYCLE_BASE_SHA}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	tasksPath := filepath.Join(dir, ".orquestalite", "tasks.json")
	if err := os.MkdirAll(filepath.Dir(tasksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tasksPath, []byte(`{"tasks":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := &reviewerPromptRunner{}
	d := &liveDeps{
		dir:       dir,
		tasksPath: tasksPath,
		memPath:   filepath.Join(dir, ".orquestalite", "memory.md"),
		inv: &invoke.RoleInvoker{
			Dir:     dir,
			MemPath: filepath.Join(dir, ".orquestalite", "memory.md"),
			Runner:  runner,
			Specs: map[string]config.RoleSpec{
				"reviewer": {
					Agents: []config.AgentSpec{{
						Name: "fake_reviewer",
						Cmd:  []string{"fake"},
					}},
					PromptPath: "prompts/reviewer.md",
					ResultPath: ".orquestalite/results/reviewer.json",
					Timeout:    time.Minute,
				},
			},
		},
	}

	base, err := d.CycleBaseSHA(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(base) != 40 {
		t.Fatalf("CycleBaseSHA = %q, want full git SHA", base)
	}
	if _, err := d.RunReviewer(context.Background(), invoke.RunContext{Cycle: 1, Attempt: 1, CycleBaseSHA: base}, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runner.prompt, "base="+base) {
		t.Fatalf("reviewer prompt missing cycle base SHA: %q", runner.prompt)
	}
}

func TestDecomposeArchivesPerSourceTaskAtSameAttempt(t *testing.T) {
	dir := t.TempDir()
	promptPath := filepath.Join(dir, "prompts", "parser-decompose.md")
	if err := os.MkdirAll(filepath.Dir(promptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(promptPath, []byte("decompose {{TASK_ID}}"), 0o644); err != nil {
		t.Fatal(err)
	}

	deps := &liveDeps{
		dir:     dir,
		memPath: filepath.Join(dir, ".orquestalite", "memory.md"),
		inv: &invoke.RoleInvoker{
			Dir:     dir,
			MemPath: filepath.Join(dir, ".orquestalite", "memory.md"),
			Runner:  decomposeArchiveRunner{},
			Specs: map[string]config.RoleSpec{
				"parser": {
					Agents: []config.AgentSpec{{
						Name: "fake_parser",
						Cmd:  []string{"fake"},
					}},
					PromptPath:      "prompts/parser.md",
					ResultPath:      ".orquestalite/results/parser.json",
					DecomposePrompt: "prompts/parser-decompose.md",
					Timeout:         time.Minute,
				},
			},
		},
	}

	for _, taskID := range []string{"T001", "T002"} {
		task := &tasks.Task{ID: taskID, Title: "large task", Description: "break this up", Priority: 1}
		got, err := deps.Decompose(
			context.Background(),
			task,
			&loops.FixResult{LastFeedback: "agent repeated failure"},
			nil,
			invoke.RunContext{TaskID: taskID, Cycle: 3, Attempt: 2},
		)
		if err != nil {
			t.Fatalf("Decompose(%s): %v", taskID, err)
		}
		if len(got) != 1 {
			t.Fatalf("Decompose(%s) returned %d subtasks, want 1", taskID, len(got))
		}
	}

	for _, taskID := range []string{"T001", "T002"} {
		archivePath := filepath.Join(
			dir,
			".orquestalite",
			"results",
			"by-task",
			taskID,
			"parser-decompose.c3.a2.json",
		)
		if _, err := os.Stat(archivePath); err != nil {
			t.Fatalf("archive for %s missing: %v", taskID, err)
		}
	}
}

func TestRunStaticAgentPreflightScopesToRequestedRoles(t *testing.T) {
	dir := t.TempDir()
	log, err := eventlog.Open(filepath.Join(dir, "run.log"), io.Discard)
	if err != nil {
		t.Fatalf("open logger: %v", err)
	}
	defer log.Close()

	missingBinary := func(name string) string {
		return filepath.Join(dir, name)
	}
	cfg := &config.Config{
		Agents: map[string]config.Agent{
			"parser_primary":    {Cmd: []string{missingBinary("parser-primary")}},
			"parser_escalated":  {Cmd: []string{missingBinary("parser-escalated")}},
			"coder_only":        {Cmd: []string{missingBinary("coder-only")}},
			"reviewer_only":     {Cmd: []string{missingBinary("reviewer-only")}},
			"unreferenced_only": {Cmd: []string{missingBinary("unreferenced-only")}},
		},
		Roles: map[string]config.Role{
			"parser": {
				Agents:           []string{"parser_primary"},
				EscalationLadder: []string{"parser_escalated"},
			},
			"coder": {
				Agents: []string{"coder_only"},
			},
			"reviewer": {
				Agents: []string{"reviewer_only"},
			},
		},
	}

	tracker := agenthealth.New(agentHealthThreshold)
	runStaticAgentPreflight(cfg, tracker, log, []string{"parser"})

	for _, name := range []string{"parser_primary", "parser_escalated"} {
		if reason, ok := tracker.IsSkipped(name); !ok || reason != agenthealth.ReasonUnreachable {
			t.Fatalf("%s skipped = (%q, %v), want (%q, true)", name, reason, ok, agenthealth.ReasonUnreachable)
		}
	}
	for _, name := range []string{"coder_only", "reviewer_only", "unreferenced_only"} {
		if reason, ok := tracker.IsSkipped(name); ok {
			t.Fatalf("%s was skipped with reason %q; want untouched", name, reason)
		}
	}
}

func TestNewLiveDepsLoadsEmptyTasksAndCleanupClosesLog(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell script test, skipping on windows")
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".orquestalite", "results"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "parser.md"), []byte("parse {{PLAN}}"), 0o644); err != nil {
		t.Fatal(err)
	}

	cliPath := filepath.Join(dir, "fakecli.sh")
	if err := os.WriteFile(cliPath, []byte(fakeCLI), 0o755); err != nil {
		t.Fatalf("write fakecli.sh: %v", err)
	}
	writePlanPreflightTeamJSON(t, dir, cliPath)

	deps, cleanup, err := newLiveDeps(liveDepsOptions{
		ProjectDir: dir,
		TeamPath:   filepath.Join(dir, "team.json"),
		Roles:      []string{"parser"},
	})
	if err != nil {
		t.Fatalf("newLiveDeps: %v", err)
	}
	if deps.tl == nil {
		t.Fatal("task list is nil")
	}
	if len(deps.tl.Tasks) != 0 {
		t.Fatalf("loaded %d tasks, want empty list", len(deps.tl.Tasks))
	}

	deps.log.Log(eventlog.Event{Type: "before_cleanup", Fields: map[string]any{}})
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	deps.log.Log(eventlog.Event{Type: "after_cleanup", Fields: map[string]any{}})

	raw, err := os.ReadFile(filepath.Join(dir, ".orquestalite", "run.log"))
	if err != nil {
		t.Fatalf("read run.log: %v", err)
	}
	log := string(raw)
	if !strings.Contains(log, `"event":"before_cleanup"`) {
		t.Fatalf("run.log missing event before cleanup:\n%s", log)
	}
	if strings.Contains(log, `"event":"after_cleanup"`) {
		t.Fatalf("logger still wrote after cleanup:\n%s", log)
	}
}

// TestRun_EndToEndWithFakeCLI drives a full review→task→fix cycle using a
// fake CLI script that writes fixed result JSONs. It verifies that after Run
// returns, T001 is marked done and a git commit was made.
func TestRun_EndToEndWithFakeCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell script test, skipping on windows")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	dir := t.TempDir()

	// Initialise a git repo with an empty commit so HEAD exists.
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

	// Scaffold the .orquestalite directory and default prompts.
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Write the fake CLI script.
	cliPath := filepath.Join(dir, "fakecli.sh")
	if err := os.WriteFile(cliPath, []byte(fakeCLI), 0o755); err != nil {
		t.Fatalf("write fakecli.sh: %v", err)
	}

	// Overwrite team.json with the fake-CLI version (Init already wrote one).
	writeFakeTeamJSON(t, dir, cliPath)

	// Pre-seed tasks.json with one pending task.
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Title: "demo task", Description: "a demo task", Status: tasks.StatusPending, Priority: 1},
	}}
	raw, _ := json.MarshalIndent(tl, "", "  ")
	tasksPath := filepath.Join(dir, ".orquestalite", "tasks.json")
	if err := os.WriteFile(tasksPath, raw, 0o644); err != nil {
		t.Fatalf("write tasks.json: %v", err)
	}

	// Run the orchestrator.
	err := Run(context.Background(), RunOptions{
		ProjectDir: dir,
		TeamPath:   filepath.Join(dir, "team.json"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify T001 is now done.
	final, err := tasks.Load(tasksPath)
	if err != nil {
		t.Fatalf("load tasks after run: %v", err)
	}
	if len(final.Tasks) == 0 {
		t.Fatal("no tasks in list after run")
	}
	found := false
	for _, task := range final.Tasks {
		if task.ID == "T001" {
			found = true
			if task.Status != tasks.StatusDone {
				t.Errorf("T001 status = %q, want %q", task.Status, tasks.StatusDone)
			}
		}
	}
	if !found {
		t.Fatal("T001 not found in final task list")
	}

	// Verify a new commit was made (git log should show more than just "init").
	c := exec.Command("git", "log", "--oneline")
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	logLines := string(out)
	// Should have at least 2 lines: the init commit + the task commit.
	count := 0
	for _, line := range []byte(logLines) {
		if line == '\n' {
			count++
		}
	}
	if count < 1 {
		t.Errorf("expected at least 2 git commits, log:\n%s", logLines)
	}
}

// TestRun_EmptyTaskList verifies that Run returns nil when there are no tasks
// to process (the reviewer immediately signals stop).
func TestRun_EmptyTaskList(t *testing.T) {
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

	writeFakeTeamJSON(t, dir, cliPath)

	// Empty task list — only the reviewer will run, and it signals stop.
	tl := &tasks.TaskList{Tasks: []tasks.Task{}}
	raw, _ := json.MarshalIndent(tl, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, ".orquestalite", "tasks.json"), raw, 0o644); err != nil {
		t.Fatalf("write tasks.json: %v", err)
	}

	err := Run(context.Background(), RunOptions{
		ProjectDir: dir,
		TeamPath:   filepath.Join(dir, "team.json"),
	})
	if err != nil {
		t.Errorf("Run with empty task list: %v", err)
	}
}

// TestCallRole_AgentRunEventFields verifies that callRole logs the new fields
// (stderr_tail, stdout_tail, exit_code, cmd_line) in the agent_run event and
// that when the agent does not write a result file the error message includes
// exit code and stderr tail.
func TestCallRole_AgentRunEventFields(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell script test, skipping on windows")
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "run.log")

	logger, err := eventlog.Open(logPath, os.Stderr)
	if err != nil {
		t.Fatalf("open logger: %v", err)
	}
	defer logger.Close()

	// Agent that writes stderr, exits non-zero, and does NOT write the result file.
	resultPath := filepath.Join(dir, "result.json")
	agentScript := fmt.Sprintf(
		`sh -c "echo 'something went wrong' 1>&2; exit 7"`,
	)
	_ = agentScript // used below via config

	ag := config.Agent{
		Cmd: []string{"sh", "-c", "echo 'something went wrong' 1>&2; exit 7"},
	}
	role := config.Role{
		Agents:         []string{"testagent"},
		Prompt:         "prompts/dummy.md",
		ResultPath:     "result.json",
		TimeoutSeconds: 5,
	}
	cfg := &config.Config{
		Agents: map[string]config.Agent{"testagent": ag},
		Roles:  map[string]config.Role{"testrole": role},
		RateLimitBackoff: config.RateLimitBackoff{
			InitialSeconds: 1,
			Factor:         2,
			MaxSeconds:     2,
			DefaultPattern: "rate_?limit",
		},
	}

	fc := fallback.NewCaller(fallback.Config{
		InitialBackoff: 0,
		Factor:         1,
		MaxBackoff:     0,
	})

	deps := &liveDeps{
		cfg:     cfg,
		dir:     dir,
		fc:      fc,
		log:     logger,
		memPath: filepath.Join(dir, "memory.md"),
	}

	callErr := deps.callRole(context.Background(), "testrole", "the prompt text", role)

	// Close logger so the file is fully flushed.
	_ = logger.Close()

	// Error message must include exit code and stderr tail.
	if callErr == nil {
		t.Fatal("expected error from callRole when result file absent")
	}
	errMsg := callErr.Error()
	if !strings.Contains(errMsg, "exit=7") {
		t.Errorf("error missing exit code: %q", errMsg)
	}
	if !strings.Contains(errMsg, "something went wrong") {
		t.Errorf("error missing stderr content: %q", errMsg)
	}

	// Parse the JSONL log and find the agent_run event.
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	var agentRunEvent map[string]any
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	for sc.Scan() {
		var rec map[string]any
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		if rec["event"] == "agent_run" {
			agentRunEvent = rec
			break
		}
	}
	if agentRunEvent == nil {
		t.Fatalf("no agent_run event found in log:\n%s", raw)
	}

	// Verify required new fields are present.
	for _, field := range []string{"stderr_tail", "stdout_tail", "exit_code", "cmd_line"} {
		if _, ok := agentRunEvent[field]; !ok {
			t.Errorf("agent_run event missing field %q; event: %v", field, agentRunEvent)
		}
	}

	// exit_code must be 7.
	if got := agentRunEvent["exit_code"]; fmt.Sprintf("%v", got) != "7" {
		t.Errorf("exit_code = %v, want 7", got)
	}

	// cmd_line must not contain the prompt text but must contain <elided>.
	cmdLine, _ := agentRunEvent["cmd_line"].(string)
	if strings.Contains(cmdLine, "the prompt text") {
		t.Errorf("cmd_line must not contain raw prompt: %q", cmdLine)
	}

	// stderr_tail must contain the agent's stderr output.
	stderrTail, _ := agentRunEvent["stderr_tail"].(string)
	if !strings.Contains(stderrTail, "something went wrong") {
		t.Errorf("stderr_tail missing agent stderr: %q", stderrTail)
	}

	// resultPath is used to ensure the result file path is correct.
	_ = resultPath
}

// TestCallRole_FallsBackOnResultMissing verifies that when the first agent in a
// two-agent chain does not write a result file, callRole falls back to the second
// agent (which does write the file) and returns nil.
// It also asserts that two agent_run events appear in the log, in order.
func TestCallRole_FallsBackOnResultMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell script test, skipping on windows")
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "run.log")

	logger, err := eventlog.Open(logPath, os.Stderr)
	if err != nil {
		t.Fatalf("open logger: %v", err)
	}
	defer logger.Close()

	resultPath := filepath.Join(dir, "result.json")
	// Script that accepts --agent <name> and writes result only for "agent_b".
	scriptPath := filepath.Join(dir, "agent.sh")
	script := fmt.Sprintf(`#!/bin/sh
agent=""
while [ $# -gt 0 ]; do
  case "$1" in
    --agent) agent="$2"; shift 2 ;;
    *) shift ;;
  esac
done
if [ "$agent" = "agent_b" ]; then
  mkdir -p "$(dirname '%s')"
  printf '{}' > '%s'
fi
exit 0
`, resultPath, resultPath)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	role := config.Role{
		Agents:         []string{"agent_a", "agent_b"},
		Prompt:         "prompts/dummy.md",
		ResultPath:     "result.json",
		TimeoutSeconds: 5,
	}
	cfg := &config.Config{
		Agents: map[string]config.Agent{
			"agent_a": {Cmd: []string{"sh", scriptPath, "--agent", "agent_a"}},
			"agent_b": {Cmd: []string{"sh", scriptPath, "--agent", "agent_b"}},
		},
		Roles: map[string]config.Role{"testrole": role},
		RateLimitBackoff: config.RateLimitBackoff{
			InitialSeconds: 1,
			Factor:         2,
			MaxSeconds:     4,
			DefaultPattern: "rate_?limit",
		},
	}

	fc := fallback.NewCaller(fallback.Config{
		InitialBackoff: 0,
		Factor:         2,
		MaxBackoff:     0,
	})

	deps := &liveDeps{
		cfg:     cfg,
		dir:     dir,
		fc:      fc,
		log:     logger,
		memPath: filepath.Join(dir, "memory.md"),
	}

	callErr := deps.callRole(context.Background(), "testrole", "prompt", role)
	_ = logger.Close()

	if callErr != nil {
		t.Fatalf("expected nil error after fallback, got: %v", callErr)
	}

	// Parse log and check two agent_run events in order.
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	var agentNames []string
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	for sc.Scan() {
		var rec map[string]any
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		if rec["event"] == "agent_run" {
			if name, ok := rec["agent"].(string); ok {
				agentNames = append(agentNames, name)
			}
		}
	}

	if len(agentNames) != 2 {
		t.Fatalf("expected 2 agent_run events, got %d: %v", len(agentNames), agentNames)
	}
	if agentNames[0] != "agent_a" || agentNames[1] != "agent_b" {
		t.Errorf("agent_run order = %v, want [agent_a, agent_b]", agentNames)
	}
}

// TestCallRole_AllAgentsFailedError verifies that when every agent in the chain
// fails to write a result file, callRole returns an error matching the format:
// all agents failed for role %q: tried [...]; last error: ...
func TestCallRole_AllAgentsFailedError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell script test, skipping on windows")
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "run.log")

	logger, err := eventlog.Open(logPath, os.Stderr)
	if err != nil {
		t.Fatalf("open logger: %v", err)
	}
	defer logger.Close()

	// Agent that never writes a result file.
	scriptPath := filepath.Join(dir, "no_result.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	role := config.Role{
		Agents:         []string{"agent_a", "agent_b"},
		Prompt:         "prompts/dummy.md",
		ResultPath:     "result.json",
		TimeoutSeconds: 5,
	}
	cfg := &config.Config{
		Agents: map[string]config.Agent{
			"agent_a": {Cmd: []string{"sh", scriptPath}},
			"agent_b": {Cmd: []string{"sh", scriptPath}},
		},
		Roles: map[string]config.Role{"testrole": role},
		RateLimitBackoff: config.RateLimitBackoff{
			InitialSeconds: 1,
			Factor:         2,
			MaxSeconds:     4,
			DefaultPattern: "rate_?limit",
		},
	}

	fc := fallback.NewCaller(fallback.Config{
		InitialBackoff: 0,
		Factor:         2,
		MaxBackoff:     0,
	})

	deps := &liveDeps{
		cfg:     cfg,
		dir:     dir,
		fc:      fc,
		log:     logger,
		memPath: filepath.Join(dir, "memory.md"),
	}

	callErr := deps.callRole(context.Background(), "testrole", "prompt", role)
	_ = logger.Close()

	if callErr == nil {
		t.Fatal("expected error when all agents fail, got nil")
	}
	errMsg := callErr.Error()
	if !strings.Contains(errMsg, `all agents failed for role "testrole"`) {
		t.Errorf("error missing expected prefix: %q", errMsg)
	}
	if !strings.Contains(errMsg, "agent_a") || !strings.Contains(errMsg, "agent_b") {
		t.Errorf("error missing agent names: %q", errMsg)
	}
	if !strings.Contains(errMsg, "last error:") {
		t.Errorf("error missing 'last error:' segment: %q", errMsg)
	}
}

// TestCallRole_PassesTemplateVarsToRunner verifies that callRole sets the
// canonical Phase-3 TemplateVars (RESULT_PATH, ROLE) on the runner.Spec.
// It uses a real shell agent whose cmd contains {{RESULT_PATH}} and {{ROLE}}
// tokens; the script echoes the resolved values so we can assert substitution.
func TestCallRole_PassesTemplateVarsToRunner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell script test, skipping on windows")
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "run.log")

	logger, err := eventlog.Open(logPath, os.Stderr)
	if err != nil {
		t.Fatalf("open logger: %v", err)
	}
	defer logger.Close()

	resultPath := filepath.Join(dir, "result.json")

	// The agent script: echoes its two args (resolved from {{RESULT_PATH}} and
	// {{ROLE}}), then writes the result file so callRole succeeds.
	scriptPath := filepath.Join(dir, "capture.sh")
	script := fmt.Sprintf(`#!/bin/sh
echo "result_path_arg=$1"
echo "role_arg=$2"
printf '{}' > '%s'
exit 0
`, resultPath)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	role := config.Role{
		Agents:         []string{"capagent"},
		Prompt:         "prompts/dummy.md",
		ResultPath:     "result.json",
		TimeoutSeconds: 5,
	}
	cfg := &config.Config{
		Agents: map[string]config.Agent{
			// The cmd uses {{RESULT_PATH}} and {{ROLE}} as positional args.
			"capagent": {Cmd: []string{"sh", scriptPath, "{{RESULT_PATH}}", "{{ROLE}}"}},
		},
		Roles: map[string]config.Role{"testrole": role},
		RateLimitBackoff: config.RateLimitBackoff{
			InitialSeconds: 1,
			Factor:         2,
			MaxSeconds:     4,
			DefaultPattern: "rate_?limit",
		},
	}

	fc := fallback.NewCaller(fallback.Config{
		InitialBackoff: 0,
		Factor:         1,
		MaxBackoff:     0,
	})

	deps := &liveDeps{
		cfg:     cfg,
		dir:     dir,
		fc:      fc,
		log:     logger,
		memPath: filepath.Join(dir, "memory.md"),
	}

	callErr := deps.callRole(context.Background(), "testrole", "prompt", role)
	_ = logger.Close()

	if callErr != nil {
		t.Fatalf("unexpected error from callRole: %v", callErr)
	}

	// Read the log to get stdout_tail which contains what the script echoed.
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	var stdoutTail string
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	for sc.Scan() {
		var rec map[string]any
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		if rec["event"] == "agent_run" {
			stdoutTail, _ = rec["stdout_tail"].(string)
			break
		}
	}

	// RESULT_PATH should be the resolved absolute path (dir + "result.json").
	wantResultPath := filepath.Join(dir, "result.json")
	if !strings.Contains(stdoutTail, "result_path_arg="+wantResultPath) {
		t.Errorf("stdout_tail missing resolved RESULT_PATH; want %q in: %q", wantResultPath, stdoutTail)
	}

	// ROLE should be "testrole".
	if !strings.Contains(stdoutTail, "role_arg=testrole") {
		t.Errorf("stdout_tail missing resolved ROLE; want %q in: %q", "role_arg=testrole", stdoutTail)
	}

	// Fix 3: cmd_line in the agent_run event must have {{RESULT_PATH}} and {{ROLE}}
	// replaced with their resolved values — not the raw placeholder strings.
	raw2, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log (fix3 check): %v", err)
	}
	var cmdLineField string
	sc2 := bufio.NewScanner(strings.NewReader(string(raw2)))
	for sc2.Scan() {
		var rec map[string]any
		if json.Unmarshal(sc2.Bytes(), &rec) != nil {
			continue
		}
		if rec["event"] == "agent_run" {
			cmdLineField, _ = rec["cmd_line"].(string)
			break
		}
	}
	if strings.Contains(cmdLineField, "{{RESULT_PATH}}") {
		t.Errorf("cmd_line must not contain raw {{RESULT_PATH}} placeholder: %q", cmdLineField)
	}
	if strings.Contains(cmdLineField, "{{ROLE}}") {
		t.Errorf("cmd_line must not contain raw {{ROLE}} placeholder: %q", cmdLineField)
	}
	if !strings.Contains(cmdLineField, wantResultPath) {
		t.Errorf("cmd_line must contain resolved RESULT_PATH %q; got: %q", wantResultPath, cmdLineField)
	}
	if !strings.Contains(cmdLineField, "testrole") {
		t.Errorf("cmd_line must contain resolved ROLE \"testrole\"; got: %q", cmdLineField)
	}
}

func TestExecRunnerMatchesRunAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell script test, skipping on windows")
	}

	var _ AgentRunner = execRunner{}

	dir := t.TempDir()
	resultPath := filepath.Join(dir, "result.json")
	spec := runner.Spec{
		Cmd: []string{
			"sh",
			"-c",
			`printf '%s\n' "stdout:$1"; printf '%s\n' "stderr:$1" >&2; printf '%s' '{"status":"ok"}' > "$2"`,
			"agent-script",
			"{{PROMPT}}",
			resultPath,
		},
		Prompt:           "hello",
		ResultPath:       resultPath,
		Timeout:          5 * time.Second,
		RateLimitPattern: "rate_?limit",
	}

	direct, err := runner.RunAgent(context.Background(), spec)
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	adapted, err := execRunner{}.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("execRunner.Run: %v", err)
	}

	if direct.Duration <= 0 {
		t.Fatalf("RunAgent duration = %v, want positive", direct.Duration)
	}
	if adapted.Duration <= 0 {
		t.Fatalf("execRunner duration = %v, want positive", adapted.Duration)
	}
	direct.Duration = 0
	adapted.Duration = 0

	if !reflect.DeepEqual(adapted, direct) {
		t.Fatalf("execRunner result mismatch\nadapted: %#v\ndirect:  %#v", *adapted, *direct)
	}
}

// TestRunFix_EscalationLadderPlumbedFromConfig verifies that the escalation_ladder
// field in team.json is correctly read and plumbed into FixConfig.EscalationLadder
// by liveDeps.RunFix.
func TestRunFix_EscalationLadderPlumbedFromConfig(t *testing.T) {
	cfg := &config.Config{
		Agents: map[string]config.Agent{
			"fake_coder":      {Cmd: []string{"sh", "-c", "exit 0"}},
			"fake_escalation": {Cmd: []string{"sh", "-c", "exit 0"}},
		},
		Roles: map[string]config.Role{
			"coder": {
				Agents:           []string{"fake_coder"},
				EscalationLadder: []string{"fake_escalation"},
				Prompt:           "prompts/coder.md",
				ResultPath:       ".orquestalite/results/coder.json",
				TimeoutSeconds:   5,
			},
		},
		Limits: config.Limits{MaxReviewCycles: 1, MaxFixIterations: 5},
		RateLimitBackoff: config.RateLimitBackoff{
			InitialSeconds: 1, Factor: 2, MaxSeconds: 4, DefaultPattern: "rate_?limit",
		},
		FullTestCommand: "true",
	}

	d := &liveDeps{
		cfg:       cfg,
		tl:        &tasks.TaskList{Tasks: []tasks.Task{{ID: "T1", Title: "t", Description: "d"}}},
		memPath:   t.TempDir() + "/memory.md",
		tasksPath: t.TempDir() + "/tasks.json",
	}

	// Verify that the escalation_ladder from the coder role is accessible.
	coderRole := d.cfg.Roles["coder"]
	if len(coderRole.EscalationLadder) != 1 || coderRole.EscalationLadder[0] != "fake_escalation" {
		t.Errorf("EscalationLadder not plumbed correctly: %v", coderRole.EscalationLadder)
	}
}

// TestCallRole_TimedOutReportsAgentCrashed verifies that when the first agent
// times out (TimedOut=true, ResultExists=false), the fallback reason logged is
// "agent_crashed" (not "result_missing"), and the chain advances to the second
// agent which succeeds.
func TestCallRole_TimedOutReportsAgentCrashed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell script test, skipping on windows")
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "run.log")

	logger, err := eventlog.Open(logPath, os.Stderr)
	if err != nil {
		t.Fatalf("open logger: %v", err)
	}
	defer logger.Close()

	resultPath := filepath.Join(dir, "result.json")

	// agent_b script: writes the result file and exits quickly.
	scriptBPath := filepath.Join(dir, "agent_b.sh")
	scriptB := fmt.Sprintf(`#!/bin/sh
mkdir -p "$(dirname '%s')"
printf '{}' > '%s'
exit 0
`, resultPath, resultPath)
	if err := os.WriteFile(scriptBPath, []byte(scriptB), 0o755); err != nil {
		t.Fatalf("write agent_b script: %v", err)
	}

	// agent_a uses exec directly (no shell wrapper) so that SIGKILL from
	// exec.CommandContext reaches the sleeping process immediately, avoiding
	// orphan grandchildren that keep pipes open.
	role := config.Role{
		Agents:         []string{"agent_a", "agent_b"},
		Prompt:         "prompts/dummy.md",
		ResultPath:     "result.json",
		TimeoutSeconds: 1, // short timeout so agent_a is killed quickly
	}
	cfg := &config.Config{
		Agents: map[string]config.Agent{
			// sleep is invoked directly (no sh wrapper) so SIGKILL is sent
			// straight to the sleep process; pipes close immediately.
			"agent_a": {Cmd: []string{"sleep", "30"}},
			"agent_b": {Cmd: []string{"sh", scriptBPath}},
		},
		Roles: map[string]config.Role{"testrole": role},
		RateLimitBackoff: config.RateLimitBackoff{
			InitialSeconds: 1,
			Factor:         2,
			MaxSeconds:     4,
			DefaultPattern: "rate_?limit",
		},
	}

	fc := fallback.NewCaller(fallback.Config{
		InitialBackoff: 0,
		Factor:         2,
		MaxBackoff:     0,
	})

	deps := &liveDeps{
		cfg:     cfg,
		dir:     dir,
		fc:      fc,
		log:     logger,
		memPath: filepath.Join(dir, "memory.md"),
	}

	callErr := deps.callRole(context.Background(), "testrole", "prompt", role)
	_ = logger.Close()

	// The fallback must advance to agent_b successfully.
	if callErr != nil {
		t.Fatalf("expected nil error after fallback to agent_b, got: %v", callErr)
	}

	// Parse the JSONL log and find the agent_run event for agent_a.
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	var agentAEvent map[string]any
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	for sc.Scan() {
		var rec map[string]any
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		if rec["event"] == "agent_run" && rec["agent"] == "agent_a" {
			agentAEvent = rec
			break
		}
	}
	if agentAEvent == nil {
		t.Fatalf("no agent_run event for agent_a found in log:\n%s", raw)
	}

	// timed_out must be true.
	if timedOut, _ := agentAEvent["timed_out"].(bool); !timedOut {
		t.Errorf("agent_a agent_run event: timed_out = %v, want true", agentAEvent["timed_out"])
	}

	// fallback_reason must be "agent_crashed", NOT "result_missing".
	if reason, _ := agentAEvent["fallback_reason"].(string); reason != "agent_crashed" {
		t.Errorf("agent_a agent_run event: fallback_reason = %q, want \"agent_crashed\"", reason)
	}
}

func newLintGateDeps(t *testing.T, lintCmd string) *liveDeps {
	t.Helper()
	dir := t.TempDir()
	logger, err := eventlog.Open(filepath.Join(dir, "run.log"), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	return &liveDeps{
		dir: dir,
		cfg: &config.Config{LintCommand: lintCmd}, // no test command: isolates the lint gate
		log: logger,
	}
}

func TestFullSuite_LintGateBlocksOnViolation(t *testing.T) {
	d := newLintGateDeps(t, "false") // `false` exits 1 = lint found problems
	err := d.FullSuite(context.Background())
	if err == nil {
		t.Fatal("expected lint failure to block the gate")
	}
	if !errors.Is(err, loops.ErrFullSuiteFailed) {
		t.Fatalf("lint failure should map to ErrFullSuiteFailed, got %v", err)
	}
}

func TestFullSuite_LintGateSkipsWhenBinaryMissing(t *testing.T) {
	d := newLintGateDeps(t, "orq-lite-no-such-linter-9f3a check .")
	if err := d.FullSuite(context.Background()); err != nil {
		t.Fatalf("a missing lint binary must be skipped, not block, got %v", err)
	}
}

func TestFullSuite_LintGatePassesThrough(t *testing.T) {
	d := newLintGateDeps(t, "true") // `true` exits 0 = clean
	if err := d.FullSuite(context.Background()); err != nil {
		t.Fatalf("clean lint + no test command should pass, got %v", err)
	}
}
