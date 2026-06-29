# Fast Mode: feature-level batching for orq-lite

## Problem

`orq-lite` currently runs the full role pipeline at task granularity:

1. coder implements one task
2. tester checks that task
3. critic reviews that task
4. local gates run
5. the task is committed
6. verifier/reviewer run at the end of each feature review cycle

This is reliable and auditable, but it is slow for normal feature work. A single
Claude, Codex, or OpenCode session can often finish the same feature much faster
because it keeps the whole feature in context, performs related edits in one
continuous pass, and runs tests/lint once after the coherent batch is ready.

Add an optional `fast` mode that raises the orchestration unit by one level:

- the coder receives the whole feature plan plus the parsed task list and
  implements the feature in one provider session;
- tester and critic run after the feature implementation, not after each task;
- feature-level failures feed back into the same feature coder loop;
- verifier/reviewer that currently run after a feature cycle move to the end of
  the full factory run, after all runnable features have completed.

The default mode must remain unchanged.

## Goals

- Preserve existing `orq-lite run` and `orq-lite factory` behavior unless fast
  mode is explicitly enabled.
- Reduce agent invocations for a feature from roughly `tasks * 3 + cycle roles`
  to roughly `feature * 3`, plus final global verification/review.
- Keep parser output useful as a checklist and status artifact, without forcing
  task-by-task execution.
- Keep local deterministic gates (`lint_command`, `full_test_command`) before
  accepting a feature.
- Keep a review path that can create remediation tasks, but run it at feature
  scope in fast mode.

## Non-goals

- Do not remove task-level mode.
- Do not remove tester, critic, verifier, reviewer, or result contracts.
- Do not make fast mode the default.
- Do not parallelize features in this change.
- Do not change provider CLIs or implement in-process tool use.

## User-facing behavior

Add a fast mode flag and config knob:

- CLI: `orq-lite factory plan.md --fast`
- CLI: `orq-lite run --fast`
- Config: `limits.fast_mode: true`

CLI flags override config for that invocation.

In normal mode, behavior is unchanged.

In fast mode, a factory feature runs as:

1. planner extracts features as today
2. parser decomposes the current feature into tasks as today
3. feature coder receives:
   - the feature plan
   - the parsed task list
   - current memory/conventions/skills
   - previous feature-level tester/critic feedback, if retrying
4. coder implements all tasks for the feature in one session
5. lint gate runs once for the feature
6. tester runs once for the feature
7. critic runs once for the feature
8. full test suite runs once for the feature
9. feature is committed/merged when accepted
10. after all selected features finish, global verifier and reviewer run once

If tester or critic rejects the feature, the feature coder is reinvoked with the
feedback and resumes the same provider session when supported. The retry budget
uses `limits.max_fix_iterations`.

## State model

Add a feature-level run state without deleting task state.

Tasks in fast mode are still written to `.orquestalite/tasks-Fxxx.json` and
`.orquestalite/tasks.json`, but they are treated as a checklist rather than the
execution queue.

When the feature coder succeeds and local gates pass:

- mark every parsed task for that feature `done`
- set each task `verify_state` to `feature_commit_ok`
- set `last_agent` to the feature coder agent
- record the feature commit SHA in run log

When the feature fails:

- leave incomplete tasks as `pending` or mark them `failed` with
  `failure_reason: feature_fast_failed`
- persist feature-level feedback in `.orquestalite/results/feature-*.json`
- rollback the feature worktree to the feature start SHA

Add verify states and failure reasons as needed:

- `verify_state: feature_commit_ok`
- `verify_state: feature_tests_pass`
- `failure_reason: feature_fast_failed`
- `failure_reason: feature_lint_failed`
- `failure_reason: feature_tests_failed`
- `failure_reason: feature_critic_rejected`

## Role contracts

### Feature coder

Add a feature-coder prompt, or extend the existing coder prompt with a mode var:

- prompt path: `prompts/feature-coder.md`
- result path: `.orquestalite/results/feature-coder.json`

The feature coder must output:

```json
{
  "status": "completed",
  "summary": "what changed",
  "tasks_completed": ["T001", "T002"],
  "files_changed": ["..."],
  "notes_for_memory": null
}
```

Allowed statuses:

- `completed`
- `blocked`

The prompt must explicitly say:

- implement the whole feature, not only the first task
- use the task list as an acceptance checklist
- do not stop after creating scaffolding if later tasks need code/tests
- run focused checks locally when useful
- final action must write the result JSON

### Feature tester

Add a feature tester prompt, or extend tester with a mode var:

- prompt path: `prompts/feature-tester.md`
- result path: `.orquestalite/results/feature-tester.json`

The tester checks the full feature against all parsed tasks and the original
feature plan.

### Feature critic

Add a feature critic prompt, or extend critic with a mode var:

- prompt path: `prompts/feature-critic.md`
- result path: `.orquestalite/results/feature-critic.json`

The critic reviews the full feature diff. It should flag:

- missing acceptance criteria
- scope creep
- poor architecture
- test gaps
- incidental artifacts
- changes that should be split before merge

## Loop changes

Add a new loop package entry point:

```go
RunFeatureFastLoop(ctx, feature, taskList, deps, rc) (*FeatureFastResult, error)
```

It should perform:

1. capture rollback base
2. run feature coder
3. run lint gate
4. run feature tester
5. run feature critic
6. run full suite
7. commit feature
8. mark tasks done

Retry rules:

- lint failure feeds `LINT_FEEDBACK` to feature coder
- tester failure feeds `TESTER_FEEDBACK` to feature coder
- critic rejection feeds `CRITIC_FEEDBACK` to feature coder
- repeat until `max_fix_iterations`
- on exhaustion, rollback and mark feature failed

Do not run task-level `RunTaskLoopWithContext` in fast mode.

## Factory behavior

In fast mode, factory should still process features sequentially.

For each feature:

1. checkout feature branch
2. parse/reuse feature tasks
3. run `RunFeatureFastLoop`
4. sync `.orquestalite/tasks.json` back to `.orquestalite/tasks-Fxxx.json`
5. if feature accepted, merge/publish as today
6. if feature failed, apply existing feature retry/no-progress behavior

After the factory queue drains, run a new global finalization phase:

1. global verifier checks all merged/finished features together
2. global reviewer reviews the whole factory increment
3. if reviewer creates follow-up tasks, attach them to a new synthetic feature
   or fail the factory with a clear handoff

The global finalizer runs once per factory execution, not once per feature.

## Run command behavior

For non-factory `orq-lite run --fast`:

- load `.orquestalite/tasks.json`
- treat all pending tasks as one batch
- run feature-fast loop with a synthetic feature id, e.g. `_run`
- mark all accepted tasks done together
- commit once
- run verifier/reviewer once after the batch

## Observability

Add run log events:

- `feature_fast_start`
- `feature_fast_agent_run`
- `feature_fast_lint_failed`
- `feature_fast_tester_failed`
- `feature_fast_critic_rejected`
- `feature_fast_done`
- `feature_fast_failed`
- `factory_global_verification`
- `factory_global_review`

`orq-lite status` should show:

- mode: `task` or `fast`
- current feature
- current feature attempt
- feature-level role currently running
- task checklist progress for the feature

## Acceptance criteria

- Default task-level mode behavior is unchanged.
- `orq-lite factory plan.md --fast` runs each feature with one feature coder
  call, one feature tester call, and one feature critic call on the happy path.
- In fast mode, task-level tester/critic are not invoked per task.
- In fast mode, parsed tasks are still persisted and marked done when the
  feature-level gates pass.
- A feature tester failure feeds back into the same feature coder loop.
- A feature critic rejection feeds back into the same feature coder loop.
- `lint_command` runs once per feature attempt, not once per task.
- `full_test_command` runs once per accepted feature, not once per task.
- Factory verifier/reviewer run once after all features complete, not after
  each feature.
- A failed fast feature rolls back to the feature start SHA and leaves a clear
  failure reason.
- Existing unit tests for task-level loops still pass.

## Suggested implementation slices

### Slice 1: Config and CLI plumbing

- Add `limits.fast_mode`.
- Add `--fast` to `run` and `factory`.
- Thread a `FastMode bool` through command options.
- Add tests proving CLI flag overrides config.

### Slice 2: Feature-level result contracts and prompts

- Add feature coder/tester/critic result structs or reuse existing structs with
  archive role names.
- Add `prompts/feature-coder.md`, `prompts/feature-tester.md`,
  `prompts/feature-critic.md`.
- Add embedded assets and schemas if schemas are role-specific.

### Slice 3: Feature fast loop

- Implement `RunFeatureFastLoop`.
- Unit test happy path:
  coder -> lint ok -> tester pass -> critic approved -> full suite pass ->
  commit -> all tasks done.
- Unit test tester fail then coder retry.
- Unit test critic reject then coder retry.
- Unit test lint fail then coder retry.

### Slice 4: Factory integration

- In factory mode, choose task loop or feature-fast loop based on mode.
- Keep branch/merge/publish behavior unchanged.
- Sync task state back to `tasks-Fxxx.json`.
- Add integration test with fake agents proving one feature creates one commit
  and marks all tasks done.

### Slice 5: Global final verifier/reviewer

- Move verifier/reviewer calls behind a mode check:
  - task mode: existing end-of-cycle behavior
  - fast factory mode: final global behavior
- Add prompts or vars that include all finished features and merged commits.
- Add tests proving verifier/reviewer run once after the full queue.

### Slice 6: Status and logs

- Add mode/current-feature/current-role display.
- Add human log rendering for feature-fast events.
- Add regression tests for event emission.

## Risks and mitigations

- Risk: feature batches are too large and lower code quality.
  Mitigation: keep task mode default; document that fast mode is for trusted
  plans or experienced operators.

- Risk: one feature commit is harder to review than per-task commits.
  Mitigation: keep parsed tasks and result archive; include task checklist in
  commit message/body.

- Risk: tester/critic feedback becomes too broad.
  Mitigation: prompts must return concerns grouped by task id when possible.

- Risk: global final reviewer creates broad follow-up work after many features.
  Mitigation: cap global follow-ups and write a handoff if the queue is already
  too large.

## Expected impact

For a feature with 8 tasks:

- current happy path: roughly 24 task role calls plus verifier/reviewer cycles
- fast happy path: roughly 3 feature role calls plus one final global verifier
  and reviewer for the whole factory run

This should make fast mode much closer to a single Claude/Codex/OpenCode
session while preserving the core orq-lite safety rails.
