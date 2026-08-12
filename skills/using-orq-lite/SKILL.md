---
name: using-orq-lite
description: Use when running, monitoring, recovering or extending orq-lite — launching a flow, choosing between factory/plan/review/intake, authoring or editing a v2 flow, subflow, pack or role, or diagnosing a run that failed, hung, sits in needs_human, or aborted with "agent.invoke permanent".
---

# Using orq-lite

orq-lite runs coding-agent workflows durably. Behavior is **data**: versioned v2 flows compose activities and subflows, and ship with schemas, policies and prompts inside verified packs. There is no imperative runtime to patch — you change a flow, validate it, and run it.

`guide.md` in the repo root is the setup procedure: preconditions, `init`, proving gates, configuring roles, writing an objective, launching, GitHub watch mode. **Read it for setup.** This skill covers what guide.md does not: the command surface at a glance, how to pick a flow, how to author one, and the runtime semantics that cost real money when you learn them by accident.

## Command surface

| Need | Command |
|---|---|
| Preflight everything before spending | `orq-lite doctor` |
| Build the whole spec | `orq-lite factory <features.md>` — batch by default, `--fast=false` for one ticket per pass |
| Decompose into tickets only | `orq-lite plan <plan.md>` |
| Work an existing ticket list | `orq-lite run` |
| Review a PR diff | `orq-lite review --pr N` |
| Triage a GitHub issue | `orq-lite intake --issue <file>` |
| Run any flow explicitly | `orq-lite flow run <ref> key=value ...` |
| Compile without spending | `orq-lite flow validate <ref\|path>` |
| List local + installed flows | `orq-lite flow list` |
| Install/replace a pack | `orq-lite pack install [--force] <dir>` |
| See live runs | `orq-lite status [--watch]` |
| Inspect one run | `orq-lite flow status\|events <run-id>` |
| Continue / stop a run | `orq-lite flow resume\|cancel <run-id>` |
| Release a blocked run | `orq-lite flow approve <run-id> <approval-id> --decision approve` |
| Replay agent traffic | `orq-lite log [--role R] [--event T]` |
| Spend rollup | `orq-lite cost` |

`plan`, `run`, `factory`, `review`, `intake` are aliases into the built-in `development` pack — `factory` resolves to `factory-governed@2`, `plan` to `plan-tickets@1`, `run` to `task-list@1`, `review` to `pr-review@1`, `intake` to `issue-fix@1`. Prefer the explicit `flow run <pack>/<flow>@<v>` when you need a specific pack version pinned into the run.

## Choosing a flow

| Situation | Flow |
|---|---|
| Green-field or large spec, want governance | `factory-governed@2` (default; `fast=true` batches the backlog, `fast=false` runs one ticket per pass) |
| Just want the backlog decomposed | `plan-tickets@1` |
| Tickets already exist | `task-list@1` |
| Code already written, want it audited | `review-existing@1` |
| A PR needs a verdict | `pr-review@1` |
| A GitHub issue needs triage then work | `issue-fix@1` |

**Batch vs per-ticket is not a cost decision.** Measured on the same spec: batch $30.51 / 94 min, per-ticket $31.63 / 101 min — a wash, with identical output quality. Pick on **feedback locality**: per-ticket catches a defect against a small diff attributed to one ticket and re-scopes it through the planner; batch delivers findings after dozens of files are written, unattributable, with a repair loop capped for the whole change set. One long session also costs ~28% more than N short ones for the same work, because a growing prefix is re-read every turn.

## Runtime semantics that bite

Each of these was learned the expensive way. None is in `guide.md`.

**A failing `gate.run` aborts the entire flow.** `status=failed`, and the following step never executes. If you want a gate's result *as data* — to hand a reviewer the exit code instead of prose — use `activity:command.run@1` with `argv: ["sh","-c","<cmd>; echo EXIT=$?"]`, which always exits 0 and puts the real code in `stdout`. Do **not** "fix" a gate ordering by removing an `if:` guard; that guard is usually what keeps a red test from killing the run.

**`agent.invoke@1` is `EffectAtMostOnce`, so policy-level retry never applies to it.** The scheduler only retries Pure/Idempotent/Reconcilable effects. When the process dies mid-invocation the runtime cannot know whether the agent already wrote files, so it parks the run at `needs_human` with `activity outcome is unknown and cannot be retried safely`. Resolve with `flow approve` or `flow cancel` — `flow resume` alone returns `workflow needs human input`. A run that shows `running` in the store with no live process is this, not a hang.

**`maxDurationSeconds` counts wall-clock, not agent time.** A run left overnight on a sleeping laptop burns its budget doing nothing and dies with `workflow duration budget exhausted`. Wrap long runs in `caffeinate -i` (macOS) or raise the policy ceiling.

**`{{VAR}}` interpolation is a plain `strings.ReplaceAll`.** No conditionals, no error on unknown keys — a placeholder with no matching key is left **literally in the prompt**. When you add a `{{VAR}}` to a *shared* prompt, supply it at every call site, even as the string `"n/a"`. Extra unused keys are harmless.

**`outputSchema` must match what the prompt actually writes.** Reusing a schema that requires a field the prompt never emits makes every call fail validation and take the corrective retry — 9 wasted invocations in one real run, from reusing `ticket-implementation@1` (which requires `ticket_id`) for a role that implements no ticket. Write a purpose-built schema.

**Injected context is billed per turn, not per invocation.** `cache_read ≈ prefix size × turns`. Before adding a block to a shared prompt, multiply its size by the turns *and* by the number of roles that consume it. A 1,335-token memory note in three prompts made a run 19% more expensive with no measurable benefit.

**`rate_limit_pattern` must match what the provider actually says.** A throttle the pattern misses is classified permanent and aborts the run, surfacing as `agent.invoke permanent: all agents for role X are marked skipped` — which reads as a role misconfiguration and is not. Verify the regex against real strings, including `rate limit` with a space and `spend limit`.

**`results/by-task/` accumulates across runs.** The `.rN` collision suffix never resets, so one iteration directory can hold verdicts from many different runs and different tickets. Do not reconstruct history from it; read the per-run stream logs under `.orquestalite/runs/<run-id>/agents/`.

## Authoring or editing flow data

Read `authoring-flows.md` in this skill directory: the flow/subflow JSON shape,
the six activities and their effect classes, `if:`/`while:` controls, how to
`$ref` data between steps, prompt and schema contracts, pack layout and policy
ceilings — plus the three traps that make a valid-looking flow invisible or
unvalidatable.

The shipped `examples/governed-pack/pack/` is the recommended setup: roles
`ticket_planner`, `coder`/`batch_coder`, `ticket_qa` for ticket work, then `qa`,
`adversary`, `critic`, `integrator`, `gov_reviewer` in `integrated-review@1`.
Edit in place and regenerate digests — the project's convention is no version
bump for pack fixes.

## Red flags in a run

| Symptom | Cause |
|---|---|
| `agent.invoke permanent: all agents ... marked skipped` | usually a throttle the `rate_limit_pattern` missed, not a role problem |
| `status=needs_human` | `agent.invoke` died mid-call; `approve` or `cancel`, not `resume` |
| `workflow duration budget exhausted` with little agent time | wall-clock budget consumed while idle or asleep |
| A `{{VAR}}` visible in `runs/*/agents/*/prompt.md` | unsupplied key at that call site |
| `status=running` with no live process | zombie; check `flow status` before assuming progress |
| Result written but validation failed | `outputSchema` and the prompt's declared shape disagree |

Verify a claim about a run from `.orquestalite/runs/<run-id>/agents/*/*/stdout.log` — the agent's own summary is a claim, not evidence. And recompile before verifying anything against a binary: a stale binary has already produced a wrong conclusion in this project.
