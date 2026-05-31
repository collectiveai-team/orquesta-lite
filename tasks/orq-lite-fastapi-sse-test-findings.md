# orq-lite end-to-end test — findings

**Scenario.** `orq-lite init` + `orq-lite plan` + `orq-lite run` against a
hand-written `plan.md` describing a Python FastAPI + SSE service.

- Test dir: `/tmp/orq-test-fastapi-sse`
- Plan: `/tmp/orq-test-fastapi-sse/plan.md`
- orq-lite version: `dev`
- Date: 2026-05-30 → 2026-05-31

## TL;DR

The orchestrator **produced a correct, working app** (5 tests, all pass; both
endpoints work as specified) but **`orq-lite status` reports all 10 tasks as
`failed`**. The "failure" reasons are environmental (no git repo, wrong default
test command) rather than functional defects. This is the headline UX problem:
a green run looks red.

```
T001 failed full_suite_failed   Initialize uv project and dependencies
T002 failed full_suite_failed   Create app package skeleton
T003 failed commit_rejected     Implement /healthz endpoint
T004 failed commit_rejected     Implement SSE event generator
T005 failed commit_rejected     Wire /events SSE endpoint
T006 failed commit_rejected     Write tests/test_health.py
T007 failed commit_rejected     Write tests/test_events.py
T008 failed commit_rejected     Write README.md
T009 failed commit_rejected     Final verification
T010 failed commit_rejected     Remove duplicate tests/test_healthz.py
```

Yet: `uv run pytest -q` → **4 passed**, `uvicorn app.main:app` → `/healthz` and
`/events` work, README is correct. Reviewer's own summary in cycle 2 confirms
this: *"All task statuses in state are 'failed' due to commit_rejected (no .git
repo) — this is an infrastructure issue, not a code defect."*

## Issues (ordered by impact)

### 1. `init` does not create a git repo, but `run` requires one
**Severity: high.** Tasks T003–T010 all failed with `commit_rejected`. Reviewer
notes this is *"because the working directory is not a git repo"*. The
reviewer's recommended fix: *"either `git init` the project or have the
orchestrator treat absent-git as a non-blocking warning rather than a task
failure."*

Either of:
- `orq-lite init` should `git init` the directory if it isn't already one
  (and create an initial commit so the per-task commits have a parent).
- Per-task commits should be a no-op when no git repo is detected (with a one-
  time warning), not a hard failure.

The current behaviour is the worst of both worlds: every task ships code, then
the orchestrator marks it `failed` and the user sees red. Even the *reviewer
agent* identified this and could not stop the cascade.

### 2. Scaffolded `team.json` defaults to `full_test_command: "go test ./..."`
**Severity: high.** orq-lite is implemented in Go but is used to *orchestrate
agents for arbitrary projects*. T001 and T002 both failed with
`full_suite_failed` — they ran `go test ./...` (presumably exit code 1 because
there is no `go.mod`), even though the actual project is Python. I patched
`team.json` to `uv run pytest -q` before T003, which is why later tasks failed
with a different reason instead.

Suggested fixes:
- Detect language during `init` (presence of `pyproject.toml`, `package.json`,
  `Cargo.toml`, etc.) and pick a sensible default.
- Better: prompt the user during `init`, or make `full_test_command` optional
  and skip the post-task full-suite run when absent.
- Even better: only run `full_test_command` when there is an existing test
  framework (e.g. don't run pytest before any tests exist — T001 created the
  project but had no tests yet; pytest exit code 5 = "no tests collected" is
  not really a regression).

### 3. `codex_gpt5` is in the default agent list but unusable with ChatGPT auth
**Severity: medium.** Every coder/tester/critic invocation began with a wasted
~1s call to `codex_gpt5`:

```
{"type":"error","status":400,"error":{"type":"invalid_request_error",
"message":"The 'gpt-5' model is not supported when using Codex with a ChatGPT
account."}}
```

In this run that happened **10 times** (every coder role attempt fell back to
`claude_sonnet`). Each call wasted ~1 second and bloated the log. The
orchestrator does *not* learn that `codex_gpt5` is unreachable for this user
and keeps trying for the next ~25 minutes.

Suggested fixes:
- After N consecutive `result_missing` failures for the same agent within a
  run, mark it skipped for the rest of the run (in-memory only).
- `init` could check Codex availability and either omit `codex_gpt5` from the
  default agent lists, or annotate the scaffolded `team.json` with a comment
  explaining how to switch model/account.

### 4. Reviewer correctly identifies the problem but cannot stop the cascade
**Severity: medium.** In cycle 1 the reviewer noted the `commit_rejected`
issue. In cycle 2 it noted it again, then set `should_stop: true`. Yet between
cycles the orchestrator continued spawning fix loops for tasks that were
already shipping correct code. This wastes loops on an environmental problem
the reviewer has already diagnosed.

Suggested fix: if the reviewer's `should_stop` is `true` AND the only
remaining failure reasons are infrastructure (`commit_rejected`,
`full_suite_failed`), end the run with a "completed with caveats" status
instead of looping again.

### 5. Coder deviated from plan's explicit layout (minor)
The plan asked for:
```
app/
  __init__.py
  main.py
  events.py
```
The coder produced `src/app/...` (and wired `pyproject.toml` accordingly).
Imports still resolve as `app.main`, so behaviour is correct, but the
literal file layout was overridden. The plan said *"Project layout"* in a
fenced block, which the agent treated as advisory rather than prescriptive.
Critic flagged it as a "nit" and approved.

This is a quality-of-output observation, not an orq-lite bug. The plan format
might want a stronger convention for "this layout is mandatory" vs "this is
illustrative". Possibly a section in `prompts/coder.md` that says "treat
fenced directory trees in the plan as binding".

### 6. Stray duplicate file required a second review cycle
After cycle 1 the coder had created BOTH `tests/test_health.py` AND
`tests/test_healthz.py` (the latter was a leftover from T003 where the agent
named its own ad-hoc test). The reviewer caught this and queued T010 in cycle
2 to delete it. Not a defect, but it shows the coder sometimes anticipates
later tasks (writing tests during T003 before T006 specified them), and the
de-duplication burden falls on the reviewer.

Suggested fix: `prompts/coder.md` could explicitly say "only do what this
task's acceptance criteria require; do not anticipate later tasks".

## Improvements / wishlist

These are not bugs — quality-of-life upgrades.

### A. `orq-lite status` should distinguish "failed but shipped" from "broken"
Today a single column shows `failed` for both `commit_rejected` (work shipped,
infra issue) and `full_suite_failed` (regression, real). Suggestion: separate
"task outcome" from "verification outcome", e.g.

```
T003 done  commit:skipped  /healthz endpoint
T009 done  tests:5/5       Final verification
```

### B. `init` should accept a language hint
`orq-lite init --lang python` would let `init` pick the right
`full_test_command` and seed a sensible `.gitignore` (currently the scaffolded
`.gitignore` is 15 bytes — likely just `.orquestalite/`). Right now I had to
hand-edit `team.json` between `plan` and `run`.

### C. Persist agent unavailability inside `.orquestalite/state.json`
So the next `run` does not re-discover that `codex_gpt5` is unreachable. Pair
with a `--reset-agent-health` flag.

### D. Plan parser is excellent — keep
The parser produced 9 well-sized atomic tasks with explicit acceptance
criteria from a fairly informal markdown plan, in 33 seconds. The decomposition
quality was the strongest part of the run.

### E. Log readability
`run.log` is JSON-ish key/value with truncated `stdout_tail` lines that are
hard to grep. A `--log-format=json` for machine use plus a `--log-format=human`
default (one line per role transition, no truncated payloads) would help
debugging. Today `grep "role=critic"` shows the full base64-ish blob.

### F. `status` could show review cycle and which agent ran
Knowing T005 was retried by `claude_sonnet` after `codex_gpt5` fallback, in
cycle 2, would help a user diagnose why a task is taking so long without
tailing `run.log`.

## Run metrics

- Total agent invocations: **42** (32 Claude + 10 Codex-fallback).
- Total agent wall time (sum of `duration_s`): **1346 s** (~22 min). Real
  wall clock is longer because of an 8-hour laptop sleep mid-run; orq-lite
  resumed cleanly after wake.
- Review cycles run: **2** (limit was 3; reviewer voluntarily stopped).
- Tasks parsed from plan: **9**. Reviewer added **1** follow-up (T010).
- Final functional state: ✅ 4/4 pytest pass, both endpoints work, README
  accurate.
- Final reported state: ❌ 10/10 tasks marked `failed`.

## Verbatim reviewer quote that captures the headline bug

> "All task statuses in state are 'failed' due to commit_rejected (no .git
> repo in /private/tmp/orq-test-fastapi-sse) — this is an infrastructure
> issue, not a code defect, and matches the cycle-1 reviewer note. No new
> functional gaps observed; nothing pending in plan.md beyond what's shipped."

If the orchestrator's own reviewer says the deliverables are complete and
correct, `status` should not show them all as `failed`.
