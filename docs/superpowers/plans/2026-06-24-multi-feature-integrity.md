# Multi-feature Integrity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `orq-lite factory` carry feature code forward (merge each gate-passing feature into base before the next branches) and stop provider sessions from bleeding across features.

**Architecture:** Two coupled fixes in the factory engine. (1) After a feature's run + bounded repair loop, the engine merges it into the base branch when it passes a strict gate, or stops the queue (preserving the branch) when it doesn't. (2) Provider session keys are namespaced by feature ID so identical per-feature task IDs (F001/T001 vs F002/T001) no longer collide.

**Tech Stack:** Go; git via `os/exec` wrappers in `internal/gitx`; JSONL eventlog; no new dependencies.

## Global Constraints

- No new third-party dependencies; standard library + existing `internal/*` only.
- All git operations go through `internal/gitx` (never shell out elsewhere).
- The base branch name is whatever `gitx.CurrentBranch` returns — never hardcode `main`/`master`.
- `.orquestalite/` is gitignored; it never affects tree cleanliness or merges.
- Backward compatibility: a non-factory `run` (no feature) uses `SessionNamespace == ""` and must behave exactly as today.
- Each task ends green: `go build ./...`, `go vet ./...`, and the touched package's tests pass.
- Commit messages end with the repo's required trailers (see existing history).

---

### Task 1: `gitx.MergeFastForward`

**Files:**
- Modify: `internal/gitx/gitx.go` (append new function)
- Test: `internal/gitx/gitx_test.go` (append tests; reuse existing `initRepo` + `gitOrSkip` helpers)

**Interfaces:**
- Produces: `func MergeFastForward(dir, base, branch string) (method string, err error)` — `method` is `"ff"` or `"no-ff"`; on conflict runs `git merge --abort` and returns an error with base unchanged and a clean tree.

- [ ] **Step 1: Write the failing tests**

Append to `internal/gitx/gitx_test.go`:

```go
func TestMergeFastForward_FastForwards(t *testing.T) {
	gitOrSkip(t)
	dir := initRepo(t)
	base, err := CurrentBranch(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckoutNewBranch(dir, "feat-x", base); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitAll(dir, "feat: f"); err != nil {
		t.Fatal(err)
	}
	method, err := MergeFastForward(dir, base, "feat-x")
	if err != nil {
		t.Fatal(err)
	}
	if method != "ff" {
		t.Errorf("method = %q, want ff", method)
	}
	if cur, _ := CurrentBranch(dir); cur != base {
		t.Errorf("current branch = %q, want %q", cur, base)
	}
	if _, err := os.Stat(filepath.Join(dir, "f.txt")); err != nil {
		t.Errorf("merged file missing on base: %v", err)
	}
}

func TestMergeFastForward_NonFFCreatesMergeCommit(t *testing.T) {
	gitOrSkip(t)
	dir := initRepo(t)
	base, _ := CurrentBranch(dir)
	if err := CheckoutNewBranch(dir, "feat-y", base); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "y.txt"), []byte("y"), 0o644)
	if _, err := CommitAll(dir, "feat: y"); err != nil {
		t.Fatal(err)
	}
	if err := Checkout(dir, base); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644)
	if _, err := CommitAll(dir, "chore: b"); err != nil {
		t.Fatal(err)
	}
	method, err := MergeFastForward(dir, base, "feat-y")
	if err != nil {
		t.Fatal(err)
	}
	if method != "no-ff" {
		t.Errorf("method = %q, want no-ff", method)
	}
	for _, f := range []string{"y.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s after merge: %v", f, err)
		}
	}
}

func TestMergeFastForward_ConflictAborts(t *testing.T) {
	gitOrSkip(t)
	dir := initRepo(t)
	base, _ := CurrentBranch(dir)
	if err := CheckoutNewBranch(dir, "feat-z", base); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "c.txt"), []byte("feat\n"), 0o644)
	if _, err := CommitAll(dir, "feat: c"); err != nil {
		t.Fatal(err)
	}
	if err := Checkout(dir, base); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "c.txt"), []byte("base\n"), 0o644)
	if _, err := CommitAll(dir, "chore: c"); err != nil {
		t.Fatal(err)
	}
	if _, err := MergeFastForward(dir, base, "feat-z"); err == nil {
		t.Fatal("expected a conflict error")
	}
	if clean, _ := IsCleanTree(dir); !clean {
		t.Errorf("tree must be clean after merge --abort")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/gitx/ -run TestMergeFastForward -v`
Expected: FAIL — `undefined: MergeFastForward`.

- [ ] **Step 3: Implement `MergeFastForward`**

Append to `internal/gitx/gitx.go`:

```go
// MergeFastForward checks out base and merges branch into it. It prefers a
// fast-forward; when base has diverged it falls back to a --no-ff merge commit.
// Returns the method used ("ff" or "no-ff"). On conflict it runs `git merge
// --abort` and returns an error, leaving base unchanged with a clean tree.
func MergeFastForward(dir, base, branch string) (string, error) {
	if err := Checkout(dir, base); err != nil {
		return "", err
	}
	if _, err := run(dir, "merge", "--ff-only", branch); err == nil {
		return "ff", nil
	}
	if _, err := run(dir, "merge", "--no-ff", "--no-edit", branch); err != nil {
		_, _ = run(dir, "merge", "--abort")
		return "", fmt.Errorf("merge %s into %s: %w", branch, base, err)
	}
	return "no-ff", nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/gitx/ -run TestMergeFastForward -v`
Expected: PASS (all three).

- [ ] **Step 5: Commit**

```bash
git add internal/gitx/gitx.go internal/gitx/gitx_test.go
git commit -m "feat(gitx): MergeFastForward merges a feature branch into base (ff, no-ff, abort-on-conflict)"
```

---

### Task 2: Config knob `max_feature_retries`

**Files:**
- Modify: `internal/config/config.go` (add `Limits.MaxFeatureRetries` field + `FeatureRetries()` accessor)
- Modify: `internal/factory/engine.go` (add `Config.MaxFeatureRetries` field)
- Test: `internal/config/config_test.go` (append accessor test)

**Interfaces:**
- Produces: `func (l Limits) FeatureRetries() int` (default `1`); `factory.Config.MaxFeatureRetries int`.

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`:

```go
func TestLimitsFeatureRetries(t *testing.T) {
	if got := (Limits{}).FeatureRetries(); got != 1 {
		t.Errorf("default FeatureRetries = %d, want 1", got)
	}
	if got := (Limits{MaxFeatureRetries: 3}).FeatureRetries(); got != 3 {
		t.Errorf("explicit FeatureRetries = %d, want 3", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/config/ -run TestLimitsFeatureRetries -v`
Expected: FAIL — `FeatureRetries` undefined.

- [ ] **Step 3: Add the field and accessor**

In `internal/config/config.go`, add to the `Limits` struct (next to `MemoryCompactChars`):

```go
	// MaxFeatureRetries caps how many extra times the factory re-runs a feature
	// that did not pass the merge gate before giving up and stopping the queue.
	// The no-progress guard can stop earlier. 0 = use the default (1).
	MaxFeatureRetries int `json:"max_feature_retries,omitempty"`
```

And add the accessor next to `VisualRounds`:

```go
// FeatureRetries returns the extra feature-level retry budget on a merge-gate
// failure, defaulting to 1 when unset.
func (l Limits) FeatureRetries() int {
	if l.MaxFeatureRetries <= 0 {
		return 1
	}
	return l.MaxFeatureRetries
}
```

In `internal/factory/engine.go`, add to the `Config` struct (after `Replan`):

```go
	// MaxFeatureRetries is the number of EXTRA feature-level runs attempted when
	// a feature fails the merge gate (the no-progress guard may stop sooner).
	// Populated from limits.max_feature_retries; 0 means stop on first failure.
	MaxFeatureRetries int
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/config/ ./internal/factory/ -run 'TestLimitsFeatureRetries|TestParseFeatures' -v`
Expected: PASS. Also confirm the build: `go build ./...`.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go internal/factory/engine.go
git commit -m "feat(config): max_feature_retries knob + factory.Config.MaxFeatureRetries"
```

---

### Task 3: `Summary.FailedTaskIDs`

**Files:**
- Modify: `internal/factory/engine.go` (add field to `Summary`)
- Modify: `internal/commands/factorycmd.go` (`summarizeTasks` collects failed IDs)
- Test: `internal/commands/factorycmd_test.go` (append test)

**Interfaces:**
- Produces: `factory.Summary.FailedTaskIDs []string` — IDs of tasks in `failed`/`needs_human` after a feature run, in task order.

- [ ] **Step 1: Write the failing test**

Append to `internal/commands/factorycmd_test.go`:

```go
func TestSummarizeTasks_CollectsFailedIDs(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".orquestalite"), 0o755); err != nil {
		t.Fatal(err)
	}
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Status: tasks.StatusDone},
		{ID: "T002", Status: tasks.StatusFailed},
		{ID: "T003", Status: tasks.StatusNeedsHuman},
		{ID: "T004", Status: tasks.StatusPending},
	}}
	if err := tasks.Save(filepath.Join(dir, ".orquestalite", "tasks.json"), tl); err != nil {
		t.Fatal(err)
	}
	d := &liveFactoryDeps{dir: dir}
	sum := d.summarizeTasks()
	if sum.TasksDone != 1 || sum.TasksFailed != 2 || sum.TasksOther != 1 {
		t.Errorf("counts = %+v", sum)
	}
	if fmt.Sprint(sum.FailedTaskIDs) != "[T002 T003]" {
		t.Errorf("FailedTaskIDs = %v, want [T002 T003]", sum.FailedTaskIDs)
	}
}
```

If `factorycmd_test.go` does not already import `fmt`, `os`, `path/filepath`, and `internal/tasks`, add them.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/commands/ -run TestSummarizeTasks_CollectsFailedIDs -v`
Expected: FAIL — `sum.FailedTaskIDs` undefined.

- [ ] **Step 3: Implement**

In `internal/factory/engine.go`, add to `Summary`:

```go
	// FailedTaskIDs lists the tasks that ended in failed/needs_human, used by the
	// engine's no-progress guard to decide whether a repair retry made headway.
	FailedTaskIDs []string
```

In `internal/commands/factorycmd.go`, update `summarizeTasks` (the `StatusFailed`/`StatusNeedsHuman` case):

```go
		case tasks.StatusFailed, tasks.StatusNeedsHuman:
			sum.TasksFailed++
			sum.FailedTaskIDs = append(sum.FailedTaskIDs, t.ID)
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/commands/ -run TestSummarizeTasks_CollectsFailedIDs -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/factory/engine.go internal/commands/factorycmd.go internal/commands/factorycmd_test.go
git commit -m "feat(factory): Summary.FailedTaskIDs for the engine no-progress guard"
```

---

### Task 4: Per-feature session namespace (#9-bug)

**Files:**
- Modify: `internal/invoke/role.go` (add `SessionNamespace` field; `sessionTaskKey` helper; use it at the resume/save/delete sites)
- Modify: `internal/commands/runcmd.go` (`RunOptions.FeatureID`, `liveDepsOptions.FeatureID`, pass through, set `inv.SessionNamespace`)
- Modify: `internal/commands/factorycmd.go` (`RunFeature` passes `FeatureID: f.ID`)
- Test: `internal/invoke/role_test.go` (append two tests)

**Interfaces:**
- Consumes: `sessions.Store` (already wired as `inv.Sessions`).
- Produces: `RoleInvoker.SessionNamespace string`; `func (inv *RoleInvoker) sessionTaskKey(taskID string) string`; `RunOptions.FeatureID string`; `liveDepsOptions.FeatureID string`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/invoke/role_test.go` (add `"github.com/lionelchamorro/orquestalite/internal/sessions"` to imports if absent):

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/invoke/ -run 'TestSessionTaskKey|TestSessionNamespace' -v`
Expected: FAIL — `SessionNamespace` / `sessionTaskKey` undefined.

- [ ] **Step 3: Implement the namespace in `role.go`**

In `internal/invoke/role.go`, add a field to the `RoleInvoker` struct (next to `ResumeRoles`):

```go
	// SessionNamespace scopes session keys (e.g. the factory feature ID "F002")
	// so identical per-feature task IDs do not resume each other's sessions.
	// Empty (non-factory run) leaves keys as the bare task ID.
	SessionNamespace string
```

Add the helper (near `resumeSessionID`):

```go
// sessionTaskKey scopes a task ID by the current SessionNamespace so the session
// store never collides identical task IDs across features. Empty namespace
// returns the task ID unchanged (non-factory runs are unaffected).
func (inv *RoleInvoker) sessionTaskKey(taskID string) string {
	if inv.SessionNamespace == "" {
		return taskID
	}
	return inv.SessionNamespace + "/" + taskID
}
```

Update `resumeSessionID` to use it:

```go
func (inv *RoleInvoker) resumeSessionID(role, agent, taskID string) string {
	if inv.Sessions == nil || taskID == "" || !inv.ResumeRoles[role] {
		return ""
	}
	return inv.Sessions.Get(inv.sessionTaskKey(taskID), role, agent)
}
```

Update the save/delete block (currently around lines 256-262) to compose the key:

```go
		if inv.Sessions != nil && inv.ResumeRoles[roleName] && rc.TaskID != "" {
			key := inv.sessionTaskKey(rc.TaskID)
			switch {
			case !shouldFallback:
				_ = inv.Sessions.Set(key, roleName, agentName, r.SessionID)
			case spec.ResumeSessionID != "" && !r.RateLimited:
				_ = inv.Sessions.Delete(key, roleName, agentName)
			}
		}
```

- [ ] **Step 4: Thread the feature ID through the run options**

In `internal/commands/runcmd.go`, add to `RunOptions` (after `LogFormat`):

```go
	// FeatureID, when set (factory mode), namespaces provider session keys so
	// identical per-feature task IDs do not resume each other's sessions.
	FeatureID string
```

Add to `liveDepsOptions` (after `Roles`):

```go
	FeatureID string
```

In `Run` (the `newLiveDeps(liveDepsOptions{...})` call), pass it through:

```go
	deps, cleanup, err := newLiveDeps(liveDepsOptions{
		ProjectDir: opts.ProjectDir,
		TeamPath:   opts.TeamPath,
		LogFormat:  opts.LogFormat,
		FeatureID:  opts.FeatureID,
	})
```

(Keep whatever `Roles` argument `Run` already passes; only add `FeatureID`.)

In `newLiveDeps`, set the namespace right after the `inv := &invoke.RoleInvoker{...}` literal (before or after the `SessionResumeEnabled()` block):

```go
	inv.SessionNamespace = opts.FeatureID
```

In `internal/commands/factorycmd.go`, `RunFeature`'s `Run(ctx, RunOptions{...})` call (around line 334) adds the feature ID:

```go
		runErr = Run(ctx, RunOptions{
			ProjectDir: d.dir,
			TeamPath:   filepath.Join(d.dir, "team.json"),
			LogFormat:  d.logFormat,
			FeatureID:  f.ID,
		})
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/invoke/ -run 'TestSessionTaskKey|TestSessionNamespace' -v`
Expected: PASS. Then `go build ./...` and `go test ./internal/commands/ -run TestRun -count=1` to confirm nothing regressed.

- [ ] **Step 6: Commit**

```bash
git add internal/invoke/role.go internal/invoke/role_test.go internal/commands/runcmd.go internal/commands/factorycmd.go
git commit -m "fix(sessions): namespace session keys by feature to stop cross-feature bleed"
```

---

### Task 5: Merge plumbing — `Feature.Merged`, `Deps.MergeFeatureToBase`, `Deps.Event`

**Files:**
- Modify: `internal/factory/factory.go` (add `Merged`/`MergedAt` to `Feature`)
- Modify: `internal/factory/engine.go` (add two methods to the `Deps` interface)
- Modify: `internal/commands/factorycmd.go` (live impls)
- Modify: `internal/factory/factory_test.go` (extend `fakeDeps` so the package compiles)
- Test: `internal/commands/factorycmd_test.go` (append `Event` test)

**Interfaces:**
- Produces:
  - `factory.Feature.Merged bool`, `factory.Feature.MergedAt *time.Time`
  - `Deps.MergeFeatureToBase(branch, base string) (method string, err error)`
  - `Deps.Event(name string, fields map[string]any)`

- [ ] **Step 1: Write the failing test**

Append to `internal/commands/factorycmd_test.go`:

```go
func TestLiveDeps_EventAppendsToRunLog(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".orquestalite"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &liveFactoryDeps{dir: dir, out: io.Discard}
	d.Event("feature_merged", map[string]any{"feature": "F001", "method": "ff"})
	raw, err := os.ReadFile(filepath.Join(dir, ".orquestalite", "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"feature_merged"`) || !strings.Contains(string(raw), "F001") {
		t.Errorf("run.log missing event: %s", raw)
	}
}
```

Add `"io"` and `"strings"` to the test imports if absent.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/commands/ -run TestLiveDeps_EventAppendsToRunLog -v`
Expected: FAIL — `d.Event` undefined.

- [ ] **Step 3: Add struct fields and interface methods**

In `internal/factory/factory.go`, add to `Feature` (after `PRURL`):

```go
	// Merged records that this feature's branch was merged into the base branch
	// (so the next feature inherits its code). Set only when the feature passed
	// the strict merge gate.
	Merged   bool       `json:"merged,omitempty"`
	MergedAt *time.Time `json:"merged_at,omitempty"`
```

In `internal/factory/engine.go`, add to the `Deps` interface:

```go
	// MergeFeatureToBase merges a gate-passed feature branch into base and leaves
	// the work tree on base. Returns the merge method ("ff" or "no-ff"). An error
	// (e.g. a conflict) leaves base unchanged for the caller to handle.
	MergeFeatureToBase(branch, base string) (method string, err error)
	// Event records a structured factory event (one JSONL line in run.log).
	Event(name string, fields map[string]any)
```

- [ ] **Step 4: Add the live implementations**

In `internal/commands/factorycmd.go` (with the other `liveFactoryDeps` methods):

```go
func (d *liveFactoryDeps) MergeFeatureToBase(branch, base string) (string, error) {
	return gitx.MergeFastForward(d.dir, base, branch)
}

// Event appends one structured event to run.log. It opens the eventlog with a
// discarded pretty writer so the human stdout stream (driven by Logf) is not
// duplicated. Best-effort: a log-open failure is silently ignored.
func (d *liveFactoryDeps) Event(name string, fields map[string]any) {
	logPath := filepath.Join(d.dir, ".orquestalite", "run.log")
	logger, err := eventlog.OpenWithFormat(logPath, io.Discard, eventlog.FormatVerbose)
	if err != nil {
		return
	}
	defer logger.Close()
	logger.Log(eventlog.Event{Type: name, Fields: fields})
}
```

(`io` and `eventlog` are already imported in `factorycmd.go`.)

- [ ] **Step 5: Extend `fakeDeps` so the factory test package compiles**

In `internal/factory/factory_test.go`, add fields to `fakeDeps`:

```go
	merges      []string         // feature branches passed to MergeFeatureToBase
	mergeMethod string           // method to return ("" -> "ff")
	mergeErr    error            // when set, MergeFeatureToBase fails
	events      []string         // event names recorded via Event
```

And add the methods:

```go
func (d *fakeDeps) MergeFeatureToBase(branch, base string) (string, error) {
	d.merges = append(d.merges, branch)
	if d.mergeErr != nil {
		return "", d.mergeErr
	}
	if d.mergeMethod != "" {
		return d.mergeMethod, nil
	}
	return "ff", nil
}
func (d *fakeDeps) Event(name string, _ map[string]any) {
	d.events = append(d.events, name)
}
```

- [ ] **Step 6: Run build + tests**

Run: `go build ./... && go test ./internal/factory/ ./internal/commands/ -run 'TestEngineRun|TestLiveDeps_EventAppendsToRunLog' -count=1`
Expected: PASS (existing engine tests still green — the engine does not yet call the new methods).

- [ ] **Step 7: Commit**

```bash
git add internal/factory/factory.go internal/factory/engine.go internal/factory/factory_test.go internal/commands/factorycmd.go internal/commands/factorycmd_test.go
git commit -m "feat(factory): merge plumbing — Feature.Merged, Deps.MergeFeatureToBase + Deps.Event"
```

---

### Task 6: Engine — merge on gate pass, bounded repair loop, stop on failure

**Files:**
- Modify: `internal/factory/engine.go` (rewrite the post-`RunFeature` body of `Run`; add `featureGatePassed` helper; add `"strings"` import)
- Modify: `internal/commands/factorycmd.go` (`Factory` populates `fcfg.MaxFeatureRetries`)
- Modify: `internal/factory/factory_test.go` (update behavior-changed tests; add new ones)

**Interfaces:**
- Consumes: `Deps.MergeFeatureToBase`, `Deps.Event`, `Summary.FailedTaskIDs`, `Config.MaxFeatureRetries` (all from Tasks 2/3/5).
- Produces: `func featureGatePassed(sum Summary, runErr error) bool`.

- [ ] **Step 1: Write/Update the failing tests**

In `internal/factory/factory_test.go`:

(a) Replace `TestEngineRun_DrainsQueue` body assertions to expect merges and no base checkouts on success:

```go
func TestEngineRun_DrainsQueue(t *testing.T) {
	q := &Queue{BaseBranch: "main", Features: ParseFeatures("## A\n\nbody\n\n## B\n\nbody\n")}
	d := &fakeDeps{}
	if err := Run(context.Background(), q, Config{}, d); err != nil {
		t.Fatal(err)
	}
	if len(d.runs) != 2 || len(d.merges) != 2 {
		t.Errorf("runs=%v merges=%v", d.runs, d.merges)
	}
	for _, f := range q.Features {
		if f.Status != StatusDone || !f.Merged {
			t.Errorf("%s = %+v", f.ID, f)
		}
		if f.StartedAt == nil || f.FinishedAt == nil || f.MergedAt == nil {
			t.Errorf("%s missing timestamps", f.ID)
		}
	}
}
```

(b) Replace `TestEngineRun_FeatureFailureContinues` with stop-on-failure:

```go
func TestEngineRun_FeatureFailureStopsQueue(t *testing.T) {
	q := &Queue{BaseBranch: "main", Features: ParseFeatures("## A\n\nbody\n\n## B\n\nbody\n")}
	d := &fakeDeps{runResult: func(f Feature) (Summary, error) {
		if f.ID == "F001" {
			return Summary{}, errors.New("agents exploded")
		}
		return Summary{TasksDone: 2}, nil
	}}
	if err := Run(context.Background(), q, Config{}, d); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(d.runs) != "[F001]" {
		t.Errorf("runs = %v, want only [F001] (queue stops on failure)", d.runs)
	}
	if len(d.merges) != 0 {
		t.Errorf("merges = %v, want none", d.merges)
	}
	if q.Features[0].Status != StatusFailed || q.Features[1].Status != StatusPending {
		t.Errorf("statuses: F001=%q F002=%q", q.Features[0].Status, q.Features[1].Status)
	}
}
```

(c) Replace `TestEngineRun_CheckpointErrorAbortsQueue` so the feature first fails the gate (so checkpoint runs):

```go
func TestEngineRun_CheckpointErrorAbortsQueue(t *testing.T) {
	q := &Queue{BaseBranch: "main", Features: ParseFeatures("## A\n\nbody\n")}
	d := &fakeDeps{
		runResult: func(Feature) (Summary, error) {
			return Summary{TasksFailed: 1, FailedTaskIDs: []string{"T001"}}, nil
		},
		checkpoint: func(Feature) (bool, error) { return false, fmt.Errorf("commit failed") },
	}
	err := Run(context.Background(), q, Config{}, d)
	if err == nil || d.bases != 0 {
		t.Fatalf("err=%v bases=%d; a checkpoint failure must abort before checking out base", err, d.bases)
	}
}
```

(d) Replace `TestEngineRun_ResumeDoesNotLoopOnRepeatedFailure` to reflect stop-on-failure:

```go
func TestEngineRun_ResumeStopsOnRepeatedFailure(t *testing.T) {
	q := &Queue{BaseBranch: "main", Features: ParseFeatures("## A\n\nbody\n\n## B\n\nbody\n")}
	q.Features[0].Status = StatusFailed
	d := &fakeDeps{runResult: func(f Feature) (Summary, error) {
		if f.ID == "F001" {
			return Summary{TasksFailed: 1, FailedTaskIDs: []string{"T001"}}, nil
		}
		return Summary{TasksDone: 1}, nil
	}}
	if err := Run(context.Background(), q, Config{Resume: true}, d); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(d.runs) != "[F001]" {
		t.Fatalf("runs = %v, want [F001] (failed feature stops the queue)", d.runs)
	}
}
```

(e) Add new tests:

```go
func TestEngineRun_MergesGatePassedFeature(t *testing.T) {
	q := &Queue{BaseBranch: "main", Features: ParseFeatures("## A\n\nbody\n")}
	d := &fakeDeps{mergeMethod: "no-ff"}
	if err := Run(context.Background(), q, Config{}, d); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(d.merges) != "[factory/001-a]" {
		t.Errorf("merges = %v", d.merges)
	}
	if !contains(d.events, "feature_merged") {
		t.Errorf("events = %v, want feature_merged", d.events)
	}
}

func TestEngineRun_GateFailStopsAndDoesNotMerge(t *testing.T) {
	q := &Queue{BaseBranch: "main", Features: ParseFeatures("## A\n\nbody\n")}
	d := &fakeDeps{runResult: func(Feature) (Summary, error) {
		return Summary{TasksDone: 1, TasksFailed: 1, FailedTaskIDs: []string{"T002"}}, nil
	}}
	if err := Run(context.Background(), q, Config{}, d); err != nil {
		t.Fatal(err)
	}
	if len(d.merges) != 0 || q.Features[0].Status != StatusFailed {
		t.Errorf("merges=%v status=%q", d.merges, q.Features[0].Status)
	}
	if !contains(d.events, "feature_merge_blocked") {
		t.Errorf("events = %v, want feature_merge_blocked", d.events)
	}
}

func TestEngineRun_RepairLoopRetriesThenMerges(t *testing.T) {
	q := &Queue{BaseBranch: "main", Features: ParseFeatures("## A\n\nbody\n")}
	call := 0
	d := &fakeDeps{runResult: func(Feature) (Summary, error) {
		call++
		if call == 1 {
			return Summary{TasksFailed: 1, FailedTaskIDs: []string{"T001"}}, nil
		}
		return Summary{TasksDone: 2}, nil
	}}
	if err := Run(context.Background(), q, Config{MaxFeatureRetries: 2}, d); err != nil {
		t.Fatal(err)
	}
	if call != 2 || len(d.merges) != 1 || q.Features[0].Status != StatusDone {
		t.Errorf("call=%d merges=%v status=%q", call, d.merges, q.Features[0].Status)
	}
}

func TestEngineRun_RepairLoopStopsOnNoProgress(t *testing.T) {
	q := &Queue{BaseBranch: "main", Features: ParseFeatures("## A\n\nbody\n")}
	call := 0
	d := &fakeDeps{runResult: func(Feature) (Summary, error) {
		call++
		return Summary{TasksFailed: 2, FailedTaskIDs: []string{"T001", "T002"}}, nil
	}}
	if err := Run(context.Background(), q, Config{MaxFeatureRetries: 5}, d); err != nil {
		t.Fatal(err)
	}
	// Initial run + exactly one retry, then the no-progress guard stops it.
	if call != 2 {
		t.Errorf("RunFeature calls = %d, want 2 (no-progress guard)", call)
	}
	if len(d.merges) != 0 || q.Features[0].Status != StatusFailed {
		t.Errorf("merges=%v status=%q", d.merges, q.Features[0].Status)
	}
}

func TestEngineRun_MergeConflictStopsQueue(t *testing.T) {
	q := &Queue{BaseBranch: "main", Features: ParseFeatures("## A\n\nbody\n\n## B\n\nbody\n")}
	d := &fakeDeps{mergeErr: errors.New("conflict")}
	if err := Run(context.Background(), q, Config{}, d); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(d.runs) != "[F001]" || q.Features[0].Status != StatusFailed {
		t.Errorf("runs=%v status=%q", d.runs, q.Features[0].Status)
	}
	if !contains(d.events, "feature_merge_blocked") {
		t.Errorf("events = %v, want feature_merge_blocked", d.events)
	}
}

// contains is a small test helper.
func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/factory/ -run TestEngineRun -v`
Expected: FAIL/compile error — `featureGatePassed` undefined and the new behavior not implemented.

- [ ] **Step 3: Rewrite the engine `Run` body**

In `internal/factory/engine.go`, add `"strings"` to the imports. Replace everything from `sum, runErr := d.RunFeature(ctx, *f, reusePlan)` (current line 117) through the end of the `for` body (current line 165, just before the closing `}` of the loop) with:

```go
		sum, runErr := d.RunFeature(ctx, *f, reusePlan)

		// Feature-level repair loop: when the feature does not pass the merge gate,
		// re-run it (reusing its task list) up to cfg.MaxFeatureRetries times,
		// stopping early once the set of failing tasks stops shrinking so a
		// structurally-unfixable feature cannot loop-fail.
		failedPrev := len(sum.FailedTaskIDs)
		for attempt := 0; !featureGatePassed(sum, runErr) && attempt < cfg.MaxFeatureRetries; attempt++ {
			if ctx.Err() != nil {
				break
			}
			d.Logf("factory: %s did not pass the merge gate (%d task(s) failing) — repair attempt %d/%d",
				f.ID, len(sum.FailedTaskIDs), attempt+1, cfg.MaxFeatureRetries)
			sum, runErr = d.RunFeature(ctx, *f, true)
			if featureGatePassed(sum, runErr) {
				break
			}
			if len(sum.FailedTaskIDs) >= failedPrev {
				d.Logf("factory: %s repair made no progress (%d task(s) still failing) — stopping retries",
					f.ID, len(sum.FailedTaskIDs))
				break
			}
			failedPrev = len(sum.FailedTaskIDs)
		}

		f.TasksDone, f.TasksFailed, f.TasksOther = sum.TasksDone, sum.TasksFailed, sum.TasksOther
		f.CostUSD = sum.CostUSD
		end := time.Now().UTC()
		f.FinishedAt = &end

		if featureGatePassed(sum, runErr) {
			method, mErr := d.MergeFeatureToBase(f.Branch, q.BaseBranch)
			if mErr != nil {
				// Conflict or git failure: keep the feature on its branch, return to
				// base, and stop the queue for the operator.
				f.Status = StatusFailed
				f.Error = "merge to base failed: " + mErr.Error()
				d.Event("feature_merge_blocked", map[string]any{
					"feature": f.ID, "branch": f.Branch, "reason": "merge_failed", "error": mErr.Error(),
				})
				d.Logf("factory: %s merge to base failed: %v — left on branch %s; resolve and `orq-lite factory --resume`",
					f.ID, mErr, f.Branch)
				if err := d.CheckoutBase(q.BaseBranch); err != nil {
					_ = d.SaveState(q)
					return fmt.Errorf("checkout base %s: %w", q.BaseBranch, err)
				}
				return d.SaveState(q)
			}
			merged := time.Now().UTC()
			f.Status = StatusDone
			f.Merged = true
			f.MergedAt = &merged
			d.Event("feature_merged", map[string]any{
				"feature": f.ID, "branch": f.Branch, "base": q.BaseBranch, "method": method,
			})
			d.Logf("factory: %s done (%d tasks done, $%.2f) — merged to %s (%s)",
				f.ID, sum.TasksDone, sum.CostUSD, q.BaseBranch, method)
			if url, err := d.PublishFeature(ctx, *f, q.BaseBranch); err != nil {
				d.Logf("factory: %s PR not created: %v", f.ID, err)
			} else if url != "" {
				f.PRURL = url
				d.Logf("factory: %s PR %s", f.ID, url)
			}
			if err := d.SaveState(q); err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}

		// Gate not passed: record the failure, preserve any residue on the feature
		// branch, return to base WITHOUT merging, and stop the queue.
		f.Status = StatusFailed
		if runErr != nil {
			f.Error = runErr.Error()
		} else {
			f.Error = fmt.Sprintf("%d task(s) did not pass the gate: %s",
				len(sum.FailedTaskIDs), strings.Join(sum.FailedTaskIDs, ", "))
		}
		d.Event("feature_merge_blocked", map[string]any{
			"feature": f.ID, "branch": f.Branch, "failed_tasks": sum.FailedTaskIDs, "reason": "gate_failed",
		})
		d.Logf("factory: %s did not pass the merge gate — %s; left on branch %s, not merged. Fix and `orq-lite factory --resume`.",
			f.ID, f.Error, f.Branch)

		if checkpointed, err := d.CheckpointResidue(*f); err != nil {
			_ = d.SaveState(q)
			return fmt.Errorf("checkpoint residue for %s: %w", f.ID, err)
		} else if checkpointed {
			d.Logf("factory: %s checkpointed uncommitted residue to %s (wip commit)", f.ID, f.Branch)
		}
		if err := d.CheckoutBase(q.BaseBranch); err != nil {
			_ = d.SaveState(q)
			return fmt.Errorf("checkout base %s: %w", q.BaseBranch, err)
		}
		if err := d.SaveState(q); err != nil {
			return err
		}
		return nil // stop the queue; operator fixes and resumes
```

Then add the helper at the end of `engine.go`:

```go
// featureGatePassed reports whether a feature run is clean enough to merge into
// base: no run error, no failed tasks, and no tasks left in a non-terminal state.
func featureGatePassed(sum Summary, runErr error) bool {
	return runErr == nil && sum.TasksFailed == 0 && sum.TasksOther == 0
}
```

Note: the old `now := time.Now().UTC()` / `f.Status = StatusInProgress` / `f.StartedAt` block (current lines 96-106) and the `CheckoutFeatureBranch` call (lines 108-115) stay unchanged above this replaced region.

- [ ] **Step 4: Wire `MaxFeatureRetries` in `Factory`**

In `internal/commands/factorycmd.go`, inside `Factory`, update the config-load block (currently lines 67-70):

```go
	fcfg := factory.Config{Resume: opts.Resume, Replan: opts.Replan}
	if cfg, cfgErr := config.Load(filepath.Join(opts.ProjectDir, "team.json")); cfgErr == nil {
		fcfg.BudgetUSD = cfg.Limits.FactoryBudgetUSD
		fcfg.MaxFeatureRetries = cfg.Limits.FeatureRetries()
	}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/factory/ -run TestEngineRun -v`
Expected: PASS (all updated + new tests).

- [ ] **Step 6: Commit**

```bash
git add internal/factory/engine.go internal/factory/factory_test.go internal/commands/factorycmd.go
git commit -m "feat(factory): merge gate-passing features to base; bounded repair loop; stop on failure"
```

---

### Task 7: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Build, vet, full test suite**

Run: `go build ./... && go vet ./... && go test ./... -count=1`
Expected: all packages build, vet clean, tests PASS.

- [ ] **Step 2: Update the spec status**

In `docs/superpowers/specs/2026-06-24-multi-feature-integrity-design.md`, change `Status: approved (design)` to `Status: implemented`.

- [ ] **Step 3: Commit**

```bash
git add docs/superpowers/specs/2026-06-24-multi-feature-integrity-design.md
git commit -m "docs(spec): mark multi-feature integrity implemented"
```

---

## Self-Review

**Spec coverage:**
- Merge-to-base (strict gate) → Tasks 1, 5, 6. ✓
- Bounded repair loop + no-progress guard → Tasks 2, 6. ✓
- Stop queue + report on failure → Task 6 (`feature_merge_blocked`, `Logf`, `return nil`). ✓
- Merge conflict → abort + stop → Tasks 1 (`MergeFastForward` abort), 6 (conflict path). ✓
- `Feature.Merged`/`MergedAt`, `Summary.FailedTaskIDs` → Tasks 3, 5. ✓
- Events `feature_merged`, `feature_merge_blocked` → Tasks 5, 6. ✓
- Per-feature session keys → Task 4. ✓
- Config `max_feature_retries` default 1 → Task 2 (+ Factory wiring in Task 6). ✓
- Backward-compat non-factory run (`FeatureID==""`) → Task 4 (test asserts bare task ID). ✓

**Placeholder scan:** No TBD/TODO; every code step shows full code. ✓

**Type consistency:** `MergeFeatureToBase(branch, base) (method string, err error)` used identically in interface (Task 5), live impl (Task 5), fake (Task 5), and engine call (Task 6). `featureGatePassed(sum Summary, runErr error) bool`, `sessionTaskKey(taskID string) string`, `FeatureRetries() int`, `Summary.FailedTaskIDs` consistent across tasks. ✓

**Behavior-change note:** Tasks 6 updates four existing engine tests whose old assertions encoded "failure continues the queue" — intentional, per the approved spec.
