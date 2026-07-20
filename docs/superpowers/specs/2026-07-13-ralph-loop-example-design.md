# Design: `examples/ralph-loop` — minimal plan.md ralph loop with adversarial meta-review

Date: 2026-07-13
Status: approved

## Goal

A new example under `examples/` showing the simplest possible ralph loop on the
flow engine: one coder role re-invoked with the **same prompt** takes the next
unchecked task from `plan.md` until every task is done, then an **adversarial
reviewer** validates the work and appends new tasks to `plan.md`; the coder
loop runs again, repeating until the reviewer approves. No parser, no
`tasks.json` — `plan.md` checkboxes are the only shared state.

## Why the flow engine (approaches considered)

- **A (chosen): `flows.json` + nested `retry_until`.** Config-only, no Go
  changes; matches how `issue-fix` / `fastapi-governed` already extend the
  engine.
- **B (rejected): legacy `orq-lite run`.** Requires the 5-role team, the
  `ParserResult`/`tasks.json` contract, and compiled squad routing — the
  opposite of "simpler".
- **C (rejected): reviewer inside the per-task loop.** Not the requested
  shape; the meta-loop must review the *whole* pass adversarially.

## Mechanics

```
retry_until {reviewer_res.pass}            (max 4 review rounds)   ← meta-loop
  ├─ command: printf %s {_attempt}         → review_round (outer round survives
  │                                          the inner loop clobbering {_attempt})
  ├─ retry_until {coder_res.pass} && {test_res.pass}   (max 25)    ← ralph loop
  │    ├─ agent ralph_coder                (same prompt every attempt)
  │    └─ command: {inputs.test_command}   (deterministic gate, on_failure: continue)
  └─ agent ralph_reviewer                  (adversarial)
```

Engine facts the design leans on:

- `retry_until` does **not** snapshot context between attempts
  (`internal/engine/runners.go:183`) — coder feedback (`{coder_res.summary}`,
  `{test_res.stdout}`) threads naturally into the next attempt. The `loop`
  step *does* isolate iterations (commit `9ed097f`) and requires a
  pre-resolved array, so it cannot iterate a task list the reviewer grows —
  hence `retry_until`, with the agent itself picking the next task.
- `derivePass` (`runners.go:240`) maps `status: completed|approved|pass` →
  `pass: true`, anything else → `pass: false`, so the conditions gate on
  `{coder_res.pass}` / `{reviewer_res.pass}`.
- Nested `retry_until`s share the `_attempt` context var; the inner loop
  overwrites it. The outer round number is captured at the top of the outer
  body via `printf %s {_attempt}` into `{review_round.stdout}`.
- `retry_until` **errors when the budget is exhausted** — a run that never
  converges fails loudly instead of shipping unapproved work.

## plan.md grammar (prompt-level contract, invisible to Go)

- `- [ ]` pending task (coder takes the **first** one each pass)
- `- [x]` done (ticked by the coder in the same commit as the implementation)
- `- [!]` blocked (coder marks it, appends an indented reason line, moves on;
  the reviewer adjudicates: reformulate as a new `- [ ]` or accept)

The reviewer appends findings as new `- [ ]` lines (with a concrete failure
scenario) and commits `plan.md`; it never edits application code.

## Files

```
examples/ralph-loop/
  flows.json            ralph_loop flow (nested retry_until, above)
  team.json             claude_haiku agents; ralph_coder + ralph_reviewer roles
                        + the 5 orchestrated roles config.Validate requires
                        (declared, unused — same caveat as issue-fix)
  plan.md               seeded 3-task demo plan with the grammar documented
  prompts/ralph-coder.md
  prompts/ralph-reviewer.md
  README.md             same shape as issue-fix: ascii flow, mermaid, inputs,
                        roles, prerequisites, run command, caveats
examples/README.md      new table row
```

## Flow inputs

| Input | Default | Purpose |
|-------|---------|---------|
| `plan_path` | `plan.md` | The checkbox plan — single source of truth. |
| `test_command` | `go test ./...` | Deterministic gate after each coder pass — override per stack. |

## Result contracts

`ralph_coder` → `.orquestalite/results/ralph_coder.json`:

```json
{ "status": "in_progress" | "completed",
  "task": "the - [ ] line just handled (or empty when completed)",
  "summary": "...", "files_changed": ["..."], "notes_for_memory": null }
```

`completed` = no `- [ ]` lines remain (returned without changing anything).
A failing deterministic gate keeps the loop alive even after `completed` —
the next attempt's prompt says: fix `{{TEST_OUTPUT}}` failures before taking
any new task.

`ralph_reviewer` → `.orquestalite/results/ralph_reviewer.json`:

```json
{ "status": "approved" | "changes_requested",
  "summary": "what was hunted and found",
  "tasks_added": 0, "notes_for_memory": null }
```

`changes_requested` requires having appended ≥1 `- [ ]` line to plan.md.
Convergence rule: from round 3 on, block only on critical findings
(wrong results, data loss, broken flows), mirroring `gov-reviewer.md`.

## Failure semantics (documented in README caveats)

- Inner budget (25) bounds coder invocations per round: tasks + fix-ups.
- Outer budget (4) bounds review rounds; exhaustion fails the flow by design.
- Contract-obedience risk (agent omits `status` etc.) is the same as every
  other example and rests on the prompts being explicit.

## Verification

- `go build ./...` and `go test ./...` stay green (no Go changes expected).
- A scratch program loads `examples/ralph-loop/flows.json` via
  `engine.LoadFlows` and `team.json` via `config.Load` to prove both parse
  and validate against the real engine.
- Prompt/flow variable cross-check: every `{{VAR}}` in the prompts has a
  matching key in the flow step `inputs`.
