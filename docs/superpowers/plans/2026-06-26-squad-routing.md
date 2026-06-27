# Squad Routing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Route each factory task to the role lane ("squad") it needs — `setup` (coder only), `full` (coder→tester→critic→verifier, unchanged), or `generic` (a new `generalist` role) — decided per task by the parser.

**Architecture:** Add a `Squad` field to `tasks.Task`. `RunTaskLoop` dispatches per task: `full` runs the existing `RunFix`+`FullSuite`+commit path untouched; `setup`/`generic` run a new lean `RunSingle(role)` then commit, skipping tester/critic/verifier AND the full test suite. The parser emits the squad; unset/unknown defaults to `full` (back-compat).

**Tech Stack:** Go 1.24, standard library + existing internal packages (`tasks`, `loops`, `commands`, `invoke`, `eventlog`).

## Global Constraints

- Go toolchain is not on PATH in non-login shells: prefix go commands with `export PATH=$PATH:/usr/local/go/bin`.
- TDD: write the failing test, see it fail, implement minimally, see it pass, commit.
- Squad recognized set is exactly `setup`, `full`, `generic`. Empty or unknown → `full` (fail-safe).
- `full` (and therefore every pre-existing task) MUST behave exactly as today.
- Commit messages end with the two trailers used in this repo:
  `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>` and
  `Claude-Session: https://claude.ai/code/session_01CQMe4SNSwLVjWccVhioZzu`.

---

### Task 1: `tasks.Squad` field, constants, and `SquadOrDefault`

**Files:**
- Modify: `internal/tasks/tasks.go` (add field + constants + method)
- Test: `internal/tasks/tasks_test.go`

**Interfaces:**
- Produces:
  - `tasks.Task.Squad string` (json `squad,omitempty`)
  - `const SquadSetup = "setup"`, `SquadFull = "full"`, `SquadGeneric = "generic"` (typed `string`)
  - `func (t *Task) SquadOrDefault() string` — returns `SquadFull` for empty or unrecognized; else the recognized value.

- [ ] **Step 1: Write the failing test**

Append to `internal/tasks/tasks_test.go`:

```go
func TestSquadOrDefault(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", SquadFull},
		{"full", SquadFull},
		{"setup", SquadSetup},
		{"generic", SquadGeneric},
		{"bogus", SquadFull}, // unknown is fail-safe
	}
	for _, c := range cases {
		task := Task{Squad: c.in}
		if got := task.SquadOrDefault(); got != c.want {
			t.Errorf("SquadOrDefault(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSquadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tasks.json")
	want := &TaskList{Tasks: []Task{{ID: "T1", Squad: SquadSetup}}}
	if err := Save(p, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tasks[0].Squad != SquadSetup {
		t.Errorf("squad did not round-trip: %q", got.Tasks[0].Squad)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/tasks/ -run 'TestSquad' -v`
Expected: FAIL — `SquadOrDefault` / `SquadSetup` undefined.

- [ ] **Step 3: Add the field and constants**

In `internal/tasks/tasks.go`, add to the `Task` struct (near the other fields, after `Priority`):

```go
	// Squad selects the role lane that runs this task: "setup" (coder only),
	// "full" (coder→tester→critic→verifier, the default), or "generic"
	// (generalist only). Empty or unknown is treated as "full".
	Squad string `json:"squad,omitempty"`
```

Add the constants (next to the `Status` consts):

```go
const (
	SquadSetup   = "setup"
	SquadFull    = "full"
	SquadGeneric = "generic"
)
```

- [ ] **Step 4: Add the method**

In `internal/tasks/tasks.go`:

```go
// SquadOrDefault returns the task's squad, defaulting to SquadFull when the
// field is empty or holds an unrecognized value (fail-safe: unknown work gets
// the full review lane).
func (t *Task) SquadOrDefault() string {
	switch t.Squad {
	case SquadSetup, SquadGeneric:
		return t.Squad
	default:
		return SquadFull
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/tasks/ -run 'TestSquad' -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tasks/tasks.go internal/tasks/tasks_test.go
git commit -m "feat(tasks): add Squad field + SquadOrDefault (default full)"
```

---

### Task 2: `RunSingle` interface + squad dispatch in `RunTaskLoop`

**Files:**
- Modify: `internal/loops/task.go` (TaskDeps interface, extract `commitTask` helper, add squad dispatch)
- Test: `internal/loops/task_test.go` (extend `stubTaskDeps`, add dispatch tests)

**Interfaces:**
- Consumes: `tasks.Task.SquadOrDefault()` (Task 1).
- Produces:
  - `loops.SingleOutcome{Status string; Summary string; FilesChanged []string}` with `Status` one of `"done"`/`"failed"`.
  - `TaskDeps.RunSingle(ctx context.Context, role string, rc invoke.RunContext) (SingleOutcome, error)`
  - `TaskDeps.HasRole(role string) bool`
  - Behavior: `setup` → `RunSingle("coder")`, no `FullSuite`, commit. `generic` → if `HasRole("generalist")` then `RunSingle("generalist")` else fall back to the `full` path with a warning; no `FullSuite`, commit. `full`/empty → existing `RunFix`+`FullSuite`+commit path, unchanged.

- [ ] **Step 1: Write the failing tests**

Add to `internal/loops/task_test.go`. First extend `stubTaskDeps` (add fields + methods):

```go
// add to stubTaskDeps struct:
//   runSingle    func(role string) (SingleOutcome, error)
//   hasRole      func(role string) bool
//   singleCalls  []string // roles passed to RunSingle
//   fullSuiteCalls int

func (s *stubTaskDeps) RunSingle(ctx context.Context, role string, rc invoke.RunContext) (SingleOutcome, error) {
	s.singleCalls = append(s.singleCalls, role)
	if s.runSingle != nil {
		return s.runSingle(role)
	}
	return SingleOutcome{Status: "done"}, nil
}
func (s *stubTaskDeps) HasRole(role string) bool {
	if s.hasRole != nil {
		return s.hasRole(role)
	}
	return true
}
```

Also change the existing `FullSuite` stub method to count calls:

```go
func (s *stubTaskDeps) FullSuite(ctx context.Context) error { s.fullSuiteCalls++; return s.fullSuite() }
```

Now the dispatch tests:

```go
func TestTaskLoop_SetupSquadRunsCoderOnlyNoFullSuite(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Squad: tasks.SquadSetup, Status: tasks.StatusPending, Priority: 1},
	}}
	d := &stubTaskDeps{
		fix:       func(id string) *FixResult { t.Fatalf("RunFix must not run for setup"); return nil },
		fullSuite: func() error { return nil },
		commit:    func(msg string) (string, error) { return "sha", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}
	if err := RunTaskLoop(context.Background(), tl, d); err != nil {
		t.Fatal(err)
	}
	if len(d.singleCalls) != 1 || d.singleCalls[0] != "coder" {
		t.Errorf("setup should call RunSingle(coder), got %v", d.singleCalls)
	}
	if d.fullSuiteCalls != 0 {
		t.Errorf("setup must skip FullSuite, got %d calls", d.fullSuiteCalls)
	}
	if len(d.commits) != 1 {
		t.Errorf("setup should commit once, got %d", len(d.commits))
	}
	if tl.Tasks[0].Status != tasks.StatusDone {
		t.Errorf("setup task should be Done, got %s", tl.Tasks[0].Status)
	}
}

func TestTaskLoop_GenericSquadRunsGeneralist(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Squad: tasks.SquadGeneric, Status: tasks.StatusPending, Priority: 1},
	}}
	d := &stubTaskDeps{
		fix:       func(id string) *FixResult { t.Fatalf("RunFix must not run for generic"); return nil },
		fullSuite: func() error { return nil },
		commit:    func(msg string) (string, error) { return "sha", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}
	if err := RunTaskLoop(context.Background(), tl, d); err != nil {
		t.Fatal(err)
	}
	if len(d.singleCalls) != 1 || d.singleCalls[0] != "generalist" {
		t.Errorf("generic should call RunSingle(generalist), got %v", d.singleCalls)
	}
	if d.fullSuiteCalls != 0 {
		t.Errorf("generic must skip FullSuite")
	}
	if tl.Tasks[0].Status != tasks.StatusDone {
		t.Errorf("generic task should be Done, got %s", tl.Tasks[0].Status)
	}
}

func TestTaskLoop_GenericFallsBackToFullWhenNoGeneralist(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Squad: tasks.SquadGeneric, Status: tasks.StatusPending, Priority: 1},
	}}
	d := &stubTaskDeps{
		fix:       func(id string) *FixResult { return &FixResult{Status: FixDone, Iterations: 1} },
		fullSuite: func() error { return nil },
		commit:    func(msg string) (string, error) { return "sha", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
		hasRole:   func(role string) bool { return role != "generalist" },
	}
	if err := RunTaskLoop(context.Background(), tl, d); err != nil {
		t.Fatal(err)
	}
	if len(d.singleCalls) != 0 {
		t.Errorf("no generalist → must not call RunSingle, got %v", d.singleCalls)
	}
	if d.fixCalled != 1 {
		t.Errorf("no generalist → generic falls back to RunFix, fixCalled=%d", d.fixCalled)
	}
	if tl.Tasks[0].Status != tasks.StatusDone {
		t.Errorf("fallback task should be Done, got %s", tl.Tasks[0].Status)
	}
}

func TestTaskLoop_SingleRoleFailureMarksFailed(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Squad: tasks.SquadSetup, Status: tasks.StatusPending, Priority: 1},
	}}
	d := &stubTaskDeps{
		runSingle: func(role string) (SingleOutcome, error) { return SingleOutcome{Status: "failed", Summary: "blocked"}, nil },
		fullSuite: func() error { return nil },
		commit:    func(msg string) (string, error) { return "sha", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}
	_ = RunTaskLoop(context.Background(), tl, d)
	if tl.Tasks[0].Status != tasks.StatusFailed {
		t.Errorf("failed RunSingle → task Failed, got %s", tl.Tasks[0].Status)
	}
	if len(d.commits) != 0 {
		t.Errorf("failed single-role task must not commit")
	}
	if d.rollbacks != 1 {
		t.Errorf("failed single-role task should roll back, got %d", d.rollbacks)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/loops/ -run 'TestTaskLoop_(Setup|Generic|SingleRole)' -v`
Expected: FAIL — `RunSingle`/`HasRole`/`SingleOutcome` undefined.

- [ ] **Step 3: Add the interface members and SingleOutcome type**

In `internal/loops/task.go`, add to the `TaskDeps` interface:

```go
	// RunSingle runs exactly one role once (with bounded retry on transient
	// agent failure), with no tester/critic/verifier loop. Used by the setup
	// and generic squads.
	RunSingle(ctx context.Context, role string, rc invoke.RunContext) (SingleOutcome, error)
	// HasRole reports whether the named role is configured in team.json. Used to
	// fall a generic task back to the full lane when no generalist role exists.
	HasRole(role string) bool
```

Add the type (near `FixResult`):

```go
// SingleOutcome is the result of a single-role squad (setup/generic).
type SingleOutcome struct {
	Status       string // "done" | "failed"
	Summary      string
	FilesChanged []string
}
```

- [ ] **Step 4: Extract a shared `commitTask` helper**

In `internal/loops/task.go`, extract the existing commit/verify-state block (currently the `msg := fmt.Sprintf(...)` … `switch { case commitErr == nil … }` … `t.Status = tasks.StatusDone` region) into a helper so both lanes reuse it:

```go
// commitTask commits the task's work and sets its VerifyState/Status. It treats
// a no-git-repo (ErrCommitSkipped) and an empty no-op commit (ErrNothingToCommit)
// as success, any other commit error as a hard failure (rolled back by caller).
// Returns true when the task is done, false when it failed.
func commitTask(ctx context.Context, d TaskDeps, t *tasks.Task) bool {
	msg := fmt.Sprintf("feat(%s): %s", t.ID, t.Title)
	_, commitErr := d.Commit(ctx, msg)
	switch {
	case commitErr == nil:
		t.VerifyState = tasks.VerifyCommitOK
	case errors.Is(commitErr, ErrCommitSkipped):
		t.VerifyState = tasks.VerifyCommitSkipped
	case errors.Is(commitErr, ErrNothingToCommit):
		t.VerifyState = tasks.VerifyCommitEmpty
	default:
		t.Status = tasks.StatusFailed
		r := tasks.ReasonCommitRejected
		t.FailureReason = &r
		t.VerifyState = tasks.VerifyCommitRejected
		t.LastFeedback = strPtr(commitErr.Error())
		return false
	}
	t.Status = tasks.StatusDone
	return true
}
```

Then replace the inline commit block in the existing `full` path with:

```go
	if !commitTask(ctx, d, t) {
		_ = d.Rollback(ctx)
		_ = d.SaveTasks(ctx, tl)
		continue
	}
	_ = d.SaveTasks(ctx, tl)
```

(Keep the `FullSuite` call that precedes it for the full path.)

- [ ] **Step 5: Add the squad dispatch**

In `internal/loops/task.go`, in `RunTaskLoopWithContext`, immediately after the task is marked in-progress and saved (after `t.Attempts++; _ = d.SaveTasks(...)`), insert a dispatch that handles the single-role lanes and `continue`s, letting `full` fall through to the existing `RunFix` code:

```go
		taskRC := baseRC
		taskRC.TaskID = taskID

		squad := t.SquadOrDefault()
		if squad == tasks.SquadGeneric && !d.HasRole("generalist") {
			squad = tasks.SquadFull // no generalist configured: use the full lane
		}
		if squad == tasks.SquadSetup || squad == tasks.SquadGeneric {
			role := "coder"
			if squad == tasks.SquadGeneric {
				role = "generalist"
			}
			out, err := d.RunSingle(ctx, role, taskRC)
			if err != nil {
				return err
			}
			if out.Status != "done" {
				t.Status = tasks.StatusFailed
				r := tasks.ReasonAgentRepeatedFail
				t.FailureReason = &r
				t.LastFeedback = strPtr(out.Summary)
				_ = d.Rollback(ctx)
				_ = d.SaveTasks(ctx, tl)
				continue
			}
			if !commitTask(ctx, d, t) {
				_ = d.Rollback(ctx)
				_ = d.SaveTasks(ctx, tl)
				continue
			}
			_ = d.SaveTasks(ctx, tl)
			continue
		}

		// full lane (default): existing RunFix path below, unchanged.
		fx, err := d.RunFix(ctx, taskID, taskRC)
```

(Remove the now-duplicated `taskRC := baseRC; taskRC.TaskID = taskID; fx, err := d.RunFix(...)` lines that previously started the body — they are replaced by the block above.)

- [ ] **Step 6: Run tests to verify they pass**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/loops/ -count=1`
Expected: PASS (new dispatch tests + all existing task-loop tests, which default to full).

- [ ] **Step 7: Commit**

```bash
git add internal/loops/task.go internal/loops/task_test.go
git commit -m "feat(loops): squad dispatch in RunTaskLoop (setup/generic via RunSingle)"
```

---

### Task 3: live `RunSingle` + `HasRole` implementations

**Files:**
- Modify: `internal/commands/runcmd.go` (add `RunSingle` and `HasRole` methods to `liveDeps`)
- Test: `internal/commands/runcmd_test.go`

**Interfaces:**
- Consumes: `loops.SingleOutcome`, `invoke.RunContext`, the existing `d.inv` (`*invoke.RoleInvoker`) and its `Specs map[string]config.RoleSpec`.
- Produces: `liveDeps` satisfies the extended `loops.TaskDeps`.

Read `internal/commands/runcmd.go:340` (`RunFix`) first to see how it invokes a role through `d.inv`; `RunSingle` invokes a single role the same way but returns after one successful result (with a small retry on missing result).

- [ ] **Step 1: Write the failing test**

Append to `internal/commands/runcmd_test.go`:

```go
func TestLiveDeps_HasRole(t *testing.T) {
	d := &liveDeps{inv: &invoke.RoleInvoker{Specs: map[string]config.RoleSpec{
		"coder": {}, "generalist": {},
	}}}
	if !d.HasRole("generalist") {
		t.Error("HasRole(generalist) should be true")
	}
	if d.HasRole("nonexistent") {
		t.Error("HasRole(nonexistent) should be false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/commands/ -run TestLiveDeps_HasRole -v`
Expected: FAIL — `HasRole` undefined.

- [ ] **Step 3: Implement `HasRole` and `RunSingle`**

In `internal/commands/runcmd.go`:

```go
func (d *liveDeps) HasRole(role string) bool {
	_, ok := d.inv.Specs[role]
	return ok
}

// RunSingle invokes one role once (with one retry if it writes no result),
// records the agent run, and maps a written result to done, an absent result to
// failed. No tester/critic/verifier loop runs.
func (d *liveDeps) RunSingle(ctx context.Context, role string, rc invoke.RunContext) (loops.SingleOutcome, error) {
	spec, ok := d.inv.Specs[role]
	if !ok {
		return loops.SingleOutcome{Status: "failed", Summary: "role not configured: " + role}, nil
	}
	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		rc.Attempt = attempt
		res, err := d.inv.RunRole(ctx, role, spec, rc) // see note below
		if err != nil {
			lastErr = err
			continue
		}
		if res.ResultExists {
			return loops.SingleOutcome{Status: "done", Summary: res.FinalText}, nil
		}
		lastErr = fmt.Errorf("%s wrote no result", role)
	}
	return loops.SingleOutcome{Status: "failed", Summary: fmt.Sprintf("%v", lastErr)}, nil
}
```

**Note:** the exact invoke call/return shape must match how `RunFix`/`Decompose` drive `d.inv` (see `runcmd.go:340` and `runcmd.go:648`). If `RoleInvoker` exposes no single-role public method, add a thin one in `internal/invoke/role.go` that wraps the existing internal `run(...)` (it already takes `roleName, role, agentOverride, prompt, relResultPath, absResultPath, rc`) and returns whether the result file was written. Mirror how `Decompose` resolves `prompt`/`result_path` from the `config.RoleSpec`. Keep this single-role helper minimal and reuse `inv.Specs[role].PromptPath` / `.ResultPath`.

- [ ] **Step 4: Run test + build to verify**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/commands/ -run TestLiveDeps_HasRole -count=1 && go build ./...`
Expected: PASS + build OK.

- [ ] **Step 5: Commit**

```bash
git add internal/commands/runcmd.go internal/commands/runcmd_test.go internal/invoke/role.go
git commit -m "feat(commands): live RunSingle + HasRole for squad lanes"
```

---

### Task 4: `generalist` role — prompt asset + default team.json

**Files:**
- Create: `internal/commands/assets/prompts/generalist.md`
- Modify: `internal/commands/assets/team.json` (add `generalist` role)
- Modify: `internal/commands/initcmd.go` only if the embed list is explicit per-file (the existing `//go:embed assets/prompts/*.md` glob already covers the new prompt — verify)
- Test: `internal/commands/initcmd_test.go` (assert scaffold includes generalist)

**Interfaces:**
- Produces: an `orq-lite init` scaffold whose `team.json` has a `generalist` role pointing at `prompts/generalist.md`.

- [ ] **Step 1: Write the failing test**

Find the existing init scaffold test in `internal/commands/initcmd_test.go` (it asserts files/roles exist). Add:

```go
func TestInit_ScaffoldsGeneralistRole(t *testing.T) {
	dir := t.TempDir()
	if err := InitWithOptions(dir, InitOptions{Lang: "python"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "team.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"generalist"`) {
		t.Errorf("scaffolded team.json missing generalist role:\n%s", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, "prompts", "generalist.md")); err != nil {
		t.Errorf("prompts/generalist.md not scaffolded: %v", err)
	}
}
```

(Adjust the `InitWithOptions` call to match the real signature used by neighboring tests.)

- [ ] **Step 2: Run test to verify it fails**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/commands/ -run TestInit_ScaffoldsGeneralistRole -v`
Expected: FAIL — no generalist in team.json.

- [ ] **Step 3: Create the generalist prompt**

Create `internal/commands/assets/prompts/generalist.md`. Model structure/placeholders on `assets/prompts/coder.md` (same `{{...}}` variables and result-file contract), but framed as a general-purpose engineer:

```markdown
You are a generalist engineer on an automated factory. You handle tasks that are
not "write code with behavior to test": project chores, file reconciliation,
documentation, configuration tidy-ups, and small non-behavioral edits.

## Task
{{TASK_TITLE}}
{{TASK_DESCRIPTION}}

## Conventions
{{CONVENTIONS}}

## Instructions
- Do exactly what the task asks — nothing more. Do not add code, tests, or files
  beyond the task's acceptance criteria.
- Make the change directly in the working tree.
- When done, write your result JSON to {{RESULT_PATH}} with a one-paragraph
  summary of what you changed under the key your schema requires (mirror
  coder.md's result contract).

There is no tester or critic after you: get it right and report precisely.
```

(Copy the exact variable names and result-JSON shape from `coder.md` so the invoker and result parser work unchanged.)

- [ ] **Step 4: Add the role to the default team.json**

In `internal/commands/assets/team.json`, add a `generalist` role mirroring the `coder` role's shape but pointing at the generalist prompt and its own result path:

```json
    "generalist": {
      "agents": ["claude_sonnet"],
      "prompt": "prompts/generalist.md",
      "result_path": ".orquestalite/results/generalist.json",
      "timeout_seconds": 1800
    }
```

(Use whatever the default coder agent name is in this file. If the default team uses a non-claude primary coder, still default `generalist` to a claude agent present in the file.)

- [ ] **Step 5: Run test + verify embed**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/commands/ -run TestInit_ScaffoldsGeneralistRole -count=1`
Expected: PASS. If it fails on a missing embedded file, confirm `initcmd.go`'s `//go:embed` covers `assets/prompts/*.md` and `assets/team.json`.

- [ ] **Step 6: Commit**

```bash
git add internal/commands/assets/prompts/generalist.md internal/commands/assets/team.json internal/commands/initcmd_test.go
git commit -m "feat(commands): scaffold generalist role + prompt"
```

---

### Task 5: parser emits `squad`

**Files:**
- Modify: `internal/commands/assets/prompts/parser.md` and `assets/prompts/parser-decompose.md` (classification rules)
- Modify: `internal/commands/assets/schemas/parser.json` (allow `squad` per task)
- Modify: the parser result→`tasks.Task` mapping (find where parsed tasks are built; grep `parser` result handling in `internal/commands` / `internal/results`) so `squad` carries through to `tasks.Task.Squad`.
- Test: the parser-mapping test (extend an existing parser/results test to assert squad maps through)

**Interfaces:**
- Consumes: `tasks.Task.Squad` (Task 1).
- Produces: parsed tasks carry a `Squad` chosen by the parser; unset → `full` via `SquadOrDefault`.

- [ ] **Step 1: Write the failing test**

Locate the test that exercises parsing a parser-result JSON into `tasks.Task` (grep `Squad`-adjacent fields like `Priority` in `internal/results` / `internal/commands` tests). Add a case asserting a result object with `"squad":"setup"` yields `Task.Squad == tasks.SquadSetup`, and an object with no squad yields `""` (which `SquadOrDefault()` maps to full). If the mapping is a pure function, test it directly; otherwise test via the smallest unit that converts a parsed result to tasks.

```go
// Example shape — adapt to the actual converter under test:
func TestParserResult_MapsSquad(t *testing.T) {
	in := `{"tasks":[{"id":"T001","title":"scaffold","description":"d","priority":1,"squad":"setup"},
	                 {"id":"T002","title":"feature","description":"d","priority":2}]}`
	got := parseTasksJSON([]byte(in)) // replace with the real converter
	if got[0].Squad != tasks.SquadSetup {
		t.Errorf("T001 squad = %q, want setup", got[0].Squad)
	}
	if got[1].SquadOrDefault() != tasks.SquadFull {
		t.Errorf("T002 should default to full")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/... -run 'Squad' -v` (scoped to the package under test once located).
Expected: FAIL — squad not mapped.

- [ ] **Step 3: Carry `squad` through the mapping**

If the parser result struct is a distinct type that is converted to `tasks.Task`, add a `Squad string json:"squad"` field to it and copy it into `tasks.Task.Squad` in the converter. If the parser writes `tasks.Task` JSON directly, the field already round-trips (Task 1) and only the schema/prompt need updating.

- [ ] **Step 4: Update the schema and prompt**

In `internal/commands/assets/schemas/parser.json`, add `squad` to each task's properties:

```json
"squad": { "type": "string", "enum": ["setup", "full", "generic"] }
```

(Do not add it to `required` — it stays optional.)

In `assets/prompts/parser.md` (and `parser-decompose.md`), add a "Squad" section:

```markdown
## Squad (role lane) per task

Set "squad" on every task:
- "setup" — creating project structure, dependency manifests, lock files, config,
  or ignore files. No runtime behavior to assert. Runs a coder only (no tests).
- "generic" — non-code reconciliation, documentation, chores, file moves.
- "full" — anything that adds or changes code behavior. This is the default; use
  it whenever you are unsure.
```

- [ ] **Step 5: Run test to verify it passes**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/... -run 'Squad' -count=1` (scoped package).
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/commands/assets/schemas/parser.json internal/commands/assets/prompts/parser.md internal/commands/assets/prompts/parser-decompose.md <converter file + test>
git commit -m "feat(parser): emit per-task squad (setup/full/generic)"
```

---

### Task 6: reviewer-created tasks default to `generic`

**Files:**
- Modify: `internal/loops/review.go` (the `tl.Append(newOnes, cycle)` site at ~:68) — set `Squad = SquadGeneric` on reviewer-created tasks that have no squad.
- Test: `internal/loops/review_test.go`

**Interfaces:**
- Consumes: `tasks.SquadGeneric`.
- Produces: reviewer-appended tasks carry `Squad == generic` unless the reviewer explicitly set another squad.

- [ ] **Step 1: Write the failing test**

In `internal/loops/review_test.go`, add a test that runs the reviewer path which appends tasks and asserts the appended task's `Squad == tasks.SquadGeneric`. Model it on the existing reviewer test that asserts new tasks are appended; after the append, check the squad. (If review.go converts a reviewer result into `[]tasks.Task` named `newOnes`, the test drives that converter / the review loop and inspects the resulting list.)

```go
// Adapt to the existing review_test harness:
func TestReview_AppendedTasksDefaultGeneric(t *testing.T) {
	// ... arrange a reviewer result that proposes one new task with no squad ...
	// ... run the review step that appends it ...
	// assert the appended task's Squad == tasks.SquadGeneric
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/loops/ -run TestReview_AppendedTasksDefaultGeneric -v`
Expected: FAIL — squad empty (defaults to full).

- [ ] **Step 3: Default the squad before append**

In `internal/loops/review.go`, right before `tl.Append(newOnes, cycle)`:

```go
	for i := range newOnes {
		if newOnes[i].Squad == "" {
			newOnes[i].Squad = tasks.SquadGeneric
		}
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/loops/ -run TestReview_AppendedTasksDefaultGeneric -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/loops/review.go internal/loops/review_test.go
git commit -m "feat(loops): reviewer-created tasks default to generic squad"
```

---

### Task 7: observability — `task_routed` event

**Files:**
- Modify: `internal/loops/task.go` (emit a routed event via a `TaskDeps` method) OR `internal/commands/runcmd.go` (log directly). Prefer adding `TaskDeps.RouteEvent(taskID, squad string)` so the loop stays decoupled and testable.
- Test: `internal/commands/runcmd_test.go` (assert the event lands in run.log) and/or `internal/loops/task_test.go` (assert the stub records the route).

**Interfaces:**
- Produces: a `task_routed{task_id, squad}` JSONL line when each task starts.

- [ ] **Step 1: Write the failing test**

In `internal/loops/task_test.go`, add a `routeEvents []string` slice + `RouteEvent` method to `stubTaskDeps` that records `taskID+":"+squad`, then assert in `TestTaskLoop_SetupSquadRunsCoderOnlyNoFullSuite` (or a new test) that `d.routeEvents` contains `"T001:setup"`.

```go
func (s *stubTaskDeps) RouteEvent(taskID, squad string) {
	s.routeEvents = append(s.routeEvents, taskID+":"+squad)
}
```

```go
// add to a dispatch test:
if len(d.routeEvents) != 1 || d.routeEvents[0] != "T001:setup" {
	t.Errorf("expected route event T001:setup, got %v", d.routeEvents)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/loops/ -run 'TestTaskLoop_Setup' -v`
Expected: FAIL — `RouteEvent` undefined / not called.

- [ ] **Step 3: Add the method and emit it**

Add `RouteEvent(taskID, squad string)` to the `TaskDeps` interface. Call it in `RunTaskLoopWithContext` right after computing `squad` (after the no-generalist fallback adjustment), for every task (full included):

```go
	d.RouteEvent(taskID, squad)
```

Live impl in `internal/commands/runcmd.go`:

```go
func (d *liveDeps) RouteEvent(taskID, squad string) {
	d.log.Log(eventlog.Event{Type: "task_routed", Fields: map[string]any{
		"task_id": taskID, "squad": squad,
	}})
}
```

Update any other `TaskDeps` implementers (e.g. review-loop stubs) to satisfy the interface.

- [ ] **Step 4: Run tests to verify they pass**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/loops/ ./internal/commands/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/loops/task.go internal/loops/task_test.go internal/commands/runcmd.go
git commit -m "feat(observability): emit task_routed{task_id,squad} per task"
```

---

### Task 8: full verification

**Files:** none (verification only).

- [ ] **Step 1: Build, vet, full suite**

Run:
```bash
export PATH=$PATH:/usr/local/go/bin
go build ./... && go vet ./... && go test ./... -count=1
```
Expected: build OK, vet OK, all packages PASS. (`internal/runner`'s `TestRunAgent_DetectsInteractiveAuthPrompt` is a known pre-existing flake under parallel `./...`; re-run `go test ./internal/runner/ -count=1` in isolation to confirm green.)

- [ ] **Step 2: Sanity-check the dispatch end to end (no API spend)**

Confirm via the unit tests that: setup→coder-only+no-FullSuite+commit; generic→generalist (or full fallback); full unchanged; reviewer tasks default generic; parser squad maps through. No live factory run is required for this plan; the live behavior is covered by the live `RunSingle`/`HasRole`/`RouteEvent` impls plus the loop dispatch tests.

- [ ] **Step 3: Update the spec status**

Edit `docs/superpowers/specs/2026-06-26-squad-routing-design.md` header `Status:` to `implemented`, commit:

```bash
git add docs/superpowers/specs/2026-06-26-squad-routing-design.md
git commit -m "docs(spec): mark squad routing implemented"
```

---

## Self-Review

**Spec coverage:**
- 3 squads → Task 1 (constants) + Task 2 (dispatch). ✓
- parser decides per task → Task 5. ✓
- setup=coder / generic=generalist, skip tester/critic/verifier + FullSuite → Task 2. ✓
- defaults empty→full / reviewer→generic → Task 1 (`SquadOrDefault`) + Task 6. ✓
- Approach A dispatcher in RunTaskLoop → Task 2. ✓
- generalist role → Task 3 (live) + Task 4 (asset/scaffold). ✓
- gate integration unchanged → covered (done/failed flow through existing Status). ✓
- observability task_routed → Task 7. ✓
- back-compat (no squad → full; generic w/o generalist → full) → Task 1 + Task 2 fallback test. ✓

**Placeholder scan:** Tasks 3, 5, 6 contain "adapt to the actual converter/harness" notes because the exact invoke signature and parser/review converters must be read at implementation time; each gives the concrete file, the grep to run, and the exact field/logic to add. These are guided lookups, not unfilled placeholders.

**Type consistency:** `SingleOutcome{Status,Summary,FilesChanged}`, `RunSingle(ctx,role,rc)`, `HasRole(role)`, `RouteEvent(taskID,squad)`, `SquadOrDefault()`, and the `SquadSetup/Full/Generic` constants are used consistently across Tasks 1–7.
