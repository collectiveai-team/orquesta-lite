package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

// fakeCLIStaged is a shell script that reads pre-staged result files by invocation count.
// Each role maintains a counter, and the script reads <resultsDir>/<role>/NNN.json in order.
const fakeCLIStaged = `#!/bin/sh
results_dir=""
state_dir=""
kind=""
result=""

while [ $# -gt 0 ]; do
  case "$1" in
    --results) results_dir="$2"; shift 2 ;;
    --state)   state_dir="$2";   shift 2 ;;
    --kind)    kind="$2";        shift 2 ;;
    --result)  result="$2";      shift 2 ;;
    -p)        shift 2 ;;
    *)         shift ;;
  esac
done

mkdir -p "$state_dir"
counter_file="$state_dir/${kind}.count"
n=0
if [ -f "$counter_file" ]; then n=$(cat "$counter_file"); fi
n=$((n + 1))
printf '%d' "$n" > "$counter_file"

padded=$(printf '%03d' "$n")
src="${results_dir}/${kind}/${padded}.json"
if [ ! -f "$src" ]; then
  src=$(ls "${results_dir}/${kind}/"*.json 2>/dev/null | sort | tail -1)
fi

mkdir -p "$(dirname "$result")"
cp "$src" "$result"
exit 0
`

// Result JSON templates.
const (
	coderDone    = `{"status":"completed","summary":"done","files_changed":[],"notes_for_memory":null}`
	coderBlocked = `{"status":"blocked","summary":"blocked","files_changed":[],"notes_for_memory":null}`

	testerPass = `{"status":"pass","command_run":"true","failures":[],"notes_for_memory":null}`
	testerFail = `{"status":"fail","command_run":"true","failures":[{"test":"TestFoo","message":"expected 1 got 0","hint":""}],"notes_for_memory":null}`
	// testerFail2/3 produce distinct failure hashes so the stuck detector (threshold=2) does
	// not fire when we want to exercise the max_iterations path with 3 attempts.
	testerFail2 = `{"status":"fail","command_run":"true","failures":[{"test":"TestFoo","message":"expected 2 got 0","hint":""}],"notes_for_memory":null}`
	testerFail3 = `{"status":"fail","command_run":"true","failures":[{"test":"TestFoo","message":"expected 3 got 0","hint":""}],"notes_for_memory":null}`

	criticApprove = `{"status":"approved","concerns":[],"notes_for_memory":null}`
	criticReject  = `{"status":"rejected","concerns":[{"severity":"blocker","where":"x.go:1","issue":"bad","suggestion":"fix it"}],"notes_for_memory":null}`

	reviewerStop       = `{"summary_of_cycle":"done","new_tasks":[],"should_stop":true,"notes_for_memory":null}`
	reviewerProposeTwo = `{"summary_of_cycle":"cycle 1","new_tasks":[{"title":"task-3","description":"desc-3","priority":1},{"title":"task-4","description":"desc-4","priority":2}],"should_stop":false,"notes_for_memory":null}`
	reviewerProposeOne = `{"summary_of_cycle":"cycle 1","new_tasks":[{"title":"task-3","description":"desc-3","priority":1}],"should_stop":false,"notes_for_memory":null}`
)

type e2eScenario struct {
	name         string
	initialTasks []tasks.Task
	resultsSeq   map[string][]string // role -> ordered JSON results
	maxFixIter   int
	maxCycles    int
	assertFn     func(t *testing.T, dir string, events []map[string]any)
}

func stageRoleResults(t *testing.T, resultsDir, role string, jsons []string) {
	t.Helper()
	roleDir := filepath.Join(resultsDir, role)
	if err := os.MkdirAll(roleDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", roleDir, err)
	}
	for i, j := range jsons {
		path := filepath.Join(roleDir, fmt.Sprintf("%03d.json", i+1))
		if err := os.WriteFile(path, []byte(j), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

func writeE2ETeamJSON(t *testing.T, dir, cliPath, resultsDir, stateDir string, maxFix, maxCycles int) {
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
		resultPath := filepath.Join(resultDir, kind+".json")
		return agentDef{
			Cmd: []string{
				"sh", cliPath,
				"--results", resultsDir,
				"--state", stateDir,
				"-p", "{{PROMPT}}",
				"--result", resultPath,
				"--kind", kind,
			},
		}
	}

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
			"max_review_cycles":  maxCycles,
			"max_fix_iterations": maxFix,
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

func parseRunLog(t *testing.T, dir string) []map[string]any {
	t.Helper()
	logPath := filepath.Join(dir, ".orquestalite", "run.log")
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read run.log: %v", err)
	}
	var events []map[string]any
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		events = append(events, ev)
	}
	return events
}

func countAgentRuns(events []map[string]any, role string) int {
	count := 0
	for _, ev := range events {
		if evType, ok := ev["event"].(string); ok && evType == "agent_run" {
			if r, ok := ev["role"].(string); ok && r == role {
				count++
			}
		}
	}
	return count
}

func countTaskDones(events []map[string]any) int {
	count := 0
	for _, ev := range events {
		if evType, ok := ev["event"].(string); ok && evType == "task_done" {
			count++
		}
	}
	return count
}

func loadFinalTasks(t *testing.T, dir string) *tasks.TaskList {
	t.Helper()
	tasksPath := filepath.Join(dir, ".orquestalite", "tasks.json")
	tl, err := tasks.Load(tasksPath)
	if err != nil {
		t.Fatalf("load tasks.json: %v", err)
	}
	return tl
}

func countGitCommits(t *testing.T, dir string) int {
	t.Helper()
	c := exec.Command("git", "log", "--oneline")
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func setupE2EGitRepo(t *testing.T, dir string) {
	t.Helper()
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
}

func findTask(tl *tasks.TaskList, id string) *tasks.Task {
	for i, t := range tl.Tasks {
		if t.ID == id {
			return &tl.Tasks[i]
		}
	}
	return nil
}

func TestRun_E2EScenarios(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell script test, skipping on windows")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	scenarios := []e2eScenario{
		{
			name: "happy_path",
			initialTasks: []tasks.Task{
				{ID: "T001", Title: "task-1", Description: "desc-1", Status: tasks.StatusPending, Priority: 1},
			},
			resultsSeq: map[string][]string{
				"coder":    {coderDone},
				"tester":   {testerPass},
				"critic":   {criticApprove},
				"reviewer": {reviewerStop},
			},
			maxFixIter: 3,
			maxCycles:  1,
			assertFn: func(t *testing.T, dir string, events []map[string]any) {
				if c := countAgentRuns(events, "coder"); c != 1 {
					t.Errorf("coder runs: got %d, want 1", c)
				}
				if c := countAgentRuns(events, "tester"); c != 1 {
					t.Errorf("tester runs: got %d, want 1", c)
				}
				if c := countAgentRuns(events, "critic"); c != 1 {
					t.Errorf("critic runs: got %d, want 1", c)
				}
				if c := countAgentRuns(events, "reviewer"); c != 1 {
					t.Errorf("reviewer runs: got %d, want 1", c)
				}
				if c := countTaskDones(events); c != 1 {
					t.Errorf("task_dones: got %d, want 1", c)
				}
				tl := loadFinalTasks(t, dir)
				task := findTask(tl, "T001")
				if task == nil || task.Status != tasks.StatusDone {
					t.Errorf("T001 status: got %v, want done", task.Status)
				}
				if commits := countGitCommits(t, dir); commits < 2 {
					t.Errorf("git commits: got %d, want >=2", commits)
				}
			},
		},
		{
			name: "tester_fails_once",
			initialTasks: []tasks.Task{
				{ID: "T001", Title: "task-1", Description: "desc-1", Status: tasks.StatusPending, Priority: 1},
			},
			resultsSeq: map[string][]string{
				"coder":    {coderDone, coderDone},
				"tester":   {testerFail, testerPass},
				"critic":   {criticApprove},
				"reviewer": {reviewerStop},
			},
			maxFixIter: 3,
			maxCycles:  1,
			assertFn: func(t *testing.T, dir string, events []map[string]any) {
				if c := countAgentRuns(events, "coder"); c != 2 {
					t.Errorf("coder runs: got %d, want 2", c)
				}
				if c := countAgentRuns(events, "tester"); c != 2 {
					t.Errorf("tester runs: got %d, want 2", c)
				}
				if c := countAgentRuns(events, "critic"); c != 1 {
					t.Errorf("critic runs: got %d, want 1", c)
				}
				if c := countTaskDones(events); c != 1 {
					t.Errorf("task_dones: got %d, want 1", c)
				}
				tl := loadFinalTasks(t, dir)
				task := findTask(tl, "T001")
				if task == nil || task.Status != tasks.StatusDone {
					t.Errorf("T001 status: got %v, want done", task.Status)
				}
			},
		},
		{
			name: "critic_rejects_once",
			initialTasks: []tasks.Task{
				{ID: "T001", Title: "task-1", Description: "desc-1", Status: tasks.StatusPending, Priority: 1},
			},
			resultsSeq: map[string][]string{
				"coder":    {coderDone, coderDone},
				"tester":   {testerPass, testerPass},
				"critic":   {criticReject, criticApprove},
				"reviewer": {reviewerStop},
			},
			maxFixIter: 3,
			maxCycles:  1,
			assertFn: func(t *testing.T, dir string, events []map[string]any) {
				if c := countAgentRuns(events, "coder"); c != 2 {
					t.Errorf("coder runs: got %d, want 2", c)
				}
				if c := countAgentRuns(events, "tester"); c != 2 {
					t.Errorf("tester runs: got %d, want 2", c)
				}
				if c := countAgentRuns(events, "critic"); c != 2 {
					t.Errorf("critic runs: got %d, want 2", c)
				}
				if c := countTaskDones(events); c != 1 {
					t.Errorf("task_dones: got %d, want 1", c)
				}
				tl := loadFinalTasks(t, dir)
				task := findTask(tl, "T001")
				if task == nil || task.Status != tasks.StatusDone {
					t.Errorf("T001 status: got %v, want done", task.Status)
				}
			},
		},
		{
			name: "max_iterations",
			initialTasks: []tasks.Task{
				{ID: "T001", Title: "task-1", Description: "desc-1", Status: tasks.StatusPending, Priority: 1},
			},
			resultsSeq: map[string][]string{
				"coder": {coderDone, coderDone, coderDone},
				// Use distinct failures per attempt so stuck detection (threshold=2 consecutive
				// identical hashes) does not fire before max_iterations is reached.
				"tester":   {testerFail, testerFail2, testerFail3},
				"critic":   {}, // critic should not run
				"reviewer": {reviewerStop},
			},
			maxFixIter: 3,
			maxCycles:  1,
			assertFn: func(t *testing.T, dir string, events []map[string]any) {
				if c := countAgentRuns(events, "coder"); c != 3 {
					t.Errorf("coder runs: got %d, want 3", c)
				}
				if c := countAgentRuns(events, "tester"); c != 3 {
					t.Errorf("tester runs: got %d, want 3", c)
				}
				if c := countAgentRuns(events, "critic"); c != 0 {
					t.Errorf("critic runs: got %d, want 0", c)
				}
				if c := countTaskDones(events); c != 0 {
					t.Errorf("task_dones: got %d, want 0", c)
				}
				tl := loadFinalTasks(t, dir)
				task := findTask(tl, "T001")
				if task == nil {
					t.Fatal("T001 not found")
				}
				if task.Status != tasks.StatusFailed {
					t.Errorf("T001 status: got %v, want failed", task.Status)
				}
				if task.FailureReason == nil || *task.FailureReason != tasks.ReasonMaxIterations {
					t.Errorf("T001 failure_reason: got %v, want max_iterations", task.FailureReason)
				}
				if commits := countGitCommits(t, dir); commits != 1 {
					t.Errorf("git commits: got %d, want 1 (only init)", commits)
				}
			},
		},
		{
			name: "multi_cycle_all_pass",
			initialTasks: []tasks.Task{
				{ID: "T001", Title: "task-1", Description: "desc-1", Status: tasks.StatusPending, Priority: 1},
				{ID: "T002", Title: "task-2", Description: "desc-2", Status: tasks.StatusPending, Priority: 2},
			},
			resultsSeq: map[string][]string{
				"coder":    {coderDone, coderDone, coderDone, coderDone},
				"tester":   {testerPass, testerPass, testerPass, testerPass},
				"critic":   {criticApprove, criticApprove, criticApprove, criticApprove},
				"reviewer": {reviewerProposeTwo, reviewerStop},
			},
			maxFixIter: 5,
			maxCycles:  2,
			assertFn: func(t *testing.T, dir string, events []map[string]any) {
				if c := countAgentRuns(events, "coder"); c != 4 {
					t.Errorf("coder runs: got %d, want 4", c)
				}
				if c := countAgentRuns(events, "tester"); c != 4 {
					t.Errorf("tester runs: got %d, want 4", c)
				}
				if c := countAgentRuns(events, "critic"); c != 4 {
					t.Errorf("critic runs: got %d, want 4", c)
				}
				if c := countAgentRuns(events, "reviewer"); c != 2 {
					t.Errorf("reviewer runs: got %d, want 2", c)
				}
				if c := countTaskDones(events); c != 4 {
					t.Errorf("task_dones: got %d, want 4", c)
				}
				tl := loadFinalTasks(t, dir)
				for _, id := range []string{"T001", "T002", "T003", "T004"} {
					task := findTask(tl, id)
					if task == nil || task.Status != tasks.StatusDone {
						t.Errorf("%s status: got %v, want done", id, task.Status)
					}
				}
				if commits := countGitCommits(t, dir); commits < 5 {
					t.Errorf("git commits: got %d, want >=5", commits)
				}
			},
		},
		{
			name: "multi_cycle_with_fix_loop_retry",
			initialTasks: []tasks.Task{
				{ID: "T001", Title: "task-1", Description: "desc-1", Status: tasks.StatusPending, Priority: 1},
				{ID: "T002", Title: "task-2", Description: "desc-2", Status: tasks.StatusPending, Priority: 2},
			},
			resultsSeq: map[string][]string{
				"coder":    {coderDone, coderDone, coderDone, coderDone},
				"tester":   {testerFail, testerPass, testerPass, testerPass},
				"critic":   {criticApprove, criticApprove, criticApprove},
				"reviewer": {reviewerProposeOne, reviewerStop},
			},
			maxFixIter: 5,
			maxCycles:  2,
			assertFn: func(t *testing.T, dir string, events []map[string]any) {
				// T001 iter1: coder, tester-fail (critic skipped)
				// T001 iter2: coder, tester-pass, critic-approve
				// T002: coder, tester-pass, critic-approve
				// T003: coder, tester-pass, critic-approve
				if c := countAgentRuns(events, "coder"); c != 4 {
					t.Errorf("coder runs: got %d, want 4", c)
				}
				if c := countAgentRuns(events, "tester"); c != 4 {
					t.Errorf("tester runs: got %d, want 4", c)
				}
				if c := countAgentRuns(events, "critic"); c != 3 {
					t.Errorf("critic runs: got %d, want 3", c)
				}
				if c := countAgentRuns(events, "reviewer"); c != 2 {
					t.Errorf("reviewer runs: got %d, want 2", c)
				}
				if c := countTaskDones(events); c != 3 {
					t.Errorf("task_dones: got %d, want 3", c)
				}
				tl := loadFinalTasks(t, dir)
				for _, id := range []string{"T001", "T002", "T003"} {
					task := findTask(tl, id)
					if task == nil || task.Status != tasks.StatusDone {
						t.Errorf("%s status: got %v, want done", id, task.Status)
					}
				}
				if commits := countGitCommits(t, dir); commits < 4 {
					t.Errorf("git commits: got %d, want >=4", commits)
				}
			},
		},
	}

	for _, sc := range scenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			dir := t.TempDir()
			setupE2EGitRepo(t, dir)
			if err := Init(dir); err != nil {
				t.Fatalf("Init: %v", err)
			}

			resultsDir := filepath.Join(dir, ".mock-results")
			stateDir := filepath.Join(dir, ".mock-state")
			for role, jsons := range sc.resultsSeq {
				stageRoleResults(t, resultsDir, role, jsons)
			}

			cliPath := filepath.Join(dir, "fakecli_staged.sh")
			if err := os.WriteFile(cliPath, []byte(fakeCLIStaged), 0o755); err != nil {
				t.Fatalf("write fakecli_staged.sh: %v", err)
			}

			writeE2ETeamJSON(t, dir, cliPath, resultsDir, stateDir, sc.maxFixIter, sc.maxCycles)

			raw, _ := json.MarshalIndent(&tasks.TaskList{Tasks: sc.initialTasks}, "", "  ")
			tasksPath := filepath.Join(dir, ".orquestalite", "tasks.json")
			if err := os.WriteFile(tasksPath, raw, 0o644); err != nil {
				t.Fatalf("write tasks.json: %v", err)
			}

			err := Run(context.Background(), RunOptions{
				ProjectDir: dir,
				TeamPath:   filepath.Join(dir, "team.json"),
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			events := parseRunLog(t, dir)
			sc.assertFn(t, dir, events)
		})
	}
}
