# Rollback safety — Option B (reset-to-base + targeted untracked removal)

## Goal
Stop `Rollback` from deleting untracked files it didn't create. A failed task must
leave the tree exactly as the task found it: revert the agent's tracked changes
AND remove only the untracked files the agent created — never pre-existing user
files (scratch notes, un-added WIP).

## Design
- Policy lives in `gitx.RollbackTo(dir, baseSHA, keepUntracked)`:
  - `git reset --hard baseSHA` reverts tracked modifications + restores tracked deletions
  - remove untracked files (`git ls-files --others --exclude-standard`) NOT in `keepUntracked`
  - prune empty dirs left behind (best-effort); ignored files never touched
- `liveDeps` captures base (`HeadSHA` + untracked snapshot) at the start of `RunFix`
  (always runs before any `Rollback` in the same task iteration).
- Delete `gitx.CheckoutAll` (the `git clean -fd` primitive being replaced).

## Steps
- [x] gitx: add `UntrackedFiles`, `ResetHard`, `RollbackTo` (+ tests, red first)
- [x] gitx: remove `CheckoutAll` + its test
- [x] runcmd: add `taskBaseSHA`/`taskUntracked` fields, `captureRollbackBase()`, call in `RunFix`, rewrite `Rollback`
- [x] e2e: prove a pre-existing untracked user file survives a failed-task rollback; agent's new file is removed
- [x] run full suite; build; vet

## Review
Done. `Rollback` no longer runs repo-wide `git clean -fd`.

- `gitx.RollbackTo(dir, baseSHA, keepUntracked)`: `reset --hard` to the task's
  base commit, then remove only untracked files absent from the start-of-task
  snapshot; prunes empty dirs; ignored files untouched.
- `liveDeps.captureRollbackBase()` runs at the top of `RunFix` (always precedes
  any `Rollback`), recording `HeadSHA` + `UntrackedFiles` snapshot. No change to
  the `TaskDeps` interface or the task loop.
- Deleted `gitx.CheckoutAll` (the `checkout .` + `clean -fd` primitive).
- Tests: gitx unit tests for `UntrackedFiles`/`ResetHard`/`RollbackTo`; e2e
  `max_iterations` now asserts a pre-existing `notes.txt` survives rollback while
  the agent's `work.txt` is removed. Full suite + vet green.
- Docs: CONTEXT.md updated.

Known limitation (documented in `RollbackTo`): an untracked file the agent
*deletes* cannot be restored — it exists in no commit. Matches prior behaviour.
