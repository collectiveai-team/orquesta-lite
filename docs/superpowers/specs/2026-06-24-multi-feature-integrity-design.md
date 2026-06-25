# Spec A — Multi-feature integrity (merge-to-base + per-feature sessions)

Date: 2026-06-24
Status: implemented
Branch: `feat/factory-multi-feature-integrity`

## Problem

In `orq-lite factory` mode, each feature runs on its own branch created **from the
base branch**, and the branch is **never merged back**. Observed in a real run
(`test-prd-orq`):

- `factory/001-...` produced `pyproject.toml`, `app/main.py`, health tests.
- `factory/002-...` was branched from `master` (the base), **not** from F001, so it
  was missing `app/main.py` even though F002's plan said "wire `/events` into
  `main.py`". Root cause: `engine.go` calls `CheckoutFeatureBranch(branch, base)`
  (`internal/factory/engine.go:113` → `gitx.CheckoutNewBranch(name, base)`), and
  there is no merge/rebase/stacking step anywhere.
- Compounding bug: provider sessions are keyed by `task → role → agent`
  (`internal/sessions/sessions.go`), but task IDs **reset per feature**
  (F001 and F002 both use `T001, T002, ...`). So F002's coder on `T001`
  **resumed F001's `T001` coder session** (`d8fdf1e8`). The agent's conversation
  context claimed `main.py` existed while the branch did not have it.

These two defects together make multi-feature runs produce incoherent features.

## Scope

- **#11** — feature code must carry forward between features (merge-to-base).
- **#9-bug** — provider sessions must be isolated per feature.

Out of scope (later specs): squad routing (B), observability/UX (C), conventions
(D), parallelism (E).

## Decisions (from brainstorming)

1. **Integration model: merge-to-base.** Each feature that passes the gate is
   merged into the base branch before the next feature starts; the next feature
   branches from the now-updated base. Failed features are NOT merged.
2. **Merge gate: strict.** A feature merges only if every task is `done` AND there
   is no `wip ... not gate-passed` residue (clean tree, all committed through the
   gated `task_done`). Concretely: `runErr == nil && TasksFailed == 0 &&
   TasksOther == 0`.
3. **On gate failure: bounded repair loop, then stop.** Re-run the feature
   (reusing its task list / resume) up to `limits.max_feature_retries` (default
   `1`). Between attempts, compare the set of failing task IDs; if it did not
   shrink, break early (no-progress guard) to avoid the loop-fail seen with
   structurally-unfixable tasks. After retries are exhausted or no progress is
   made, mark the feature `StatusFailed`, checkpoint residue to its branch, return
   to base **without merging**, and **stop the queue** with a report.
4. **Infra error stops the queue** (git checkout, state persistence) as today, but
   also halts rather than moving on.

## Design

### Branching / merge (engine.go `Run`)

Today the post-feature decision always continues to the next feature. Rewrite into
three outcomes after `RunFeature`:

- **Gate passes:** `d.MergeFeatureToBase(f.Branch, q.BaseBranch)`; set
  `f.Merged = true`, `f.MergedAt`; emit `feature_merged`. Continue the loop. The
  next `CheckoutFeatureBranch(next, base)` now starts from base-with-F001.
- **Gate fails:** run the repair loop (below). On final failure: `StatusFailed`,
  `CheckpointResidue`, `CheckoutBase`, emit `feature_merge_blocked`, persist state,
  and `return` (stop the queue) with a human report.
- **Infra error:** persist state and `return err`.

Repair loop (gate failure path):

```
failedPrev := <failed set from the run that just finished>
for attempt := 1; attempt <= cfg.MaxFeatureRetries; attempt++ {
    sum, runErr = d.RunFeature(ctx, *f, true /* reusePlan */)
    if runErr == nil && sum.TasksFailed == 0 && sum.TasksOther == 0 {
        // gate now passes → fall through to merge
        break
    }
    failedNow := set(sum.FailedTaskIDs)
    if !shrunk(failedNow, failedPrev) { // no-progress guard
        break
    }
    failedPrev = failedNow
}
```

`shrunk(now, prev)` is true only when `len(now) < len(prev)` (the failing set got
strictly smaller). Same-size or larger ⇒ no progress ⇒ stop.

After the loop exits (either because the gate passed or the guard tripped),
re-evaluate the same gate condition once: if it now passes, merge; otherwise mark
the feature failed and block. This keeps a single source of truth for "did the
feature pass" rather than duplicating the check inside the loop.

### New `Deps` method + gitx helper

- `Deps.MergeFeatureToBase(branch, base string) error` (interface in
  `engine.go`; live impl in `internal/commands/factorycmd.go`).
- `gitx.MergeFastForward(dir, base, branch string) error`:
  `git checkout base` then `git merge --ff-only branch`. If not fast-forwardable,
  retry with `--no-ff` (creates a merge commit preserving the feature boundary).
  On conflict: `git merge --abort` and return an error → treated as a gate-blocked
  stop (the feature stays intact on its branch).

### State (`factory.json`)

Add to `factory.Feature`: `Merged bool` and `MergedAt *time.Time`. Used so
`--resume` does not re-merge an already-merged feature. `factory.Summary` gains
`FailedTaskIDs []string` to drive the no-progress guard.

### Config

`limits.max_feature_retries` (int, default `1`) via a `Limits.MaxFeatureRetries()`
accessor in `internal/config/config.go`, mirroring existing `Limits` accessors.

### Per-feature session keys (#9-bug)

The store stays keyed `task → role → agent`, but the **task key is namespaced by
feature**: `key = feature + "/" + task` when a feature is present, else `task`
(unchanged for non-factory `run`). Implementation:

- Add `FeatureID string` to `invoke.RunContext`.
- At the save site (`internal/invoke/role.go:256-262`) and load site (`:306-310`),
  compose the key via a helper `sessionTaskKey(featureID, taskID)`:
  `featureID == "" ? taskID : featureID + "/" + taskID`.
- The factory already knows the feature; thread `f.ID` into `RunContext.FeatureID`
  where the factory builds run contexts (`internal/commands/runcmd.go` /
  `factorycmd.go`).

Result: F001/T001 and F002/T001 are isolated; resume within the same feature+task
still reuses.

### Events (`internal/eventlog`)

- `feature_merged{feature, branch, base, method}` where `method ∈ {ff, no-ff}`.
- `feature_merge_blocked{feature, branch, failed_tasks, reason}`.

## Error handling

- **Merge conflict:** `git merge --abort`; feature left intact on its branch; queue
  stops; `feature_merge_blocked` with `reason=conflict`.
- **Resume after a blocked feature:** the failed feature is runnable again
  (`Queue.NextRunnable(resume)`); if it now passes the gate it merges and the queue
  continues from the next feature.
- **No-progress in repair loop:** stop after the guard trips; report the persistent
  failing task set so the operator can intervene (often a Group-B squad-routing
  case).

## Testing (TDD — tests first)

- `internal/factory/factory_test.go` (fake `Deps`):
  - F001 gate passes → `MergeFeatureToBase` invoked once → F002 starts; assert base
    advanced. F002 gate passes → merged.
  - F001 `TasksFailed > 0` → no merge; repair loop runs ≤ `max_feature_retries`;
    no-progress guard stops it; queue halts; F002 never started.
  - Repair loop that makes progress (failing set shrinks) then passes → merges.
  - Infra error → `Run` returns the error, state persisted.
- `internal/gitx/gitx_test.go`: `MergeFastForward` ff path; non-ff → no-ff merge
  commit; conflicting branches → abort + error.
- `internal/sessions/sessions_test.go` + `internal/invoke/role_*_test.go`:
  `F001/T001` and `F002/T001` resolve to distinct entries; same feature+task
  reuses; empty feature (`run` mode) behaves exactly as today.
- Regression: existing factory/run tests stay green.

## Rollout

Behavior change: a partial feature (some tasks failed) now **stops the queue**
instead of moving on. This is intentional (chosen in brainstorming) and surfaced
via the new report + `feature_merge_blocked` event. `max_feature_retries`
defaults to `1`, so at most one extra feature attempt before stopping.
