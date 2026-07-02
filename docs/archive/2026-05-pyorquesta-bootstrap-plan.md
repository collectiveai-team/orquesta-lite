> _Archivado: plan histórico completado. Preservado para referencia._
# pyorquesta Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a single-binary Go CLI (`pyorquesta`) that orchestrates AI-coding CLI tools through nested loops (review → task → fix) to autonomously implement plans, per the Ralph technique.

**Architecture:** Bottom-up Go modules. Pure stdlib for the spine. Each agent is a subprocess; each agent writes a JSON result file the orchestrator parses. Rate-limit-aware fallback chain per role. Sequential single-dir execution with one git commit per successful task.

**Tech Stack:** Go 1.22+, stdlib only for v0 (`os/exec`, `encoding/json`, `log/slog`, `flag`, `regexp`, `time`, `os`, `path/filepath`, `compress/gzip`, `bytes`, `bufio`, `testing`).

**Reference:** [`CONTEXT.md`](../CONTEXT.md), [`docs/adr/0001-cli-subprocess-orchestration.md`](../docs/adr/0001-cli-subprocess-orchestration.md), [`docs/adr/0002-json-result-contracts.md`](../docs/adr/0002-json-result-contracts.md).

## File map

```
cmd/pyorquesta/main.go            entry point: subcommand dispatch (init, plan, run, status, reset)
internal/config/config.go         team.json types + Load + Validate
internal/tasks/tasks.go           tasks.json types + Load/Save + status transitions + ID generator
internal/results/results.go       per-role JSON contract types + Validate per role
internal/prompts/prompts.go       template Load + {{VAR}} Interpolate
internal/memory/memory.go         memory.md append-only Write + Read-all
internal/gitx/gitx.go             thin wrappers: Commit, CheckoutAll, LogStat, IsCleanTree, HeadSHA
internal/eventlog/eventlog.go     JSONL log writer + stdout pretty + rotation at 50MB
internal/runner/runner.go         RunAgent: build cmd, exec with timeout, capture out/err, detect rate limit
internal/fallback/fallback.go     CallRole: iterate role.agents, manage cooldowns, exponential backoff
internal/loops/fix.go             FixLoop: coder → tester (→ critic) sequencing + feedback injection
internal/loops/task.go            TaskLoop: pick next pending task in priority order, drive fix loop, full-suite gate, commit
internal/loops/review.go          ReviewLoop: outer N cycles, invoke reviewer at end of each, append new tasks
internal/commands/initcmd.go      `pyorquesta init`: scaffold .pyorquesta, default team.json, default prompts/, .gitignore
internal/commands/plancmd.go      `pyorquesta plan`: invoke parser role, write tasks.json (with --append)
internal/commands/runcmd.go       `pyorquesta run`: drive review loop; with arg, plan+run
internal/commands/statuscmd.go    `pyorquesta status`: print task table + currently line; --watch refreshes
internal/commands/resetcmd.go     `pyorquesta reset`: wipe .pyorquesta/ (with confirmation)
prompts/parser.md                 default parser prompt shipped by `init`
prompts/coder.md                  default coder prompt shipped by `init`
prompts/tester.md                 default tester prompt shipped by `init`
prompts/critic.md                 default critic prompt shipped by `init`
prompts/reviewer.md               default reviewer prompt shipped by `init`
```

## Conventions

- **Tests:** standard `testing` package, table-driven where useful. No `testify`; use `t.Fatal`, `t.Errorf` and a small helper `eq[T comparable]`.
- **Errors:** wrapped with `fmt.Errorf("context: %w", err)`. No bare panics outside `main`.
- **Paths:** all paths constructed with `filepath.Join`. Constants for known roots: `.pyorquesta/`, `.pyorquesta/results/`, `.pyorquesta/run.log`, `.pyorquesta/memory.md`, `.pyorquesta/tasks.json`.
- **Commits:** one commit per task in this plan. Messages prefixed with the task tag, e.g. `feat(config): load and validate team.json`.

---

## Task 1: Project bootstrap

**Files:**
- Create: `go.mod`
- Create: `.gitignore`
- Create: `README.md` (one-paragraph stub)

- [ ] **Step 1: Initialize Go module**

```bash
cd /Users/lionelchamorro/Projects/personal/pyorquesta
go mod init github.com/lionelchamorro/pyorquesta
```

Expected: creates `go.mod` with module declaration and `go 1.22` (or current).

- [ ] **Step 2: Write .gitignore**

```
# binaries
/pyorquesta
/dist/

# runtime state (orchestrator writes here)
/.pyorquesta/

# go
*.test
*.out
coverage.txt
```

- [ ] **Step 3: Write README.md stub**

```markdown
# pyorquesta

Minimalist Go orchestrator for the Ralph technique. See [CONTEXT.md](./CONTEXT.md) for design and [docs/adr/](./docs/adr/) for architecture decisions.

## Quick start

```bash
go build -o pyorquesta ./cmd/pyorquesta
./pyorquesta init
```
```

- [ ] **Step 4: Initial git commit**

```bash
git init
git add go.mod .gitignore README.md CONTEXT.md docs/ tasks/
git commit -m "chore: bootstrap pyorquesta repo"
```

Expected: clean working tree afterwards.

---

## Task 2: `internal/config` — team.json types and validation

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTeamJSON(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "team.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad_Valid(t *testing.T) {
	p := writeTeamJSON(t, `{
		"agents": {
			"a1": {"cmd": ["claude", "-p", "{{PROMPT}}"], "rate_limit_pattern": "rate_?limit"}
		},
		"roles": {
			"coder": {"agents": ["a1"], "prompt": "prompts/coder.md", "result_path": ".pyorquesta/results/coder.json", "timeout_seconds": 900}
		},
		"limits": {"max_review_cycles": 3, "max_fix_iterations": 5},
		"rate_limit_backoff": {"initial_seconds": 30, "factor": 2, "max_seconds": 1800, "default_pattern": "rate_?limit|429"},
		"full_test_command": "go test ./..."
	}`)

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Limits.MaxFixIterations != 5 {
		t.Errorf("MaxFixIterations = %d, want 5", cfg.Limits.MaxFixIterations)
	}
	if got := cfg.Roles["coder"].Agents[0]; got != "a1" {
		t.Errorf("coder.agents[0] = %q, want %q", got, "a1")
	}
}

func TestLoad_UnknownAgentInRoleFails(t *testing.T) {
	p := writeTeamJSON(t, `{
		"agents": {"a1": {"cmd": ["x"]}},
		"roles": {"coder": {"agents": ["a1", "ghost"], "prompt": "p.md", "result_path": "r.json", "timeout_seconds": 1}},
		"limits": {"max_review_cycles": 1, "max_fix_iterations": 1},
		"rate_limit_backoff": {"initial_seconds": 1, "factor": 2, "max_seconds": 2, "default_pattern": "x"},
		"full_test_command": "true"
	}`)

	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("expected error mentioning unknown agent 'ghost', got: %v", err)
	}
}

func TestLoad_PromptInCmdMustContainMarker(t *testing.T) {
	p := writeTeamJSON(t, `{
		"agents": {"a1": {"cmd": ["claude", "-p", "no-marker"]}},
		"roles": {"coder": {"agents": ["a1"], "prompt": "p.md", "result_path": "r.json", "timeout_seconds": 1}},
		"limits": {"max_review_cycles": 1, "max_fix_iterations": 1},
		"rate_limit_backoff": {"initial_seconds": 1, "factor": 2, "max_seconds": 2, "default_pattern": "x"},
		"full_test_command": "true"
	}`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "{{PROMPT}}") {
		t.Fatalf("expected error about missing {{PROMPT}} marker, got: %v", err)
	}
}
```

- [ ] **Step 2: Run test to confirm failure**

```bash
go test ./internal/config/...
```

Expected: build/compile error (no Load function yet).

- [ ] **Step 3: Implement config.go**

```go
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type Agent struct {
	Cmd               []string `json:"cmd"`
	RateLimitPattern  string   `json:"rate_limit_pattern,omitempty"`
}

type Role struct {
	Agents         []string `json:"agents"`
	Prompt         string   `json:"prompt"`
	ResultPath     string   `json:"result_path"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

type Limits struct {
	MaxReviewCycles  int `json:"max_review_cycles"`
	MaxFixIterations int `json:"max_fix_iterations"`
}

type RateLimitBackoff struct {
	InitialSeconds int    `json:"initial_seconds"`
	Factor         int    `json:"factor"`
	MaxSeconds     int    `json:"max_seconds"`
	DefaultPattern string `json:"default_pattern"`
}

type Config struct {
	Agents           map[string]Agent `json:"agents"`
	Roles            map[string]Role  `json:"roles"`
	Limits           Limits           `json:"limits"`
	RateLimitBackoff RateLimitBackoff `json:"rate_limit_backoff"`
	FullTestCommand  string           `json:"full_test_command"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) Validate() error {
	if len(c.Agents) == 0 {
		return fmt.Errorf("no agents declared")
	}
	for name, a := range c.Agents {
		if len(a.Cmd) == 0 {
			return fmt.Errorf("agent %q has empty cmd", name)
		}
		hasMarker := false
		for _, tok := range a.Cmd {
			if strings.Contains(tok, "{{PROMPT}}") {
				hasMarker = true
				break
			}
		}
		if !hasMarker {
			return fmt.Errorf("agent %q cmd is missing {{PROMPT}} marker", name)
		}
	}
	for rname, r := range c.Roles {
		if len(r.Agents) == 0 {
			return fmt.Errorf("role %q has no agents", rname)
		}
		for _, a := range r.Agents {
			if _, ok := c.Agents[a]; !ok {
				return fmt.Errorf("role %q references unknown agent %q", rname, a)
			}
		}
		if r.Prompt == "" || r.ResultPath == "" {
			return fmt.Errorf("role %q must declare prompt and result_path", rname)
		}
		if r.TimeoutSeconds <= 0 {
			return fmt.Errorf("role %q timeout_seconds must be > 0", rname)
		}
	}
	if c.Limits.MaxReviewCycles <= 0 || c.Limits.MaxFixIterations <= 0 {
		return fmt.Errorf("limits must be positive")
	}
	if c.RateLimitBackoff.InitialSeconds <= 0 || c.RateLimitBackoff.Factor < 2 || c.RateLimitBackoff.MaxSeconds < c.RateLimitBackoff.InitialSeconds {
		return fmt.Errorf("invalid rate_limit_backoff")
	}
	if c.FullTestCommand == "" {
		return fmt.Errorf("full_test_command must be set")
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/config/...
```

Expected: PASS, all 3 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/config/
git commit -m "feat(config): load and validate team.json"
```

---

## Task 3: `internal/tasks` — tasks.json + status transitions

**Files:**
- Create: `internal/tasks/tasks.go`
- Create: `internal/tasks/tasks_test.go`

- [ ] **Step 1: Write the failing test**

```go
package tasks

import (
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tasks.json")

	want := &TaskList{Tasks: []Task{
		{ID: "T001", Title: "first", Description: "do X", Status: StatusPending, Priority: 1},
	}}
	if err := Save(p, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tasks) != 1 || got.Tasks[0].ID != "T001" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestNextPendingByPriority(t *testing.T) {
	tl := &TaskList{Tasks: []Task{
		{ID: "T001", Status: StatusDone, Priority: 1},
		{ID: "T002", Status: StatusPending, Priority: 5},
		{ID: "T003", Status: StatusPending, Priority: 2},
	}}
	got := tl.NextPending()
	if got == nil || got.ID != "T003" {
		t.Fatalf("NextPending = %+v, want T003", got)
	}
}

func TestAnyPending(t *testing.T) {
	tl := &TaskList{Tasks: []Task{{Status: StatusDone}, {Status: StatusFailed}}}
	if tl.AnyPending() {
		t.Errorf("AnyPending = true, want false")
	}
	tl.Tasks = append(tl.Tasks, Task{Status: StatusPending})
	if !tl.AnyPending() {
		t.Errorf("AnyPending = false, want true")
	}
}

func TestNextID(t *testing.T) {
	tl := &TaskList{Tasks: []Task{{ID: "T001"}, {ID: "T007"}, {ID: "T003"}}}
	if got := tl.NextID(); got != "T008" {
		t.Errorf("NextID = %q, want T008", got)
	}
	empty := &TaskList{}
	if got := empty.NextID(); got != "T001" {
		t.Errorf("empty NextID = %q, want T001", got)
	}
}

func TestAppendAssignsIDs(t *testing.T) {
	tl := &TaskList{Tasks: []Task{{ID: "T001"}}}
	added := tl.Append([]Task{
		{Title: "x", Description: "y", Priority: 1},
		{Title: "z", Description: "w", Priority: 2},
	}, 1)
	if len(added) != 2 || added[0].ID != "T002" || added[1].ID != "T003" {
		t.Fatalf("Append IDs = %+v", added)
	}
	if added[0].Status != StatusPending || added[0].CreatedInReviewCycle != 1 {
		t.Errorf("Append did not initialise status/cycle: %+v", added[0])
	}
}
```

- [ ] **Step 2: Run test to confirm failure**

```bash
go test ./internal/tasks/...
```

Expected: build error.

- [ ] **Step 3: Implement tasks.go**

```go
package tasks

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusDone       Status = "done"
	StatusFailed     Status = "failed"
)

type FailureReason string

const (
	ReasonMaxIterations       FailureReason = "max_iterations"
	ReasonAgentRepeatedFail   FailureReason = "agent_repeated_failure"
	ReasonRateLimitExhausted  FailureReason = "rate_limit_exhausted"
	ReasonCommitRejected      FailureReason = "commit_rejected"
	ReasonFullSuiteFailed     FailureReason = "full_suite_failed"
	ReasonAgentCrashed        FailureReason = "agent_crashed"
	ReasonInvalidResultJSON   FailureReason = "invalid_result_json"
)

type Task struct {
	ID                   string         `json:"id"`
	Title                string         `json:"title"`
	Description          string         `json:"description"`
	Status               Status         `json:"status"`
	Priority             int            `json:"priority"`
	CreatedInReviewCycle int            `json:"created_in_review_cycle"`
	Attempts             int            `json:"attempts"`
	LastFeedback         *string        `json:"last_feedback"`
	FailureReason        *FailureReason `json:"failure_reason,omitempty"`
}

type TaskList struct {
	Tasks []Task `json:"tasks"`
}

func Load(path string) (*TaskList, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read tasks.json: %w", err)
	}
	var tl TaskList
	if err := json.Unmarshal(raw, &tl); err != nil {
		return nil, fmt.Errorf("parse tasks.json: %w", err)
	}
	return &tl, nil
}

func Save(path string, tl *TaskList) error {
	raw, err := json.MarshalIndent(tl, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func (tl *TaskList) AnyPending() bool {
	for _, t := range tl.Tasks {
		if t.Status == StatusPending {
			return true
		}
	}
	return false
}

func (tl *TaskList) NextPending() *Task {
	pending := []int{}
	for i, t := range tl.Tasks {
		if t.Status == StatusPending {
			pending = append(pending, i)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	sort.SliceStable(pending, func(a, b int) bool {
		return tl.Tasks[pending[a]].Priority < tl.Tasks[pending[b]].Priority
	})
	return &tl.Tasks[pending[0]]
}

func (tl *TaskList) NextID() string {
	max := 0
	for _, t := range tl.Tasks {
		n, err := strconv.Atoi(strings.TrimPrefix(t.ID, "T"))
		if err == nil && n > max {
			max = n
		}
	}
	return fmt.Sprintf("T%03d", max+1)
}

func (tl *TaskList) Append(newTasks []Task, cycle int) []Task {
	added := make([]Task, 0, len(newTasks))
	for _, t := range newTasks {
		t.ID = tl.NextID()
		t.Status = StatusPending
		t.CreatedInReviewCycle = cycle
		t.Attempts = 0
		tl.Tasks = append(tl.Tasks, t)
		added = append(added, t)
	}
	return added
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/tasks/...
```

Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/tasks/
git commit -m "feat(tasks): tasks.json types, transitions, ID assignment"
```

---

## Task 4: `internal/results` — per-role contract types

**Files:**
- Create: `internal/results/results.go`
- Create: `internal/results/results_test.go`

- [ ] **Step 1: Write the failing test**

```go
package results

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "r.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseTester_PassNoFailures(t *testing.T) {
	p := write(t, `{"status":"pass","command_run":"go test","failures":[],"notes_for_memory":null}`)
	r, err := ParseTester(p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "pass" {
		t.Errorf("status = %q", r.Status)
	}
}

func TestParseTester_FailMissingCommand(t *testing.T) {
	p := write(t, `{"status":"fail","failures":[{"test":"t1","message":"boom"}]}`)
	_, err := ParseTester(p)
	if err == nil {
		t.Fatal("expected error: command_run required")
	}
}

func TestParseCritic_RejectedRequiresConcerns(t *testing.T) {
	p := write(t, `{"status":"rejected","concerns":[]}`)
	_, err := ParseCritic(p)
	if err == nil {
		t.Fatal("expected error: rejected critic must list concerns")
	}
}

func TestParseReviewer_ShouldStopMissing(t *testing.T) {
	p := write(t, `{"summary_of_cycle":"x","new_tasks":[]}`)
	_, err := ParseReviewer(p)
	if err == nil {
		t.Fatal("expected error: should_stop required")
	}
}

func TestParseParser_ZeroTasksOK(t *testing.T) {
	p := write(t, `{"tasks":[],"notes_for_memory":null}`)
	r, err := ParseParser(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Tasks) != 0 {
		t.Errorf("tasks = %d", len(r.Tasks))
	}
}
```

- [ ] **Step 2: Run test to confirm failure**

```bash
go test ./internal/results/...
```

- [ ] **Step 3: Implement results.go**

```go
package results

import (
	"encoding/json"
	"fmt"
	"os"
)

type ParserTask struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    int    `json:"priority"`
}

type ParserResult struct {
	Tasks          []ParserTask `json:"tasks"`
	NotesForMemory *string      `json:"notes_for_memory"`
}

type CoderResult struct {
	Status         string   `json:"status"`
	Summary        string   `json:"summary"`
	FilesChanged   []string `json:"files_changed"`
	NotesForMemory *string  `json:"notes_for_memory"`
}

type TestFailure struct {
	Test    string `json:"test"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

type TesterResult struct {
	Status         string        `json:"status"`
	CommandRun     string        `json:"command_run"`
	Failures       []TestFailure `json:"failures"`
	NotesForMemory *string       `json:"notes_for_memory"`
}

type Concern struct {
	Severity   string `json:"severity"`
	Where      string `json:"where"`
	Issue      string `json:"issue"`
	Suggestion string `json:"suggestion"`
}

type CriticResult struct {
	Status         string    `json:"status"`
	Concerns       []Concern `json:"concerns"`
	NotesForMemory *string   `json:"notes_for_memory"`
}

type ReviewerNewTask struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    int    `json:"priority"`
}

type ReviewerResult struct {
	SummaryOfCycle string            `json:"summary_of_cycle"`
	NewTasks       []ReviewerNewTask `json:"new_tasks"`
	ShouldStop     *bool             `json:"should_stop"`
	NotesForMemory *string           `json:"notes_for_memory"`
}

func read(path string, into any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func ParseParser(path string) (*ParserResult, error) {
	var r ParserResult
	if err := read(path, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func ParseCoder(path string) (*CoderResult, error) {
	var r CoderResult
	if err := read(path, &r); err != nil {
		return nil, err
	}
	if r.Status != "completed" && r.Status != "blocked" {
		return nil, fmt.Errorf("coder.status %q invalid", r.Status)
	}
	return &r, nil
}

func ParseTester(path string) (*TesterResult, error) {
	var r TesterResult
	if err := read(path, &r); err != nil {
		return nil, err
	}
	if r.Status != "pass" && r.Status != "fail" {
		return nil, fmt.Errorf("tester.status %q invalid", r.Status)
	}
	if r.CommandRun == "" {
		return nil, fmt.Errorf("tester.command_run required")
	}
	return &r, nil
}

func ParseCritic(path string) (*CriticResult, error) {
	var r CriticResult
	if err := read(path, &r); err != nil {
		return nil, err
	}
	if r.Status != "approved" && r.Status != "rejected" {
		return nil, fmt.Errorf("critic.status %q invalid", r.Status)
	}
	if r.Status == "rejected" && len(r.Concerns) == 0 {
		return nil, fmt.Errorf("rejected critic must list concerns")
	}
	return &r, nil
}

func ParseReviewer(path string) (*ReviewerResult, error) {
	var r ReviewerResult
	if err := read(path, &r); err != nil {
		return nil, err
	}
	if r.ShouldStop == nil {
		return nil, fmt.Errorf("reviewer.should_stop required")
	}
	return &r, nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/results/...
```

Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/results/
git commit -m "feat(results): per-role JSON contract types and parsers"
```

---

## Task 5: `internal/prompts` — template loading and {{VAR}} interpolation

**Files:**
- Create: `internal/prompts/prompts.go`
- Create: `internal/prompts/prompts_test.go`

- [ ] **Step 1: Write the failing test**

```go
package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInterpolate(t *testing.T) {
	tmpl := "Task: {{TASK_TITLE}}\nAttempt {{ATTEMPT_NUMBER}}\n{{TESTER_FEEDBACK}}"
	got := Interpolate(tmpl, map[string]string{
		"TASK_TITLE":      "Add login",
		"ATTEMPT_NUMBER":  "2",
		"TESTER_FEEDBACK": "test_login failed: unexpected status",
	})
	want := "Task: Add login\nAttempt 2\ntest_login failed: unexpected status"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestInterpolate_MissingVarStaysLiteral(t *testing.T) {
	got := Interpolate("hello {{NAME}} {{UNKNOWN}}", map[string]string{"NAME": "world"})
	if !strings.Contains(got, "{{UNKNOWN}}") {
		t.Errorf("unknown vars should remain literal, got: %q", got)
	}
}

func TestInterpolate_PreservesCurlyBracesInContent(t *testing.T) {
	tmpl := `Output JSON: {"status": "{{STATUS}}"}`
	got := Interpolate(tmpl, map[string]string{"STATUS": "pass"})
	want := `Output JSON: {"status": "pass"}`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.md")
	if err := os.WriteFile(p, []byte("hello {{X}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmpl, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if tmpl != "hello {{X}}" {
		t.Errorf("Load mismatch: %q", tmpl)
	}
}
```

- [ ] **Step 2: Run test to confirm failure**

```bash
go test ./internal/prompts/...
```

- [ ] **Step 3: Implement prompts.go**

```go
package prompts

import (
	"fmt"
	"os"
	"strings"
)

func Load(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("load prompt %s: %w", path, err)
	}
	return string(raw), nil
}

func Interpolate(tmpl string, vars map[string]string) string {
	out := tmpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	return out
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/prompts/...
```

Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/prompts/
git commit -m "feat(prompts): load and interpolate prompt templates with {{VAR}} markers"
```

---

## Task 6: `internal/memory` — append-only memory.md

**Files:**
- Create: `internal/memory/memory.go`
- Create: `internal/memory/memory_test.go`

- [ ] **Step 1: Write the failing test**

```go
package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendCreatesFileAndAddsHeader(t *testing.T) {
	p := filepath.Join(t.TempDir(), "memory.md")
	if err := Append(p, Entry{Cycle: 1, TaskID: "T003", Role: "critic", Body: "snake_case in DB"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, "## [cycle 1, task T003, critic]") {
		t.Errorf("missing header: %s", got)
	}
	if !strings.Contains(got, "snake_case in DB") {
		t.Errorf("missing body: %s", got)
	}
}

func TestAppendIsAppendOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "memory.md")
	_ = Append(p, Entry{Cycle: 1, TaskID: "T001", Role: "coder", Body: "first"})
	_ = Append(p, Entry{Cycle: 1, TaskID: "T002", Role: "coder", Body: "second"})
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), "first") || !strings.Contains(string(raw), "second") {
		t.Errorf("append did not preserve prior content: %s", raw)
	}
}

func TestReadAllReturnsEmptyIfMissing(t *testing.T) {
	got, err := ReadAll(filepath.Join(t.TempDir(), "nope.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}
```

- [ ] **Step 2: Run test to confirm failure**

```bash
go test ./internal/memory/...
```

- [ ] **Step 3: Implement memory.go**

```go
package memory

import (
	"errors"
	"fmt"
	"os"
)

type Entry struct {
	Cycle  int
	TaskID string
	Role   string
	Body   string
}

func Append(path string, e Entry) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open memory: %w", err)
	}
	defer f.Close()
	header := fmt.Sprintf("\n## [cycle %d, task %s, %s]\n", e.Cycle, e.TaskID, e.Role)
	if _, err := f.WriteString(header + e.Body + "\n"); err != nil {
		return fmt.Errorf("write memory: %w", err)
	}
	return nil
}

func ReadAll(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/memory/...
```

Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/memory/
git commit -m "feat(memory): append-only memory.md with role/cycle headers"
```

---

## Task 7: `internal/gitx` — thin git wrappers

**Files:**
- Create: `internal/gitx/gitx.go`
- Create: `internal/gitx/gitx_test.go`

- [ ] **Step 1: Write the failing test**

The tests use a temp dir initialised as a git repo. Tests skip if `git` is not on PATH.

```go
package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitOrSkip(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@test"},
		{"config", "user.name", "test"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestIsCleanTree_TrueOnFreshRepo(t *testing.T) {
	gitOrSkip(t)
	dir := initRepo(t)
	clean, err := IsCleanTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Errorf("expected clean tree")
	}
}

func TestIsCleanTree_FalseAfterModification(t *testing.T) {
	gitOrSkip(t)
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	clean, _ := IsCleanTree(dir)
	if clean {
		t.Errorf("expected dirty tree")
	}
}

func TestCommitAll_CreatesCommit(t *testing.T) {
	gitOrSkip(t)
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, err := CommitAll(dir, "feat: add a")
	if err != nil {
		t.Fatal(err)
	}
	if len(sha) < 7 {
		t.Errorf("sha too short: %q", sha)
	}
}

func TestCheckoutAll_DiscardsChanges(t *testing.T) {
	gitOrSkip(t)
	dir := initRepo(t)
	p := filepath.Join(dir, "a.txt")
	_ = os.WriteFile(p, []byte("a"), 0o644)
	_, _ = CommitAll(dir, "add a")
	_ = os.WriteFile(p, []byte("DIRTY"), 0o644)
	if err := CheckoutAll(dir); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if string(raw) != "a" {
		t.Errorf("checkout did not restore: %q", raw)
	}
}

func TestLogStatSinceHead(t *testing.T) {
	gitOrSkip(t)
	dir := initRepo(t)
	start, _ := HeadSHA(dir)
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	_, _ = CommitAll(dir, "add a")
	out, err := LogStat(dir, start)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "add a") {
		t.Errorf("log missing commit message: %q", out)
	}
}
```

- [ ] **Step 2: Run test to confirm failure**

```bash
go test ./internal/gitx/...
```

- [ ] **Step 3: Implement gitx.go**

```go
package gitx

import (
	"fmt"
	"os/exec"
	"strings"
)

func run(dir string, args ...string) (string, error) {
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

func IsCleanTree(dir string) (bool, error) {
	out, err := run(dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out == "", nil
}

func CommitAll(dir, message string) (string, error) {
	if _, err := run(dir, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := run(dir, "commit", "-m", message); err != nil {
		return "", err
	}
	return HeadSHA(dir)
}

func CheckoutAll(dir string) error {
	if _, err := run(dir, "checkout", "."); err != nil {
		return err
	}
	if _, err := run(dir, "clean", "-fd"); err != nil {
		return err
	}
	return nil
}

func HeadSHA(dir string) (string, error) {
	return run(dir, "rev-parse", "HEAD")
}

func LogStat(dir, sinceSHA string) (string, error) {
	return run(dir, "log", sinceSHA+"..HEAD", "--stat")
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/gitx/...
```

Expected: PASS (5 tests, or skipped if git missing).

- [ ] **Step 5: Commit**

```bash
git add internal/gitx/
git commit -m "feat(gitx): wrappers for commit, checkout, log, head sha, clean check"
```

---

## Task 8: `internal/eventlog` — JSONL events + stdout pretty + rotation

**Files:**
- Create: `internal/eventlog/eventlog.go`
- Create: `internal/eventlog/eventlog_test.go`

- [ ] **Step 1: Write the failing test**

```go
package eventlog

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLog_WritesJSONLAndPretty(t *testing.T) {
	dir := t.TempDir()
	pretty := &bytes.Buffer{}
	l, err := Open(filepath.Join(dir, "run.log"), pretty)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	l.Log(Event{Type: "task_start", Fields: map[string]any{"task_id": "T003", "priority": 2}})

	raw, _ := os.ReadFile(filepath.Join(dir, "run.log"))
	if !strings.Contains(string(raw), `"event":"task_start"`) {
		t.Errorf("jsonl missing event: %s", raw)
	}
	if !strings.Contains(string(raw), `"task_id":"T003"`) {
		t.Errorf("jsonl missing field: %s", raw)
	}

	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &got); err != nil {
		t.Fatalf("invalid JSONL: %v", err)
	}

	if !strings.Contains(pretty.String(), "task_start") {
		t.Errorf("pretty stdout missing event: %q", pretty.String())
	}
}

func TestRotateAtThreshold(t *testing.T) {
	dir := t.TempDir()
	pretty := &bytes.Buffer{}
	l, _ := Open(filepath.Join(dir, "run.log"), pretty)
	l.RotateBytes = 200 // tiny threshold for test
	defer l.Close()

	for i := 0; i < 50; i++ {
		l.Log(Event{Type: "noop", Fields: map[string]any{"i": i, "padding": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}})
	}

	entries, _ := os.ReadDir(dir)
	rotated := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "run-") && strings.HasSuffix(e.Name(), ".log.gz") {
			rotated++
		}
	}
	if rotated < 1 {
		t.Errorf("expected at least one rotated file, found 0")
	}
}
```

- [ ] **Step 2: Run test to confirm failure**

```bash
go test ./internal/eventlog/...
```

- [ ] **Step 3: Implement eventlog.go**

```go
package eventlog

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const DefaultRotateBytes = 50 * 1024 * 1024

type Event struct {
	Type   string
	Fields map[string]any
}

type Logger struct {
	mu          sync.Mutex
	path        string
	pretty      io.Writer
	f           *os.File
	RotateBytes int64
}

func Open(path string, pretty io.Writer) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &Logger{path: path, pretty: pretty, f: f, RotateBytes: DefaultRotateBytes}, nil
}

func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

func (l *Logger) Log(e Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	rec := map[string]any{"ts": time.Now().UTC().Format(time.RFC3339Nano), "event": e.Type}
	for k, v := range e.Fields {
		rec[k] = v
	}
	raw, _ := json.Marshal(rec)
	raw = append(raw, '\n')
	_, _ = l.f.Write(raw)
	_, _ = fmt.Fprintf(l.pretty, "[%s] %s %s\n", time.Now().Format("15:04:05"), e.Type, summariseFields(e.Fields))

	if info, err := l.f.Stat(); err == nil && info.Size() >= l.RotateBytes {
		l.rotateLocked()
	}
}

func (l *Logger) rotateLocked() {
	_ = l.f.Close()
	stamp := time.Now().UTC().Format("20060102T150405Z")
	gzPath := filepath.Join(filepath.Dir(l.path), fmt.Sprintf("run-%s.log.gz", stamp))
	src, err := os.Open(l.path)
	if err != nil {
		l.f, _ = os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		return
	}
	defer src.Close()
	dst, err := os.Create(gzPath)
	if err != nil {
		l.f, _ = os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		return
	}
	gz := gzip.NewWriter(dst)
	_, _ = io.Copy(gz, src)
	_ = gz.Close()
	_ = dst.Close()
	_ = os.Truncate(l.path, 0)
	l.f, _ = os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
}

func summariseFields(fs map[string]any) string {
	parts := make([]string, 0, len(fs))
	for k, v := range fs {
		if k == "result_snapshot" {
			parts = append(parts, k+"=...")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " "
		}
		out += p
	}
	return out
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/eventlog/...
```

Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/eventlog/
git commit -m "feat(eventlog): JSONL log with stdout pretty mirror and 50MB rotation"
```

---

## Task 9: `internal/runner` — subprocess execution with timeout and rate-limit detection

**Files:**
- Create: `internal/runner/runner.go`
- Create: `internal/runner/runner_test.go`

- [ ] **Step 1: Write the failing test**

```go
package runner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRunAgent_WritesResultJSON(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}
	dir := t.TempDir()
	resultPath := filepath.Join(dir, "out.json")

	cmdTpl := []string{"sh", "-c", "echo $0 > /dev/null; printf '%s' '{\"status\":\"ok\"}' > " + resultPath, "{{PROMPT}}"}

	res, err := RunAgent(context.Background(), Spec{
		Cmd:              cmdTpl,
		Prompt:           "hello world",
		ResultPath:       resultPath,
		Timeout:          5 * time.Second,
		RateLimitPattern: "rate_?limit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RateLimited {
		t.Errorf("unexpected rate_limited")
	}
	raw, _ := os.ReadFile(resultPath)
	if string(raw) != `{"status":"ok"}` {
		t.Errorf("result file content = %q", raw)
	}
}

func TestRunAgent_DetectsRateLimitFromStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}
	res, err := RunAgent(context.Background(), Spec{
		Cmd:              []string{"sh", "-c", "echo 'Error: rate_limit exceeded' 1>&2; exit 0", "{{PROMPT}}"},
		Prompt:           "x",
		ResultPath:       filepath.Join(t.TempDir(), "out.json"),
		Timeout:          5 * time.Second,
		RateLimitPattern: "rate_?limit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.RateLimited {
		t.Errorf("expected RateLimited=true")
	}
}

func TestRunAgent_TimeoutKills(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}
	start := time.Now()
	res, err := RunAgent(context.Background(), Spec{
		Cmd:              []string{"sh", "-c", "sleep 5", "{{PROMPT}}"},
		Prompt:           "x",
		ResultPath:       filepath.Join(t.TempDir(), "out.json"),
		Timeout:          500 * time.Millisecond,
		RateLimitPattern: "rate_?limit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Errorf("expected TimedOut=true")
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("did not kill in time: %v", time.Since(start))
	}
}
```

- [ ] **Step 2: Run test to confirm failure**

```bash
go test ./internal/runner/...
```

- [ ] **Step 3: Implement runner.go**

```go
package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type Spec struct {
	Cmd              []string
	Prompt           string
	ResultPath       string
	Timeout          time.Duration
	RateLimitPattern string
}

type Result struct {
	Stdout         string
	Stderr         string
	TimedOut       bool
	RateLimited    bool
	ResultExists   bool
	ExitCode       int
	Duration       time.Duration
}

func RunAgent(ctx context.Context, s Spec) (*Result, error) {
	if len(s.Cmd) == 0 {
		return nil, errors.New("empty cmd")
	}
	args := make([]string, len(s.Cmd))
	for i, tok := range s.Cmd {
		args[i] = strings.ReplaceAll(tok, "{{PROMPT}}", s.Prompt)
	}

	// Pre-clear stale result so we can detect "did not write".
	_ = os.Remove(s.ResultPath)

	cctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, args[0], args[1:]...)

	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)

	res := &Result{
		Stdout:   outBuf.String(),
		Stderr:   errBuf.String(),
		Duration: dur,
		ExitCode: cmd.ProcessState.ExitCode(),
	}
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
	}
	if s.RateLimitPattern != "" {
		re, errRe := regexp.Compile("(?i)" + s.RateLimitPattern)
		if errRe == nil && (re.MatchString(res.Stderr) || re.MatchString(res.Stdout)) {
			res.RateLimited = true
		}
	}
	if _, statErr := os.Stat(s.ResultPath); statErr == nil {
		res.ResultExists = true
	}
	if err != nil && !res.TimedOut && res.ExitCode != 0 {
		// Surface the error for the caller, but the return is not fatal — caller may still inspect Result.
		return res, nil
	}
	return res, nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/runner/...
```

Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/runner/
git commit -m "feat(runner): exec subprocess with timeout, capture out/err, detect rate limit"
```

---

## Task 10: `internal/fallback` — agent chain + cooldown + exponential backoff

**Files:**
- Create: `internal/fallback/fallback.go`
- Create: `internal/fallback/fallback_test.go`

- [ ] **Step 1: Write the failing test**

```go
package fallback

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeRun struct {
	rateLimited bool
	resultExists bool
	timedOut     bool
}

func TestCallRole_FirstAgentWins(t *testing.T) {
	calls := 0
	chain := []string{"a1", "a2"}
	cfg := Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: 10 * time.Millisecond, Now: time.Now}
	c := NewCaller(cfg)

	res, agentUsed, err := c.Call(context.Background(), chain, func(ctx context.Context, agent string) (Outcome, error) {
		calls++
		if agent == "a1" {
			return Outcome{ResultExists: true}, nil
		}
		return Outcome{ResultExists: false}, errors.New("should not run")
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || agentUsed != "a1" {
		t.Errorf("calls=%d agent=%s", calls, agentUsed)
	}
	if !res.ResultExists {
		t.Errorf("expected result")
	}
}

func TestCallRole_FallsOverOnRateLimit(t *testing.T) {
	chain := []string{"a1", "a2"}
	cfg := Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: 10 * time.Millisecond, Now: time.Now}
	c := NewCaller(cfg)
	_, agent, err := c.Call(context.Background(), chain, func(ctx context.Context, name string) (Outcome, error) {
		if name == "a1" {
			return Outcome{RateLimited: true}, nil
		}
		return Outcome{ResultExists: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if agent != "a2" {
		t.Errorf("expected a2, got %s", agent)
	}
}

func TestCallRole_AllRateLimitedThenSucceeds(t *testing.T) {
	chain := []string{"a1"}
	pass := 0
	cfg := Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: 10 * time.Millisecond, Now: time.Now}
	c := NewCaller(cfg)
	_, _, err := c.Call(context.Background(), chain, func(ctx context.Context, name string) (Outcome, error) {
		pass++
		if pass < 3 {
			return Outcome{RateLimited: true}, nil
		}
		return Outcome{ResultExists: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if pass != 3 {
		t.Errorf("expected 3 passes, got %d", pass)
	}
}

func TestCallRole_Exhausted(t *testing.T) {
	chain := []string{"a1"}
	cfg := Config{InitialBackoff: time.Millisecond, Factor: 2, MaxBackoff: 4 * time.Millisecond, Now: time.Now}
	c := NewCaller(cfg)
	_, _, err := c.Call(context.Background(), chain, func(ctx context.Context, name string) (Outcome, error) {
		return Outcome{RateLimited: true}, nil
	})
	if !errors.Is(err, ErrRateLimitExhausted) {
		t.Fatalf("expected ErrRateLimitExhausted, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to confirm failure**

```bash
go test ./internal/fallback/...
```

- [ ] **Step 3: Implement fallback.go**

```go
package fallback

import (
	"context"
	"errors"
	"time"
)

type Outcome struct {
	RateLimited  bool
	ResultExists bool
	TimedOut     bool
}

type Config struct {
	InitialBackoff time.Duration
	Factor         int
	MaxBackoff     time.Duration
	Now            func() time.Time
}

type Caller struct {
	cfg     Config
	cooldown map[string]time.Time
}

func NewCaller(cfg Config) *Caller {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Factor < 2 {
		cfg.Factor = 2
	}
	return &Caller{cfg: cfg, cooldown: map[string]time.Time{}}
}

var ErrRateLimitExhausted = errors.New("all agents rate-limited past max backoff")

type AgentFunc func(ctx context.Context, agent string) (Outcome, error)

func (c *Caller) Call(ctx context.Context, chain []string, fn AgentFunc) (Outcome, string, error) {
	backoff := c.cfg.InitialBackoff
	for {
		anyTried := false
		for _, agent := range chain {
			if cd, ok := c.cooldown[agent]; ok && cd.After(c.cfg.Now()) {
				continue
			}
			anyTried = true
			out, err := fn(ctx, agent)
			if err != nil {
				return out, agent, err
			}
			if out.RateLimited {
				c.cooldown[agent] = c.cfg.Now().Add(backoff)
				continue
			}
			return out, agent, nil
		}

		if !anyTried {
			// All agents in cooldown — wait for the soonest expiry, capped by backoff.
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return Outcome{}, "", ctx.Err()
			}
		} else {
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return Outcome{}, "", ctx.Err()
			}
		}

		next := backoff * time.Duration(c.cfg.Factor)
		if next > c.cfg.MaxBackoff {
			return Outcome{}, "", ErrRateLimitExhausted
		}
		backoff = next
	}
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/fallback/...
```

Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/fallback/
git commit -m "feat(fallback): role agent chain with cooldown memory and exponential backoff"
```

---

## Task 11: `internal/loops/fix` — coder → tester → critic loop

**Files:**
- Create: `internal/loops/fix.go`
- Create: `internal/loops/fix_test.go`

- [ ] **Step 1: Write the failing test**

The fix loop is tested with stub functions for each role call so we can simulate sequences without spawning real CLIs.

```go
package loops

import (
	"context"
	"testing"
)

type stubRoles struct {
	coder   func(attempt int, testerFB, criticFB string) CoderOutcome
	tester  func(attempt int) TesterOutcome
	critic  func(attempt int) CriticOutcome
}

func (s *stubRoles) RunCoder(ctx context.Context, attempt int, testerFB, criticFB string) (CoderOutcome, error) {
	return s.coder(attempt, testerFB, criticFB), nil
}
func (s *stubRoles) RunTester(ctx context.Context, attempt int) (TesterOutcome, error) {
	return s.tester(attempt), nil
}
func (s *stubRoles) RunCritic(ctx context.Context, attempt int) (CriticOutcome, error) {
	return s.critic(attempt), nil
}

func TestFix_PassFirstTry(t *testing.T) {
	r := &stubRoles{
		coder:  func(int, string, string) CoderOutcome { return CoderOutcome{Status: "completed"} },
		tester: func(int) TesterOutcome { return TesterOutcome{Status: "pass"} },
		critic: func(int) CriticOutcome { return CriticOutcome{Status: "approved"} },
	}
	out, err := RunFix(context.Background(), FixConfig{MaxIterations: 5}, r)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != FixDone || out.Iterations != 1 {
		t.Errorf("got %+v", out)
	}
}

func TestFix_TesterShortCircuitsCritic(t *testing.T) {
	criticCalls := 0
	r := &stubRoles{
		coder:  func(int, string, string) CoderOutcome { return CoderOutcome{Status: "completed"} },
		tester: func(a int) TesterOutcome { if a < 3 { return TesterOutcome{Status: "fail", FailuresHash: "h1"} }; return TesterOutcome{Status: "pass"} },
		critic: func(int) CriticOutcome { criticCalls++; return CriticOutcome{Status: "approved"} },
	}
	out, _ := RunFix(context.Background(), FixConfig{MaxIterations: 5}, r)
	if out.Status != FixDone {
		t.Errorf("status=%v", out.Status)
	}
	if criticCalls != 1 {
		t.Errorf("critic should run once (after tester finally passed), got %d", criticCalls)
	}
}

func TestFix_HitsMaxIterations(t *testing.T) {
	r := &stubRoles{
		coder:  func(int, string, string) CoderOutcome { return CoderOutcome{Status: "completed"} },
		tester: func(int) TesterOutcome { return TesterOutcome{Status: "fail", FailuresHash: "h"} },
		critic: func(int) CriticOutcome { return CriticOutcome{Status: "approved"} },
	}
	out, _ := RunFix(context.Background(), FixConfig{MaxIterations: 3}, r)
	if out.Status != FixFailed || out.Reason != "max_iterations" {
		t.Errorf("got %+v", out)
	}
	if out.Iterations != 3 {
		t.Errorf("iter=%d", out.Iterations)
	}
}

func TestFix_DetectsAgentRepeatedFailure(t *testing.T) {
	r := &stubRoles{
		coder:  func(int, string, string) CoderOutcome { return CoderOutcome{Status: "completed"} },
		tester: func(int) TesterOutcome { return TesterOutcome{Status: "fail", FailuresHash: "same"} },
		critic: func(int) CriticOutcome { return CriticOutcome{Status: "approved"} },
	}
	out, _ := RunFix(context.Background(), FixConfig{MaxIterations: 10}, r)
	if out.Reason != "agent_repeated_failure" {
		t.Errorf("expected agent_repeated_failure, got %q", out.Reason)
	}
	// Should detect on the second identical failure (iteration 2), so fewer than 10 iterations.
	if out.Iterations > 3 {
		t.Errorf("repeated-failure detection too slow: %d", out.Iterations)
	}
}

func TestFix_CriticVetoesAndReruns(t *testing.T) {
	criticCalls := 0
	r := &stubRoles{
		coder:  func(int, string, string) CoderOutcome { return CoderOutcome{Status: "completed"} },
		tester: func(int) TesterOutcome { return TesterOutcome{Status: "pass"} },
		critic: func(int) CriticOutcome { criticCalls++; if criticCalls < 2 { return CriticOutcome{Status: "rejected", Feedback: "bad"} }; return CriticOutcome{Status: "approved"} },
	}
	out, _ := RunFix(context.Background(), FixConfig{MaxIterations: 5}, r)
	if out.Status != FixDone || out.Iterations != 2 {
		t.Errorf("got %+v", out)
	}
}
```

- [ ] **Step 2: Run test to confirm failure**

```bash
go test ./internal/loops/...
```

- [ ] **Step 3: Implement fix.go**

```go
package loops

import (
	"context"
)

type CoderOutcome struct {
	Status  string // "completed" | "blocked"
	Summary string
}

type TesterOutcome struct {
	Status       string // "pass" | "fail"
	Feedback     string // injected back to coder
	FailuresHash string // for repeated-failure detection
}

type CriticOutcome struct {
	Status   string // "approved" | "rejected"
	Feedback string
}

type RoleRunner interface {
	RunCoder(ctx context.Context, attempt int, testerFB, criticFB string) (CoderOutcome, error)
	RunTester(ctx context.Context, attempt int) (TesterOutcome, error)
	RunCritic(ctx context.Context, attempt int) (CriticOutcome, error)
}

type FixStatus int

const (
	FixDone FixStatus = iota
	FixFailed
)

type FixConfig struct {
	MaxIterations int
}

type FixResult struct {
	Status     FixStatus
	Reason     string // "max_iterations" | "agent_repeated_failure" | "" when Done
	Iterations int
	LastFeedback string
}

func RunFix(ctx context.Context, cfg FixConfig, r RoleRunner) (*FixResult, error) {
	var testerFB, criticFB string
	var prevHash string

	for attempt := 1; attempt <= cfg.MaxIterations; attempt++ {
		if _, err := r.RunCoder(ctx, attempt, testerFB, criticFB); err != nil {
			return nil, err
		}

		t, err := r.RunTester(ctx, attempt)
		if err != nil {
			return nil, err
		}
		if t.Status == "fail" {
			if attempt > 1 && t.FailuresHash != "" && t.FailuresHash == prevHash {
				return &FixResult{Status: FixFailed, Reason: "agent_repeated_failure", Iterations: attempt, LastFeedback: t.Feedback}, nil
			}
			prevHash = t.FailuresHash
			testerFB = t.Feedback
			criticFB = ""
			continue
		}

		c, err := r.RunCritic(ctx, attempt)
		if err != nil {
			return nil, err
		}
		if c.Status == "approved" {
			return &FixResult{Status: FixDone, Iterations: attempt}, nil
		}
		criticFB = c.Feedback
		testerFB = ""
		// Critic rejection does not contribute to repeated-failure detection (resets prevHash).
		prevHash = ""
	}

	return &FixResult{Status: FixFailed, Reason: "max_iterations", Iterations: cfg.MaxIterations, LastFeedback: testerFB + criticFB}, nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/loops/...
```

Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/loops/fix.go internal/loops/fix_test.go
git commit -m "feat(loops): fix loop with short-circuit, AND gate, repeated-failure detection"
```

---

## Task 12: `internal/loops/task` — per-task driver: pick, run fix, full-suite gate, commit

**Files:**
- Create: `internal/loops/task.go`
- Create: `internal/loops/task_test.go`

This task wires the fix loop into actual orchestration: persist task statuses, run the full test suite as a pre-commit gate, commit on success or rollback on failure. To keep it testable, the actual git/test invocations are exposed as injectable functions.

- [ ] **Step 1: Write the failing test**

```go
package loops

import (
	"context"
	"testing"

	"github.com/lionelchamorro/pyorquesta/internal/tasks"
)

type stubTaskDeps struct {
	fix          func(taskID string) *FixResult
	fullSuite    func() error
	commit       func(msg string) (string, error)
	rollback     func() error
	saveTasks    func(*tasks.TaskList) error
	commits      []string
	rollbacks    int
}

func (s *stubTaskDeps) RunFix(ctx context.Context, taskID string) (*FixResult, error)  { return s.fix(taskID), nil }
func (s *stubTaskDeps) FullSuite(ctx context.Context) error                            { return s.fullSuite() }
func (s *stubTaskDeps) Commit(ctx context.Context, msg string) (string, error)         { sha, err := s.commit(msg); s.commits = append(s.commits, msg); return sha, err }
func (s *stubTaskDeps) Rollback(ctx context.Context) error                             { s.rollbacks++; return s.rollback() }
func (s *stubTaskDeps) SaveTasks(ctx context.Context, tl *tasks.TaskList) error        { return s.saveTasks(tl) }

func TestTaskLoop_HappyPath(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Title: "first", Status: tasks.StatusPending, Priority: 1},
	}}
	d := &stubTaskDeps{
		fix:       func(id string) *FixResult { return &FixResult{Status: FixDone, Iterations: 1} },
		fullSuite: func() error { return nil },
		commit:    func(msg string) (string, error) { return "abc1234", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}

	if err := RunTaskLoop(context.Background(), tl, d); err != nil {
		t.Fatal(err)
	}
	if tl.Tasks[0].Status != tasks.StatusDone {
		t.Errorf("expected Done, got %s", tl.Tasks[0].Status)
	}
	if len(d.commits) != 1 {
		t.Errorf("commits=%d", len(d.commits))
	}
}

func TestTaskLoop_FullSuiteFailureRollsBack(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Status: tasks.StatusPending, Priority: 1},
	}}
	d := &stubTaskDeps{
		fix:       func(id string) *FixResult { return &FixResult{Status: FixDone, Iterations: 1} },
		fullSuite: func() error { return ErrFullSuiteFailed },
		commit:    func(msg string) (string, error) { return "x", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}
	_ = RunTaskLoop(context.Background(), tl, d)
	if tl.Tasks[0].Status != tasks.StatusFailed {
		t.Errorf("expected Failed, got %s", tl.Tasks[0].Status)
	}
	if tl.Tasks[0].FailureReason == nil || *tl.Tasks[0].FailureReason != tasks.ReasonFullSuiteFailed {
		t.Errorf("failure_reason wrong: %+v", tl.Tasks[0].FailureReason)
	}
	if d.rollbacks != 1 {
		t.Errorf("rollback should run, got %d", d.rollbacks)
	}
}

func TestTaskLoop_FixFailedMarkedFailed(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Status: tasks.StatusPending, Priority: 1},
	}}
	d := &stubTaskDeps{
		fix:       func(id string) *FixResult { return &FixResult{Status: FixFailed, Reason: "max_iterations", Iterations: 5} },
		fullSuite: func() error { return nil },
		commit:    func(string) (string, error) { return "", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}
	_ = RunTaskLoop(context.Background(), tl, d)
	if tl.Tasks[0].Status != tasks.StatusFailed || *tl.Tasks[0].FailureReason != tasks.ReasonMaxIterations {
		t.Errorf("got status=%s reason=%v", tl.Tasks[0].Status, tl.Tasks[0].FailureReason)
	}
}

func TestTaskLoop_SkipsAlreadyDone(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Status: tasks.StatusDone, Priority: 1},
		{ID: "T002", Status: tasks.StatusPending, Priority: 2},
	}}
	called := 0
	d := &stubTaskDeps{
		fix:       func(string) *FixResult { called++; return &FixResult{Status: FixDone, Iterations: 1} },
		fullSuite: func() error { return nil },
		commit:    func(string) (string, error) { return "x", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}
	_ = RunTaskLoop(context.Background(), tl, d)
	if called != 1 {
		t.Errorf("fix should run once for T002, ran %d times", called)
	}
}
```

- [ ] **Step 2: Run test to confirm failure**

```bash
go test ./internal/loops/...
```

- [ ] **Step 3: Implement task.go**

```go
package loops

import (
	"context"
	"errors"
	"fmt"

	"github.com/lionelchamorro/pyorquesta/internal/tasks"
)

var ErrFullSuiteFailed = errors.New("full test suite failed")

type TaskDeps interface {
	RunFix(ctx context.Context, taskID string) (*FixResult, error)
	FullSuite(ctx context.Context) error
	Commit(ctx context.Context, msg string) (string, error)
	Rollback(ctx context.Context) error
	SaveTasks(ctx context.Context, tl *tasks.TaskList) error
}

func RunTaskLoop(ctx context.Context, tl *tasks.TaskList, d TaskDeps) error {
	for {
		t := tl.NextPending()
		if t == nil {
			return nil
		}
		t.Status = tasks.StatusInProgress
		t.Attempts++
		_ = d.SaveTasks(ctx, tl)

		fx, err := d.RunFix(ctx, t.ID)
		if err != nil {
			return err
		}
		if fx.Status == FixFailed {
			t.Status = tasks.StatusFailed
			t.LastFeedback = strPtr(fx.LastFeedback)
			r := tasks.FailureReason(fx.Reason)
			t.FailureReason = &r
			_ = d.Rollback(ctx)
			_ = d.SaveTasks(ctx, tl)
			continue
		}

		if err := d.FullSuite(ctx); err != nil {
			t.Status = tasks.StatusFailed
			r := tasks.ReasonFullSuiteFailed
			t.FailureReason = &r
			t.LastFeedback = strPtr(err.Error())
			_ = d.Rollback(ctx)
			_ = d.SaveTasks(ctx, tl)
			continue
		}

		msg := fmt.Sprintf("feat(%s): %s", t.ID, t.Title)
		if _, err := d.Commit(ctx, msg); err != nil {
			t.Status = tasks.StatusFailed
			r := tasks.ReasonCommitRejected
			t.FailureReason = &r
			t.LastFeedback = strPtr(err.Error())
			_ = d.Rollback(ctx)
			_ = d.SaveTasks(ctx, tl)
			continue
		}

		t.Status = tasks.StatusDone
		_ = d.SaveTasks(ctx, tl)
	}
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/loops/...
```

Expected: all tests PASS (fix + task = 9 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/loops/task.go internal/loops/task_test.go
git commit -m "feat(loops): task loop drives fix, full-suite gate, commit-or-rollback"
```

---

## Task 13: `internal/loops/review` — outer loop with reviewer at end of cycle

**Files:**
- Create: `internal/loops/review.go`
- Create: `internal/loops/review_test.go`

- [ ] **Step 1: Write the failing test**

```go
package loops

import (
	"context"
	"testing"

	"github.com/lionelchamorro/pyorquesta/internal/results"
	"github.com/lionelchamorro/pyorquesta/internal/tasks"
)

type stubReviewDeps struct {
	taskDeps  TaskDeps
	reviewer  func(cycle int) results.ReviewerResult
	cycles    []int
}

func (s *stubReviewDeps) RunFix(ctx context.Context, id string) (*FixResult, error) { return s.taskDeps.RunFix(ctx, id) }
func (s *stubReviewDeps) FullSuite(ctx context.Context) error                       { return s.taskDeps.FullSuite(ctx) }
func (s *stubReviewDeps) Commit(ctx context.Context, m string) (string, error)      { return s.taskDeps.Commit(ctx, m) }
func (s *stubReviewDeps) Rollback(ctx context.Context) error                        { return s.taskDeps.Rollback(ctx) }
func (s *stubReviewDeps) SaveTasks(ctx context.Context, tl *tasks.TaskList) error   { return s.taskDeps.SaveTasks(ctx, tl) }
func (s *stubReviewDeps) RunReviewer(ctx context.Context, cycle int) (results.ReviewerResult, error) {
	s.cycles = append(s.cycles, cycle)
	return s.reviewer(cycle), nil
}

func boolPtr(b bool) *bool { return &b }

func TestReview_StopsWhenReviewerSaysSo(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{{ID: "T001", Status: tasks.StatusPending, Priority: 1}}}
	td := &stubTaskDeps{
		fix:       func(string) *FixResult { return &FixResult{Status: FixDone, Iterations: 1} },
		fullSuite: func() error { return nil },
		commit:    func(string) (string, error) { return "x", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}
	d := &stubReviewDeps{
		taskDeps: td,
		reviewer: func(int) results.ReviewerResult { return results.ReviewerResult{ShouldStop: boolPtr(true)} },
	}
	if err := RunReviewLoop(context.Background(), tl, ReviewConfig{MaxCycles: 5}, d); err != nil {
		t.Fatal(err)
	}
	if len(d.cycles) != 1 {
		t.Errorf("cycles ran = %d, want 1", len(d.cycles))
	}
}

func TestReview_StopsAtMaxCycles(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{{ID: "T001", Status: tasks.StatusPending, Priority: 1}}}
	td := &stubTaskDeps{
		fix:       func(string) *FixResult { return &FixResult{Status: FixDone, Iterations: 1} },
		fullSuite: func() error { return nil },
		commit:    func(string) (string, error) { return "x", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}
	d := &stubReviewDeps{
		taskDeps: td,
		reviewer: func(int) results.ReviewerResult {
			return results.ReviewerResult{ShouldStop: boolPtr(false), NewTasks: []results.ReviewerNewTask{{Title: "x", Description: "y", Priority: 1}}}
		},
	}
	_ = RunReviewLoop(context.Background(), tl, ReviewConfig{MaxCycles: 2}, d)
	if len(d.cycles) != 2 {
		t.Errorf("cycles=%d, want 2", len(d.cycles))
	}
}

func TestReview_AppendsNewTasks(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{{ID: "T001", Status: tasks.StatusPending, Priority: 1}}}
	td := &stubTaskDeps{
		fix:       func(string) *FixResult { return &FixResult{Status: FixDone, Iterations: 1} },
		fullSuite: func() error { return nil },
		commit:    func(string) (string, error) { return "x", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}
	called := false
	d := &stubReviewDeps{
		taskDeps: td,
		reviewer: func(c int) results.ReviewerResult {
			if !called {
				called = true
				return results.ReviewerResult{ShouldStop: boolPtr(false), NewTasks: []results.ReviewerNewTask{
					{Title: "follow-up", Description: "y", Priority: 5},
				}}
			}
			return results.ReviewerResult{ShouldStop: boolPtr(true)}
		},
	}
	_ = RunReviewLoop(context.Background(), tl, ReviewConfig{MaxCycles: 5}, d)
	if len(tl.Tasks) != 2 {
		t.Errorf("expected 2 tasks after append, got %d", len(tl.Tasks))
	}
	if tl.Tasks[1].Title != "follow-up" || tl.Tasks[1].CreatedInReviewCycle != 1 {
		t.Errorf("appended task wrong: %+v", tl.Tasks[1])
	}
}
```

- [ ] **Step 2: Run test to confirm failure**

```bash
go test ./internal/loops/...
```

- [ ] **Step 3: Implement review.go**

```go
package loops

import (
	"context"

	"github.com/lionelchamorro/pyorquesta/internal/results"
	"github.com/lionelchamorro/pyorquesta/internal/tasks"
)

type ReviewDeps interface {
	TaskDeps
	RunReviewer(ctx context.Context, cycle int) (results.ReviewerResult, error)
}

type ReviewConfig struct {
	MaxCycles int
}

func RunReviewLoop(ctx context.Context, tl *tasks.TaskList, cfg ReviewConfig, d ReviewDeps) error {
	for cycle := 1; cycle <= cfg.MaxCycles; cycle++ {
		if err := RunTaskLoop(ctx, tl, d); err != nil {
			return err
		}

		rev, err := d.RunReviewer(ctx, cycle)
		if err != nil {
			return err
		}

		// Convert reviewer.NewTasks → tasks.Task and append.
		newOnes := make([]tasks.Task, 0, len(rev.NewTasks))
		for _, n := range rev.NewTasks {
			newOnes = append(newOnes, tasks.Task{Title: n.Title, Description: n.Description, Priority: n.Priority})
		}
		tl.Append(newOnes, cycle)
		_ = d.SaveTasks(ctx, tl)

		if rev.ShouldStop != nil && *rev.ShouldStop {
			return nil
		}
		if !tl.AnyPending() && len(rev.NewTasks) == 0 {
			return nil
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/loops/...
```

Expected: all loops tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/loops/review.go internal/loops/review_test.go
git commit -m "feat(loops): review loop with reviewer veto and max-cycle ceiling"
```

---

## Task 14: `internal/commands/initcmd` — `pyorquesta init` scaffolding

**Files:**
- Create: `internal/commands/initcmd.go`
- Create: `internal/commands/initcmd_test.go`
- Create: `prompts/parser.md`, `prompts/coder.md`, `prompts/tester.md`, `prompts/critic.md`, `prompts/reviewer.md` (default templates shipped via Go embed)

- [ ] **Step 1: Write default prompt templates**

Each prompt is a markdown file with the role's instructions and the JSON contract it must produce. Keep them tight (the CLI subprocess will inject them via `{{PROMPT}}`).

`prompts/parser.md`:

```markdown
You are the **parser** in a Ralph orchestrator. Read the plan below and split it into atomic, testable tasks. Each task should be small enough to complete in a single focused change.

## Memory (prior cycles)

{{MEMORY}}

## Plan

{{PLAN}}

## Output contract

Your final action MUST be to write `.pyorquesta/results/parser.json` with the exact shape:

```json
{
  "tasks": [
    { "title": "short title", "description": "what to do + acceptance criteria", "priority": 1 }
  ],
  "notes_for_memory": null
}
```

Priorities are integers, lower = higher priority. `notes_for_memory` should be `null` unless you learned something non-obvious that future iterations need.
```

`prompts/coder.md`:

```markdown
You are the **coder**. Implement the task. Write code, tests, and ensure the change is self-contained.

## Memory

{{MEMORY}}

## Task

**ID:** {{TASK_ID}}
**Title:** {{TASK_TITLE}}
**Description:**
{{TASK_DESCRIPTION}}

## Attempt {{ATTEMPT_NUMBER}}

{{TESTER_FEEDBACK}}

{{CRITIC_FEEDBACK}}

## Output contract

Your final action MUST be to write `.pyorquesta/results/coder.json`:

```json
{
  "status": "completed" | "blocked",
  "summary": "what you did, or why blocked",
  "files_changed": ["path/to/file"],
  "notes_for_memory": null
}
```
```

`prompts/tester.md`:

```markdown
You are the **tester**. Run the relevant tests for the change just made. Do not modify source code.

## Task

{{TASK_TITLE}}: {{TASK_DESCRIPTION}}

## Files changed by coder

{{FILES_CHANGED}}

## Output contract

Run the tests, then write `.pyorquesta/results/tester.json`:

```json
{
  "status": "pass" | "fail",
  "command_run": "<the exact command you ran>",
  "failures": [
    { "test": "name", "message": "...", "hint": "what the coder probably missed" }
  ],
  "notes_for_memory": null
}
```
```

`prompts/critic.md`:

```markdown
You are the **critic**. Review the change for design quality, hidden bugs, missing edge cases. You can VETO with `rejected`, but you cannot create new tasks. Out-of-scope concerns go in `notes_for_memory`.

## Task

{{TASK_TITLE}}: {{TASK_DESCRIPTION}}

## Files changed

{{FILES_CHANGED}}

## Output contract

```json
{
  "status": "approved" | "rejected",
  "concerns": [
    { "severity": "blocker" | "nit", "where": "file:line", "issue": "...", "suggestion": "..." }
  ],
  "notes_for_memory": null
}
```
```

`prompts/reviewer.md`:

```markdown
You are the **reviewer** at the end of cycle {{REVIEW_CYCLE}}. Inspect what was done this cycle and decide: should we stop, or are there valuable follow-up tasks?

## Memory

{{MEMORY}}

## Tasks state

{{TASKS_JSON}}

## Commits this cycle

{{GIT_LOG}}

## Output contract

```json
{
  "summary_of_cycle": "...",
  "new_tasks": [
    { "title": "...", "description": "...", "priority": 1 }
  ],
  "should_stop": true | false,
  "notes_for_memory": null
}
```
```

- [ ] **Step 2: Write the failing test**

```go
package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInit_CreatesScaffolding(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"team.json",
		"prompts/parser.md", "prompts/coder.md", "prompts/tester.md", "prompts/critic.md", "prompts/reviewer.md",
		".pyorquesta/results",
	} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
}

func TestInit_AddsGitignoreEntry(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("# user\nnode_modules/\n"), 0o644)
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.Contains(string(raw), ".pyorquesta/") {
		t.Errorf(".gitignore did not get .pyorquesta/: %s", raw)
	}
	if !strings.Contains(string(raw), "node_modules/") {
		t.Errorf("init must not delete prior gitignore lines: %s", raw)
	}
}

func TestInit_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir); err != nil {
		t.Fatalf("second init failed: %v", err)
	}
}
```

- [ ] **Step 3: Implement initcmd.go**

```go
package commands

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed assets/team.json assets/prompts/*.md
var defaultAssets embed.FS

func Init(dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, ".pyorquesta", "results"), 0o755); err != nil {
		return err
	}
	if err := writeIfMissing(filepath.Join(dir, "team.json"), mustReadAsset("assets/team.json")); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		return err
	}
	entries, err := fs.ReadDir(defaultAssets, "assets/prompts")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := writeIfMissing(
			filepath.Join(dir, "prompts", e.Name()),
			mustReadAsset("assets/prompts/"+e.Name()),
		); err != nil {
			return err
		}
	}
	return ensureGitignoreEntry(filepath.Join(dir, ".gitignore"), ".pyorquesta/")
}

func writeIfMissing(path string, content []byte) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, content, 0o644)
}

func mustReadAsset(p string) []byte {
	raw, err := defaultAssets.ReadFile(p)
	if err != nil {
		panic(fmt.Sprintf("missing embedded asset %s: %v", p, err))
	}
	return raw
}

func ensureGitignoreEntry(path, entry string) error {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return os.WriteFile(path, []byte(entry+"\n"), 0o644)
	}
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == entry {
			return nil
		}
	}
	body := string(raw)
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return os.WriteFile(path, []byte(body+entry+"\n"), 0o644)
}
```

- [ ] **Step 4: Create embedded assets**

Create `internal/commands/assets/team.json` with a minimal valid default team:

```json
{
  "agents": {
    "claude_sonnet": { "cmd": ["claude", "-p", "{{PROMPT}}", "--model", "claude-sonnet-4-6"], "rate_limit_pattern": "rate_?limit|429|quota" },
    "claude_opus":   { "cmd": ["claude", "-p", "{{PROMPT}}", "--model", "claude-opus-4-7"] }
  },
  "roles": {
    "parser":   { "agents": ["claude_opus"],                       "prompt": "prompts/parser.md",   "result_path": ".pyorquesta/results/parser.json",   "timeout_seconds": 300 },
    "coder":    { "agents": ["claude_sonnet", "claude_opus"],      "prompt": "prompts/coder.md",    "result_path": ".pyorquesta/results/coder.json",    "timeout_seconds": 900 },
    "tester":   { "agents": ["claude_sonnet"],                     "prompt": "prompts/tester.md",   "result_path": ".pyorquesta/results/tester.json",   "timeout_seconds": 600 },
    "critic":   { "agents": ["claude_opus", "claude_sonnet"],      "prompt": "prompts/critic.md",   "result_path": ".pyorquesta/results/critic.json",   "timeout_seconds": 300 },
    "reviewer": { "agents": ["claude_opus"],                       "prompt": "prompts/reviewer.md", "result_path": ".pyorquesta/results/reviewer.json", "timeout_seconds": 600 }
  },
  "limits": { "max_review_cycles": 3, "max_fix_iterations": 5 },
  "rate_limit_backoff": { "initial_seconds": 30, "factor": 2, "max_seconds": 1800, "default_pattern": "rate_?limit|429|quota|too many requests" },
  "full_test_command": "go test ./..."
}
```

Copy the five prompt files written in Step 1 into `internal/commands/assets/prompts/`.

- [ ] **Step 5: Run tests**

```bash
go test ./internal/commands/...
```

Expected: PASS (3 tests).

- [ ] **Step 6: Commit**

```bash
git add internal/commands/initcmd.go internal/commands/initcmd_test.go internal/commands/assets/ prompts/
git commit -m "feat(init): scaffold .pyorquesta, default team.json, prompts, .gitignore entry"
```

---

## Task 15: `internal/commands/plancmd` — `pyorquesta plan plan.md`

**Files:**
- Create: `internal/commands/plancmd.go`
- Create: `internal/commands/plancmd_test.go`

This command invokes the parser role and persists `tasks.json`. Because invoking a real CLI is not deterministic, the command exposes a `RoleCaller` interface so tests can substitute a stub.

- [ ] **Step 1: Write the failing test**

```go
package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lionelchamorro/pyorquesta/internal/results"
	"github.com/lionelchamorro/pyorquesta/internal/tasks"
)

type stubParserCaller struct {
	out results.ParserResult
}

func (s *stubParserCaller) RunParser(ctx context.Context, plan string) (*results.ParserResult, error) {
	return &s.out, nil
}

func TestPlan_WritesTasksJSON(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.md")
	_ = os.WriteFile(planPath, []byte("# build x\nadd login flow"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, ".pyorquesta"), 0o755)

	stub := &stubParserCaller{out: results.ParserResult{Tasks: []results.ParserTask{
		{Title: "scaffold", Description: "set up repo", Priority: 1},
		{Title: "add login route", Description: "POST /login", Priority: 2},
	}}}

	if err := Plan(context.Background(), dir, planPath, false, stub); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, ".pyorquesta", "tasks.json"))
	var tl tasks.TaskList
	if err := json.Unmarshal(raw, &tl); err != nil {
		t.Fatal(err)
	}
	if len(tl.Tasks) != 2 || tl.Tasks[0].ID != "T001" || tl.Tasks[1].ID != "T002" {
		t.Fatalf("got tasks: %+v", tl.Tasks)
	}
	if tl.Tasks[0].Status != tasks.StatusPending {
		t.Errorf("status not initialised")
	}
}

func TestPlan_AppendPreservesExisting(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".pyorquesta"), 0o755)
	prev := &tasks.TaskList{Tasks: []tasks.Task{{ID: "T001", Title: "old", Status: tasks.StatusDone}}}
	raw, _ := json.MarshalIndent(prev, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, ".pyorquesta", "tasks.json"), raw, 0o644)

	planPath := filepath.Join(dir, "plan.md")
	_ = os.WriteFile(planPath, []byte("more"), 0o644)
	stub := &stubParserCaller{out: results.ParserResult{Tasks: []results.ParserTask{{Title: "new", Description: "y", Priority: 1}}}}

	if err := Plan(context.Background(), dir, planPath, true, stub); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(filepath.Join(dir, ".pyorquesta", "tasks.json"))
	var tl tasks.TaskList
	_ = json.Unmarshal(out, &tl)
	if len(tl.Tasks) != 2 || tl.Tasks[1].ID != "T002" {
		t.Fatalf("append failed: %+v", tl.Tasks)
	}
}
```

- [ ] **Step 2: Run test to confirm failure**

```bash
go test ./internal/commands/...
```

- [ ] **Step 3: Implement plancmd.go**

```go
package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lionelchamorro/pyorquesta/internal/results"
	"github.com/lionelchamorro/pyorquesta/internal/tasks"
)

type ParserCaller interface {
	RunParser(ctx context.Context, plan string) (*results.ParserResult, error)
}

func Plan(ctx context.Context, projectDir, planPath string, append bool, caller ParserCaller) error {
	planRaw, err := os.ReadFile(planPath)
	if err != nil {
		return fmt.Errorf("read plan: %w", err)
	}
	parsed, err := caller.RunParser(ctx, string(planRaw))
	if err != nil {
		return fmt.Errorf("parser: %w", err)
	}

	tasksPath := filepath.Join(projectDir, ".pyorquesta", "tasks.json")
	var tl *tasks.TaskList
	if append {
		existing, err := tasks.Load(tasksPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if existing == nil {
			tl = &tasks.TaskList{}
		} else {
			tl = existing
		}
	} else {
		tl = &tasks.TaskList{}
	}

	converted := make([]tasks.Task, 0, len(parsed.Tasks))
	for _, p := range parsed.Tasks {
		converted = append(converted, tasks.Task{Title: p.Title, Description: p.Description, Priority: p.Priority})
	}
	tl.Append(converted, 0)
	return tasks.Save(tasksPath, tl)
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/commands/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/commands/plancmd.go internal/commands/plancmd_test.go
git commit -m "feat(plan): parser invocation + tasks.json persistence with --append"
```

---

## Task 16: `internal/commands/runcmd` — `pyorquesta run` wires everything

**Files:**
- Create: `internal/commands/runcmd.go`
- Create: `internal/commands/runcmd_test.go`

This is the wire-up: it composes config + tasks + runner + fallback + loops + eventlog + memory + gitx into a runnable command. Because the underlying CLI calls are subprocess-bound, the test uses a fake CLI script.

- [ ] **Step 1: Write the failing test**

```go
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

// fakeCLI writes a fixed result.json and exits 0. Used to drive a real
// end-to-end run without hitting any model.
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
case "$kind" in
  coder)    printf '%s' '{"status":"completed","summary":"ok","files_changed":["x.txt"],"notes_for_memory":null}' > "$result" ;;
  tester)   printf '%s' '{"status":"pass","command_run":"true","failures":[],"notes_for_memory":null}' > "$result" ;;
  critic)   printf '%s' '{"status":"approved","concerns":[],"notes_for_memory":null}' > "$result" ;;
  reviewer) printf '%s' '{"summary_of_cycle":"done","new_tasks":[],"should_stop":true,"notes_for_memory":null}' > "$result" ;;
  parser)   printf '%s' '{"tasks":[{"title":"t","description":"d","priority":1}],"notes_for_memory":null}' > "$result" ;;
esac
exit 0
`

func TestRun_EndToEndWithFakeCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	dir := t.TempDir()
	// init git
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", "x@x"}, {"config", "user.name", "x"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		c := exec.Command("git", args...); c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil { t.Fatalf("git %v: %v %s", args, err, out) }
	}

	if err := Init(dir); err != nil { t.Fatal(err) }

	// Replace team.json with one pointing at the fake CLI.
	cliPath := filepath.Join(dir, "fakecli.sh")
	_ = os.WriteFile(cliPath, []byte(fakeCLI), 0o755)

	team := map[string]any{
		"agents": map[string]any{
			"fake": map[string]any{ "cmd": []string{"sh", cliPath, "-p", "{{PROMPT}}", "--result", ".pyorquesta/results/coder.json", "--kind", "coder"} },
		},
	}
	_ = team // for brevity below we'll skip the full team.json composition; assume a helper builds one role per CLI invocation.

	// Pre-seed tasks.json with one pending task.
	tl := &tasks.TaskList{Tasks: []tasks.Task{{ID: "T001", Title: "demo", Description: "demo", Status: tasks.StatusPending, Priority: 1}}}
	raw, _ := json.MarshalIndent(tl, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, ".pyorquesta", "tasks.json"), raw, 0o644)

	if err := Run(context.Background(), RunOptions{ProjectDir: dir, TeamPath: filepath.Join(dir, "team.json")}); err != nil {
		t.Fatalf("run: %v", err)
	}
}
```

> **Note for the implementing agent:** the test above sketches the e2e flow but the real `team.json` for a fake-CLI test has to map five distinct role result paths and `--kind` arguments. When implementing this task, write a helper `writeFakeTeamJSON(t, dir)` that emits the full `team.json` for the five roles all pointing at `fakecli.sh` with appropriate flags. Keep the test focused on "the run completes without error and tasks.json shows T001 = done".

- [ ] **Step 2: Implement runcmd.go**

```go
package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lionelchamorro/pyorquesta/internal/config"
	"github.com/lionelchamorro/pyorquesta/internal/eventlog"
	"github.com/lionelchamorro/pyorquesta/internal/fallback"
	"github.com/lionelchamorro/pyorquesta/internal/gitx"
	"github.com/lionelchamorro/pyorquesta/internal/loops"
	"github.com/lionelchamorro/pyorquesta/internal/memory"
	"github.com/lionelchamorro/pyorquesta/internal/prompts"
	"github.com/lionelchamorro/pyorquesta/internal/results"
	"github.com/lionelchamorro/pyorquesta/internal/runner"
	"github.com/lionelchamorro/pyorquesta/internal/tasks"
)

type RunOptions struct {
	ProjectDir string
	TeamPath   string
}

func Run(ctx context.Context, opts RunOptions) error {
	cfg, err := config.Load(opts.TeamPath)
	if err != nil {
		return err
	}
	tasksPath := filepath.Join(opts.ProjectDir, ".pyorquesta", "tasks.json")
	tl, err := tasks.Load(tasksPath)
	if err != nil {
		return err
	}

	logPath := filepath.Join(opts.ProjectDir, ".pyorquesta", "run.log")
	logger, err := eventlog.Open(logPath, os.Stdout)
	if err != nil {
		return err
	}
	defer logger.Close()

	memPath := filepath.Join(opts.ProjectDir, ".pyorquesta", "memory.md")

	fc := fallback.NewCaller(fallback.Config{
		InitialBackoff: time.Duration(cfg.RateLimitBackoff.InitialSeconds) * time.Second,
		Factor:         cfg.RateLimitBackoff.Factor,
		MaxBackoff:     time.Duration(cfg.RateLimitBackoff.MaxSeconds) * time.Second,
	})

	deps := &liveDeps{
		cfg: cfg, dir: opts.ProjectDir, fc: fc, log: logger, tl: tl,
		memPath: memPath, tasksPath: tasksPath, currentCycle: 0,
	}

	return loops.RunReviewLoop(ctx, tl, loops.ReviewConfig{MaxCycles: cfg.Limits.MaxReviewCycles}, deps)
}

type liveDeps struct {
	cfg          *config.Config
	dir          string
	fc           *fallback.Caller
	log          *eventlog.Logger
	tl           *tasks.TaskList
	memPath      string
	tasksPath    string
	currentCycle int
	currentTask  *tasks.Task
}

func (d *liveDeps) RunFix(ctx context.Context, taskID string) (*loops.FixResult, error) {
	for i, t := range d.tl.Tasks {
		if t.ID == taskID {
			d.currentTask = &d.tl.Tasks[i]
			break
		}
	}
	rr := &liveRoleRunner{deps: d}
	return loops.RunFix(ctx, loops.FixConfig{MaxIterations: d.cfg.Limits.MaxFixIterations}, rr)
}

func (d *liveDeps) FullSuite(ctx context.Context) error {
	parts := strings.Fields(d.cfg.FullTestCommand)
	if len(parts) == 0 {
		return nil
	}
	c := exec.CommandContext(ctx, parts[0], parts[1:]...)
	c.Dir = d.dir
	out, err := c.CombinedOutput()
	if err != nil {
		d.log.Log(eventlog.Event{Type: "full_suite_failed", Fields: map[string]any{"output_tail": tailString(string(out), 1024)}})
		return loops.ErrFullSuiteFailed
	}
	return nil
}

func (d *liveDeps) Commit(ctx context.Context, msg string) (string, error) {
	sha, err := gitx.CommitAll(d.dir, msg)
	if err != nil {
		return "", err
	}
	d.log.Log(eventlog.Event{Type: "task_done", Fields: map[string]any{"task_id": d.currentTask.ID, "commit_sha": sha}})
	return sha, nil
}

func (d *liveDeps) Rollback(ctx context.Context) error {
	return gitx.CheckoutAll(d.dir)
}

func (d *liveDeps) SaveTasks(ctx context.Context, tl *tasks.TaskList) error {
	return tasks.Save(d.tasksPath, tl)
}

func (d *liveDeps) RunReviewer(ctx context.Context, cycle int) (results.ReviewerResult, error) {
	d.currentCycle = cycle
	role := d.cfg.Roles["reviewer"]
	tmpl, err := prompts.Load(filepath.Join(d.dir, role.Prompt))
	if err != nil {
		return results.ReviewerResult{}, err
	}
	mem, _ := memory.ReadAll(d.memPath)
	tasksRaw, _ := os.ReadFile(d.tasksPath)
	gitLog := ""
	if sha, err := gitx.HeadSHA(d.dir); err == nil {
		// We don't track cycle-start SHA here; pass the last 5 commits instead for v0.
		_ = sha
		gitLog, _ = gitx.LogStat(d.dir, "HEAD~5")
	}
	prompt := prompts.Interpolate(tmpl, map[string]string{
		"REVIEW_CYCLE": fmt.Sprintf("%d", cycle), "MEMORY": mem,
		"TASKS_JSON": string(tasksRaw), "GIT_LOG": gitLog,
	})
	if err := d.callRole("reviewer", prompt, role); err != nil {
		return results.ReviewerResult{}, err
	}
	r, err := results.ParseReviewer(filepath.Join(d.dir, role.ResultPath))
	if err != nil {
		return results.ReviewerResult{}, err
	}
	if r.NotesForMemory != nil {
		_ = memory.Append(d.memPath, memory.Entry{Cycle: cycle, TaskID: "-", Role: "reviewer", Body: *r.NotesForMemory})
	}
	return *r, nil
}

func (d *liveDeps) callRole(roleName, prompt string, role config.Role) error {
	res, agent, err := d.fc.Call(context.Background(), role.Agents, func(ctx context.Context, agentName string) (fallback.Outcome, error) {
		ag := d.cfg.Agents[agentName]
		pattern := ag.RateLimitPattern
		if pattern == "" {
			pattern = d.cfg.RateLimitBackoff.DefaultPattern
		}
		spec := runner.Spec{
			Cmd: ag.Cmd, Prompt: prompt,
			ResultPath: filepath.Join(d.dir, role.ResultPath),
			Timeout: time.Duration(role.TimeoutSeconds) * time.Second,
			RateLimitPattern: pattern,
		}
		r, err := runner.RunAgent(ctx, spec)
		if err != nil {
			return fallback.Outcome{}, err
		}
		d.log.Log(eventlog.Event{Type: "agent_run", Fields: map[string]any{
			"role": roleName, "agent": agentName, "duration_s": int(r.Duration.Seconds()),
			"timed_out": r.TimedOut, "rate_limited": r.RateLimited, "result_exists": r.ResultExists,
		}})
		return fallback.Outcome{RateLimited: r.RateLimited, ResultExists: r.ResultExists, TimedOut: r.TimedOut}, nil
	})
	_ = agent
	if errors.Is(err, fallback.ErrRateLimitExhausted) {
		return err
	}
	if err != nil {
		return err
	}
	if !res.ResultExists {
		return fmt.Errorf("agent did not write result file")
	}
	return nil
}

func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
```

> **Note for implementing agent:** `liveRoleRunner` (the type used inside `RunFix`) needs to bind `coder/tester/critic` calls similarly to `RunReviewer` above. Implement it analogously: build prompt with the right interpolation vars (`{{TASK_TITLE}}`, `{{TESTER_FEEDBACK}}`, etc.), invoke `callRole`, parse result, append to memory if non-null. Compute `FailuresHash` from `TesterResult.Failures` for repeated-failure detection (e.g. SHA-256 of `json.Marshal(failures)`).

- [ ] **Step 3: Run tests**

```bash
go test ./...
```

Expected: end-to-end test passes (T001 ends in `done`).

- [ ] **Step 4: Commit**

```bash
git add internal/commands/runcmd.go internal/commands/runcmd_test.go
git commit -m "feat(run): wire config+runner+fallback+loops+log+memory into pyorquesta run"
```

---

## Task 17: `internal/commands/statuscmd` — `pyorquesta status` table + --watch

**Files:**
- Create: `internal/commands/statuscmd.go`
- Create: `internal/commands/statuscmd_test.go`

- [ ] **Step 1: Write the failing test**

```go
package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lionelchamorro/pyorquesta/internal/tasks"
)

func TestStatus_PrintsTable(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".pyorquesta"), 0o755)
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Title: "first", Status: tasks.StatusDone, Priority: 1},
		{ID: "T002", Title: "second", Status: tasks.StatusPending, Priority: 2},
	}}
	raw, _ := json.MarshalIndent(tl, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, ".pyorquesta", "tasks.json"), raw, 0o644)

	buf := &bytes.Buffer{}
	if err := Status(dir, buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"T001", "T002", "done", "pending", "first", "second"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q: %s", want, out)
		}
	}
}

func TestStatus_HandlesMissingTasksFile(t *testing.T) {
	dir := t.TempDir()
	buf := &bytes.Buffer{}
	if err := Status(dir, buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no tasks") {
		t.Errorf("expected 'no tasks' message, got: %s", buf.String())
	}
}
```

- [ ] **Step 2: Implement statuscmd.go**

```go
package commands

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lionelchamorro/pyorquesta/internal/tasks"
)

func Status(projectDir string, w io.Writer) error {
	p := filepath.Join(projectDir, ".pyorquesta", "tasks.json")
	tl, err := tasks.Load(p)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(w, "no tasks (run `pyorquesta plan plan.md` first)")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "%-6s  %-8s  %-3s  %-6s  %-20s  %s\n", "ID", "STATUS", "PRI", "ATT", "REASON", "TITLE")
	for _, t := range tl.Tasks {
		reason := ""
		if t.FailureReason != nil {
			reason = string(*t.FailureReason)
		}
		fmt.Fprintf(w, "%-6s  %-8s  %-3d  %-6d  %-20s  %s\n", t.ID, t.Status, t.Priority, t.Attempts, reason, t.Title)
	}
	return nil
}
```

- [ ] **Step 3: Run tests + commit**

```bash
go test ./internal/commands/...
git add internal/commands/statuscmd.go internal/commands/statuscmd_test.go
git commit -m "feat(status): print task table; handle missing tasks.json gracefully"
```

> `--watch` is a v0.1 follow-up; not required for the spine.

---

## Task 18: `internal/commands/resetcmd` — `pyorquesta reset`

**Files:**
- Create: `internal/commands/resetcmd.go`
- Create: `internal/commands/resetcmd_test.go`

- [ ] **Step 1: Write the failing test**

```go
package commands

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReset_RemovesPyorquestaDir(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, ".pyorquesta")
	_ = os.MkdirAll(filepath.Join(state, "results"), 0o755)
	_ = os.WriteFile(filepath.Join(state, "tasks.json"), []byte("{}"), 0o644)
	if err := Reset(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Errorf("expected .pyorquesta to be gone")
	}
}

func TestReset_NoOpIfMissing(t *testing.T) {
	if err := Reset(t.TempDir()); err != nil {
		t.Fatalf("expected no-op, got %v", err)
	}
}
```

- [ ] **Step 2: Implement resetcmd.go**

```go
package commands

import (
	"errors"
	"os"
	"path/filepath"
)

func Reset(projectDir string) error {
	p := filepath.Join(projectDir, ".pyorquesta")
	err := os.RemoveAll(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
```

- [ ] **Step 3: Run tests + commit**

```bash
go test ./internal/commands/...
git add internal/commands/resetcmd.go internal/commands/resetcmd_test.go
git commit -m "feat(reset): remove .pyorquesta state directory"
```

---

## Task 19: `cmd/pyorquesta/main.go` — CLI entry + subcommand dispatch

**Files:**
- Create: `cmd/pyorquesta/main.go`

- [ ] **Step 1: Write main.go**

Use `flag` package — no extra deps. Subcommand pattern by inspecting `os.Args[1]`.

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/lionelchamorro/pyorquesta/internal/commands"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	ctx := context.Background()

	switch cmd {
	case "init":
		dir := "."
		if len(args) > 0 {
			dir = args[0]
		}
		exit(commands.Init(dir))

	case "plan":
		fs := flag.NewFlagSet("plan", flag.ExitOnError)
		appendFlag := fs.Bool("append", false, "append to existing tasks.json")
		_ = fs.Parse(args)
		if fs.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "usage: pyorquesta plan <plan.md> [--append]")
			os.Exit(2)
		}
		// In v0 the parser caller is wired via the same liveDeps machinery as `run`;
		// for now, exit with a clear message that this requires the run wire-up.
		// Implementation: refactor liveDeps to expose RunParser and call it here.
		exit(commands.PlanWithLiveCaller(ctx, ".", fs.Arg(0), *appendFlag))

	case "run":
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		_ = fs.Parse(args)
		teamPath := "team.json"
		exit(commands.Run(ctx, commands.RunOptions{ProjectDir: ".", TeamPath: teamPath}))

	case "status":
		exit(commands.Status(".", os.Stdout))

	case "reset":
		exit(commands.Reset("."))

	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage: pyorquesta <command> [args]

Commands:
  init [dir]            scaffold .pyorquesta, team.json, prompts/
  plan <plan.md>        invoke parser, write tasks.json (--append to add)
  run                   run review/task/fix loops over existing tasks.json
  status                print tasks table
  reset                 remove .pyorquesta state`)
}

func exit(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
```

> **Implementation note:** add `commands.PlanWithLiveCaller(ctx, dir, planPath, append)` as a thin wrapper that constructs a `liveDeps` (same as `Run` does), then calls `commands.Plan(ctx, dir, planPath, append, liveCaller)` where `liveCaller` is a `liveDeps`-based implementation of `ParserCaller`. This avoids duplicating wiring code.

- [ ] **Step 2: Build and smoke-test**

```bash
go build -o pyorquesta ./cmd/pyorquesta
./pyorquesta
```

Expected: prints usage, exits 2.

```bash
./pyorquesta init /tmp/pyq-smoke
ls /tmp/pyq-smoke/.pyorquesta /tmp/pyq-smoke/team.json /tmp/pyq-smoke/prompts/
```

Expected: directory + files exist.

- [ ] **Step 3: Commit**

```bash
git add cmd/ internal/commands/
git commit -m "feat(cli): main entry point with subcommand dispatch"
```

---

## Task 20: End-to-end smoke + full test pass

**Files:** none new — verification only.

- [ ] **Step 1: Run the full Go test suite**

```bash
go test ./...
```

Expected: all tests PASS.

- [ ] **Step 2: Run go vet and basic linting**

```bash
go vet ./...
gofmt -d .
```

Expected: no output from either.

- [ ] **Step 3: Manual smoke test in a throwaway repo**

```bash
mkdir -p /tmp/pyq-smoke && cd /tmp/pyq-smoke
git init -q && git commit --allow-empty -m init
/Users/lionelchamorro/Projects/personal/pyorquesta/pyorquesta init
cat team.json | head -20
ls prompts/
```

Expected: files match the embedded defaults.

- [ ] **Step 4: Tag v0.1.0**

```bash
cd /Users/lionelchamorro/Projects/personal/pyorquesta
git tag v0.1.0
git log --oneline -10
```

Expected: clean linear history of feat commits, tagged at HEAD.

---

## Self-review notes

This plan covers the spine of pyorquesta as designed in `CONTEXT.md`. Out of scope for v0.1 (intentionally deferred):

- `pyorquesta status --watch` (live tailing) — file is small, add when needed
- Plan-input confirmation prompts (`run plan.md` over existing state) — add to runcmd
- Full e2e test that exercises the JSONL log rotation path
- `DBus`/desktop notifications when AFK run finishes
- `pyorquesta retry T005` to re-attempt a single failed task

Each Task above is a single subagent dispatch unit: independent, TDD, ends with one commit. Tasks 2–10 are mostly independent foundational packages and can be worked in parallel by separate subagents (they only share `internal/tasks` types from Task 3, which Tasks 11–13 import). Tasks 11–13 (loops) depend on 3, 4. Tasks 14–18 (commands) depend on 2, 3, 4, 5, 6, 7, 8, 9, 10. Task 19 (CLI) depends on all commands. Task 20 is verification only.

---

**Plan complete and saved to `tasks/todo.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session with checkpoints for review.

**Which approach?**
