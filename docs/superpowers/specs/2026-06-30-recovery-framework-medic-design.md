# Recovery framework + `medic` role — design

**Status:** approved (design) · **Date:** 2026-06-30

## Problem

The factory halts on mechanical, recoverable failures that an agent could fix in
seconds. Observed example — a feature passed every gate, but the branch merge hit
a trivial `pyproject.toml` conflict and the whole run stopped for manual
`orq-lite factory --resume`:

```
factory: F010 merge to base failed: ... CONFLICT (content): Merge conflict in pyproject.toml
Automatic merge failed; ... — left on branch ...; resolve and `orq-lite factory --resume`
```

`gitx.MergeFastForward` even runs `git merge --abort` on conflict, discarding the
state, then the engine (`internal/factory/engine.go`) leaves the feature on its
branch. No recovery is attempted.

## Goal

A reusable **recovery framework**: when an orchestration step fails in a way a
registered handler recognizes, hand the failure to a dedicated **medic** agent
that resolves it, verify the resolution, and continue the run — falling back to
today's manual halt only when recovery is exhausted. Ship the framework plus one
proven handler (merge conflict). New recoverable failures become one registry
entry each.

## Non-goals

- No new recoverable failures beyond merge conflict in this iteration (YAGNI).
- No change to the gate logic (lint/tester/critic/full-suite) — recovery wraps
  *infrastructure* failures around the gates, not the gates themselves.
- Not the architect/pm/qa incremental-planning loop — that is a separate spec.

## Architecture — typed recovery registry (`internal/recovery`)

A new package. Each recoverable failure is one `Handler`:

```go
// Failure is the structured context handed to the medic prompt.
type Failure struct {
    Kind    string            // "merge_conflict"
    Summary string            // human one-liner for logs
    Vars    map[string]string // → medic.md template ({{FAILURE_KIND}}, {{FAILURE_CONTEXT}}, …)
}

type Handler interface {
    Kind() string                        // stable identifier
    Detect(err error) bool               // does this handler own this failure?
    Prepare(dir string) (Failure, error) // gather context WITHOUT destroying state
    Verify(dir string) error             // confirm resolved; nil == success
    Abort(dir string) error              // clean up when giving up
}

type Registry struct{ handlers []Handler }
func (r *Registry) Match(err error) (Handler, bool) // first handler whose Detect matches
```

The safety loop is handler-agnostic:

```go
// Attempt runs the verify+retry loop. medic resolves; the handler verifies.
func Attempt(ctx, h Handler, dir string, medic MedicRunner, log Logger) (recovered bool) {
    const maxAttempts = 2 // one attempt + one retry, then halt
    for attempt := 1; attempt <= maxAttempts; attempt++ {
        f, err := h.Prepare(dir)
        if err != nil { break }
        log("recovery_attempt", {kind: h.Kind(), attempt})
        if err := medic.Resolve(ctx, f); err != nil { continue }
        if err := h.Verify(dir); err == nil {
            log("recovery_succeeded", {kind: h.Kind(), attempt})
            return true
        }
    }
    _ = h.Abort(dir)
    log("recovery_failed", {kind: h.Kind()})
    return false
}
```

`MedicRunner` is a thin interface satisfied by a `liveDeps`/factory method that
invokes the `medic` role — so the `recovery` package has no dependency on
`invoke`/`commands` (testable with a fake runner).

## Flow change — stop auto-aborting the conflict

`gitx.MergeFastForward` currently aborts on conflict. Change:

- Add sentinel `gitx.ErrMergeConflict`.
- On `--no-ff` conflict, **leave the conflicted tree in place** and return
  `ErrMergeConflict` (do not `--abort`). Only `MergeFeatureToBase` calls this, so
  the blast radius is contained; recovery (or the fallback) owns the cleanup.

Engine integration (`engine.go`, at the existing merge-failure branch ~line 164):

```
method, mErr := d.MergeFeatureToBase(f.Branch, base)
if mErr != nil {
    if h, ok := recovery.Default.Match(mErr); ok &&
        recovery.Attempt(ctx, h, dir, d.medic, d.Event) {
        // resolved + verified → treat as merged, continue the queue
    } else {
        // exhausted/unmatched → today's behavior: leave on branch, --resume
    }
}
```

## The `medic` role

- New role in `team.json` and the embedded `init` asset
  (`internal/commands/assets/team.json`), prompt `prompts/medic.md` +
  `internal/commands/assets/prompts/medic.md`, archive role `medic`, result
  schema parsed by `results.ParseMedic` → `{status: "resolved"|"blocked", summary}`.
- **Prompt contract:** *"A recoverable failure halted the run. Resolve it
  minimally so the run can continue. Do NOT expand scope, add features, or touch
  unrelated code. When done, leave the repository in a clean, committed state."*
  Template vars: `{{FAILURE_KIND}}`, `{{FAILURE_SUMMARY}}`, `{{FAILURE_CONTEXT}}`.
- The medic is an ordinary tool-using agent (like the coder): it edits files and
  runs git. For a merge conflict it resolves the markers, `git add`, and commits
  to complete the merge.

## First handler: `MergeConflictHandler`

| Method | Implementation |
|--------|----------------|
| `Kind` | `"merge_conflict"` |
| `Detect` | `errors.Is(err, gitx.ErrMergeConflict)` |
| `Prepare` | conflicted files (`git diff --name-only --diff-filter=U`), the conflict hunks, and `git status` → `Failure.Vars["FAILURE_CONTEXT"]` |
| `Verify` | `git ls-files -u` empty (no unmerged paths) **and** no `.git/MERGE_HEAD` (merge committed) **and** `FullSuite` passes (resolution didn't break the build) |
| `Abort` | `git merge --abort` |

`Verify` re-running the full suite is deliberate: a bad conflict resolution can
merge broken code, so we pay the suite cost once before accepting the fix.

## Events

`recovery_attempt` (`kind`, `attempt`), `recovery_succeeded` (`kind`, `attempt`),
`recovery_failed` (`kind`). Emitted via the existing eventlog so the dashboard and
run.log show the recovery story.

## Error handling & safety

- Bounded: 2 attempts (one + one retry), then `Abort` and fall back to the
  current manual-halt behavior — never an infinite loop.
- State-safe: the conflict is only left in the tree while recovery is active; any
  exhaustion path calls `Abort` so the working tree returns to a clean base.
- Verified: a fix is accepted only when `Verify` (including the full suite)
  passes — no unverified merge reaches base.

## Testing

- `internal/recovery`:
  - `Registry.Match` selects the right handler / returns false for unmatched errors.
  - `MergeConflictHandler` against a **real** git conflict in a temp repo:
    `Detect` true on `ErrMergeConflict`; `Verify` fails on an unresolved tree and
    passes after the conflict is resolved + committed; `Abort` restores base.
  - `Attempt` loop with a fake `MedicRunner`: resolver-that-succeeds → `recovered
    == true`, one `recovery_succeeded`; resolver-that-no-ops → two attempts then
    `Abort` + `recovery_failed`, `recovered == false`.
- `internal/gitx`: `MergeFastForward` returns `ErrMergeConflict` and leaves the
  tree conflicted (does not abort) on a real conflict.
- `internal/factory` engine: a merge conflict that the (fake) medic resolves
  continues the queue and marks the feature merged; an unresolved one halts as
  today.

## Rollout

Backward compatible. Recovery is attempted only when a `medic` role is configured
in `team.json`; if it is absent (projects scaffolded before this feature), the
engine skips `recovery.Attempt` and behaves exactly as today (manual `--resume`).
`orq-lite init` adds the role and prompt going forward; a note in the upgrade path
covers existing projects.
