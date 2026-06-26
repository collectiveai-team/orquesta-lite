# Spec B — Squad routing (per-task role lanes)

Date: 2026-06-26
Status: approved (brainstormed)
Branch: `feat/factory-squad-routing`

## Problem

The per-task execution sequence is hardcoded: every task runs
coder → tester → critic → verifier (`internal/loops/fix.go` `RunFix`, driven by
`internal/loops/task.go` `RunTaskLoop`). Tasks that are not "write code with
behavior to test" pay the full price and hit false friction:

- A **repo-setup** task ("scaffold uv project: `pyproject.toml`, `.gitignore`,
  `uv.lock`") has no behavior to test, yet the tester demands a
  `tests/test_scaffold.py` and the critic over-analyzes. Observed in a real run:
  the tester *failed* the scaffold task for "no test coverage".
- A **reconcile/chore** task the reviewer creates ("remove forbidden third test
  module", "reconcile tasks") is non-code bookkeeping, but still loops through
  tester + critic.

Result: wasted cycles, spurious failures, and slower runs (issues #1, #2).

## Scope

- **#2 / #7** — route each task to the role lane ("squad") it actually needs,
  decided per task by the planning layer.

Out of scope: feature-boundary scoping (#1, the over-decomposition where one
feature absorbs another's tasks) — a separate planning-layer spec.

## Decisions (from brainstorming)

1. **Three fixed squads:** `setup`, `full`, `generic`.
   - `setup` → `coder` only, then commit. No tester/critic/verifier, no full
     test suite. For scaffolding / deps / config.
   - `full` → coder → tester → critic → verifier (today's behavior, unchanged).
     For real code with behavior to test.
   - `generic` → a new `generalist` role, then commit. No tester/critic/verifier,
     no full test suite. For non-code reconcile / docs / chores.
2. **The parser decides, per task.** The parser emits a `squad` on every task it
   produces, guided by its prompt. Granularity is per-task; one decision site.
3. **Defaults / back-compat.** Empty/missing `squad` normalizes to `full` (the
   safe, unchanged behavior). Tasks the **reviewer** creates (reconcile) default
   to `generic`, with an explicit override to `full` when the task is real code.
4. **Execution architecture: dispatcher in `RunTaskLoop` (Approach A).** The loop
   switches per task on `squad`: `full` → `RunFix`; `setup`/`generic` →
   `RunSingle(role)`. `RunFix` is untouched. The shared commit / rollback path
   stays in `RunTaskLoop`.
5. **`setup`/`generic` skip the full test suite (`FullSuite`) too,** not just the
   review loop. A scaffold/chore task must not be gated by `full_test_command`.

## Design

### Data model (`internal/tasks`)

- Add `Task.Squad string` (`json:"squad,omitempty"`).
- Add `Task.SquadOrDefault() string`: returns `full` when `Squad` is `""` or
  unrecognized (fail-safe), else the recognized value. Recognized set:
  `setup`, `full`, `generic`.
- Add squad constants: `SquadSetup`, `SquadFull`, `SquadGeneric`.

### Execution (`internal/loops`)

`RunTaskLoop` (task.go) currently, per task: `RunFix` → `FullSuite` → `Commit`.
Rewrite the per-task body to dispatch on `task.SquadOrDefault()`:

```
switch squad {
case full:
    fix := d.RunFix(...)        // coder→tester→critic→verifier (unchanged)
    if !fix.done { fail; continue }
    if d.FullSuite(...) fails { rollback; fail; continue }
    commit(...)                 // shared commit + #12 no-op handling
case setup:
    out := d.RunSingle(ctx, "coder", rc)
    if !out.done { fail; continue }
    commit(...)                 // NO FullSuite
case generic:
    out := d.RunSingle(ctx, "generalist", rc)
    if !out.done { fail; continue }
    commit(...)                 // NO FullSuite
}
```

- New `TaskDeps.RunSingle(ctx, role string, rc invoke.RunContext) (SingleOutcome, error)`:
  invokes exactly one role once, with bounded retry on transient agent failure
  (no result / crash) — no tester/critic/verifier loop. `SingleOutcome{Status:
  "done"|"failed", Summary string, FilesChanged []string}`.
- Live impl (`internal/commands`): resolves the role spec from `team.json`,
  invokes via the existing `invoke.RoleInvoker`, maps a written result to `done`,
  a missing/failed result (after retries) to `failed`.
- The commit path already handles `ErrNothingToCommit` (#12) and
  `ErrCommitSkipped`; setup/generic reuse it unchanged.

### New role `generalist`

- `team.json`: add a `generalist` role (claude agent, `prompts/generalist.md`,
  a results path, a timeout).
- `prompts/generalist.md`: a general-purpose engineer prompt — perform the
  described task (reconcile, docs, chores, non-code edits) precisely, write the
  result file, report done. No test/críticism framing.
- `internal/commands/initcmd.go` scaffold assets: include `generalist` in the
  default `team.json` and ship `prompts/generalist.md` so `orq-lite init` wires
  it. (If absent at runtime, a `generic` task falls back to `full` with a warning
  so a stale team.json never hard-fails.)

### Who sets the squad

- **Parser:** its result schema (`schemas/parser*.json`) gains an optional
  `squad` per task; the parser prompt (`prompts/parser*.md`) gets classification
  rules:
  - `setup` — creating/scaffolding project structure, dependency manifests, lock
    files, config, ignore files; nothing with runtime behavior to assert.
  - `generic` — non-code reconciliation, documentation, chores, file moves.
  - `full` — anything that adds or changes code behavior (the default if unsure).
- **Reviewer:** when it appends reconcile tasks (`tasks.Append` site), default
  their `Squad` to `generic`; allow the reviewer schema/prompt to set `full` when
  the new task is real code.

### Gate integration (Group A)

No change. A `setup`/`generic` task that completes is `done` and counts toward
the merge gate exactly like a `full` task. If `RunSingle` fails (no result after
retries), the task is `failed`, which blocks the gate (correct). The #13
reset-failed-on-retry and #12 empty-commit-no-op both apply to setup/generic.

### Observability (#6)

- Emit `task_routed{task_id, squad}` at the start of each task.
- `orq-lite status` shows the squad column per task.

### Back-compat

Additive. A `tasks.json` produced before this change has no `squad`, so every
task normalizes to `full` — identical to today. The parser/reviewer schema
changes are additive (optional field). A `generic` task with no `generalist`
role in `team.json` falls back to `full` with a logged warning.

## Testing (TDD — tests first)

- `internal/tasks`: `Squad` round-trips; `SquadOrDefault` returns `full` for
  empty and unknown, and the recognized value otherwise.
- `internal/loops` (stub `TaskDeps` with `RunSingle`):
  - `setup` task → `RunSingle("coder")` called, `RunFix` and `FullSuite` NOT
    called, commit happens, task `done`.
  - `generic` task → `RunSingle("generalist")` called, no FullSuite, `done`.
  - `full` task (and empty squad) → `RunFix` + `FullSuite` path, unchanged.
  - `RunSingle` returns failed → task `failed`, rollback, no commit.
- `internal/commands`: live `RunSingle` resolves the role and maps
  result-present→done / result-missing→failed; `generic` with no `generalist`
  role → fallback to `full` + warning.
- Reviewer: appended reconcile tasks carry `Squad=generic`.
- Regression: existing factory/run/loops tests stay green (all default to
  `full`).

## Rollout

Behavior change: scaffold and reconcile tasks no longer run tester/critic/
verifier or the full test suite — they run one role and commit. This is the
intended fix for #2. Everything else (`full`, the default) is unchanged, so the
Group A merge gate and existing runs behave exactly as before.
