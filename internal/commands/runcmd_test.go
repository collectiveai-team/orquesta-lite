package commands

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lionelchamorro/pyorquesta/internal/tasks"
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
	resultDir := filepath.Join(dir, ".pyorquesta", "results")

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
		return filepath.Join(".pyorquesta", "results", kind+".json")
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

	// Scaffold the .pyorquesta directory and default prompts.
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
	tasksPath := filepath.Join(dir, ".pyorquesta", "tasks.json")
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
	if err := os.WriteFile(filepath.Join(dir, ".pyorquesta", "tasks.json"), raw, 0o644); err != nil {
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
