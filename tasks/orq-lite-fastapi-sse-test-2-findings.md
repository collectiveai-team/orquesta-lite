# orq-lite end-to-end test #2 — findings (with manual pre-run fixes)

**Scenario.** Same FastAPI + SSE plan as test #1, but this time applying the
three fixes the first test surfaced *before* invoking `orq-lite run`:

1. `git init` + initial commit before scaffolding (fix for issue #1).
2. `team.json` → `full_test_command: "uv run pytest -q"` (fix for issue #2).
3. `team.json` → codex model `gpt-5` → **`gpt-5.5`** (fix for issue #3).

- Test dir: `/tmp/orq-test-2-fastapi-sse`
- Plan: identical to test #1 (copied verbatim).
- orq-lite version: `dev`.

## TL;DR

**All three fixes worked.** The orchestrator went from "all 10 tasks falsely
marked `failed`" in test #1 to **all 10 tasks correctly marked `done`** in
test #2, with a cleaner per-task git history and higher-quality code (Codex
took every coder slot and produced more idiomatic Python than Sonnet's
fallback work in test #1).

| Metric                         | Test #1 (no fixes)        | Test #2 (with fixes)     |
| ------------------------------ | ------------------------- | ------------------------ |
| Final status reported          | 10/10 `failed` ❌          | **10/10 `done` ✅**       |
| Tests passing                  | 4/4                       | **7/7**                  |
| App working                    | Yes                       | Yes                      |
| codex_gpt5 invocations         | 10 (all 400 errors)       | 10 (all exit 0)          |
| Total Claude invocations       | 32                        | **22** (−10)             |
| Total agent wall time          | 1346 s                    | 1248 s                   |
| Per-task git commits           | None (no repo)            | **11 clean commits**     |
| Review cycles run              | 2                         | 2                        |
| Layout deviation from plan     | `src/app/` (deviated)     | `app/` (matched plan)    |

## What the three fixes actually changed

### Fix 1: `git init` before scaffolding

Run #1's 8/10 `commit_rejected` failures vanished. The orchestrator made
**11 commits in the right shape** with no extra work:

```
6c51f8d feat(T010): Stop tracking __pycache__ in git
df1c9d5 feat(T009): Verify full acceptance criteria end-to-end
433a431 feat(T008): Write README.md
11b92c1 feat(T007): Write tests/test_events.py
f2e67ad feat(T006): Write tests/test_health.py
3f9064b feat(T005): Wire /events SSE endpoint
3e7980b feat(T004): Implement SSE event generator in app/events.py
7246f7e feat(T003): Implement /healthz endpoint
655fbf8 feat(T002): Create app package skeleton
12a0090 feat(T001): Initialize pyproject.toml with uv
b168662 initial scaffold
```

Confirms the diagnosis from test #1: orq-lite makes one commit per accepted
task. With no repo, every commit became a "failure"; the work was correct but
the bookkeeping was broken. **`init` should `git init` automatically** — this
is the single biggest UX cliff in the tool.

### Fix 2: `full_test_command` matched the project

T001 and T002 no longer failed with `full_suite_failed`. The full-suite hook
ran `uv run pytest -q` after each task and the result was treated as a
*warning* on early tasks (pre-tests) rather than as a hard fail. Worth
underlining: in run #1 the orchestrator marked T001 done-then-failed because
`go test ./...` didn't apply; in run #2 the same flow worked end-to-end.

Recommendation stands: **`init` should detect language** and pick a default,
or leave the field empty when ambiguous and skip the post-task hook.

### Fix 3: codex model `gpt-5` → `gpt-5.5`

This was the user's diagnostic insight, and it was correct. With `gpt-5.5`:

- **codex_gpt5 ran 10 times, all exit code 0** (vs 10/10 fails in test #1).
- Coder slot was taken by Codex on every task — the first item in
  `roles.coder.agents` is now actually usable.
- Claude was no longer hit as fallback for the coder role at all → **−10
  Claude invocations** vs run #1.
- Coder output was *better* on at least three measurable axes (see next
  section).

Note: I probed Codex with `gpt-5`, `gpt-5.1`, `gpt-5-codex`, and
`gpt-5-codex-mini` — **all four returned**
`"The '<model>' model is not supported when using Codex with a ChatGPT
account."`. Only `gpt-5.5` worked. The scaffolded default in
`orq-lite init` (`gpt-5`) is therefore broken for the most common Codex auth
mode and should be replaced.

## Quality delta: Codex (run #2) vs Sonnet fallback (run #1)

Same plan, same critic, same reviewer. With Codex actually doing the coding
the output improved in concrete ways:

| Aspect                         | Run #1 (Sonnet)            | Run #2 (Codex)                       |
| ------------------------------ | -------------------------- | ------------------------------------ |
| Project layout vs plan         | `src/app/` (deviated)      | `app/` (matched plan literally)      |
| `event_stream` signature       | Tightly coupled to FastAPI | `Protocol` for duck-typed request    |
| Cancellation handling          | None                       | Explicit `asyncio.CancelledError`    |
| Extra tests beyond plan        | None (4 tests total)       | Added `test_project_metadata`, `test_readme` (7 total) |
| Stray cleanup needed by review | Yes (`test_healthz.py`)    | Yes (`__pycache__` tracking)         |

Run #2's `events.py` is the more polished file:

```python
class DisconnectAwareRequest(Protocol):
    async def is_disconnected(self) -> bool: ...

async def tick_event_generator(
    request: DisconnectAwareRequest,
) -> AsyncIterator[dict[str, str]]:
    try:
        for count in range(1, 4):
            if await request.is_disconnected():
                break
            ...
    except asyncio.CancelledError:
        return
```

vs Run #1 which imported `Request` directly from FastAPI and had no
cancellation guard. Both work; Codex's matches the python.md rules
(`Protocol` for structural subtyping, structured exception handling) more
closely. The decision to put Codex first in `roles.coder.agents` is sound;
it just needs the right model name.

## What's still imperfect (new observations from run #2)

### N1. Reviewer still queues a hygiene task in cycle 2
Run #1's leftover was a duplicate `test_healthz.py`. Run #2's is a tracked
`__pycache__/`. Both are foot-guns introduced *during* execution because
neither the coder nor the per-task critic verifies "is the working tree
clean of incidental artefacts?". This is a real category — a one-line
`prompts/coder.md` addition ("never commit `__pycache__/`, `*.pyc`,
`.pytest_cache/`; ensure `.gitignore` covers Python build artefacts before
exit") would have caught it in cycle 1.

Note that orq-lite's *own* scaffolded `.gitignore` is 15 bytes — it
clearly doesn't cover Python. `init` could write a language-aware
`.gitignore` once it knows the language (fix #2 above).

### N2. Codex coder produced 2 extra tests outside the plan's acceptance criteria
The plan asked for `test_health.py` and `test_events.py`. Codex shipped
those plus `test_project_metadata.py` and `test_readme.py` (asserting the
pyproject and README contents). They pass and don't harm anything, but they
were not requested. Critic approved without flagging the scope expansion.

This is the inverse of run #1's `src/app/` deviation: where Sonnet over-
interpreted layout, Codex over-interpreted acceptance criteria. Neither
critic flagged the deviation. `prompts/critic.md` could include "the task
description is the contract — flag additions, not just defects".

### N3. The fact that one user-side edit (`gpt-5` → `gpt-5.5`) doubled output quality is itself a finding
If the scaffolded default agent doesn't work, the orchestrator silently
falls back to claude_sonnet for *every* coder turn. The user sees output
that is correct but not what the team config implied. There is no warning
on startup like "codex_gpt5 unreachable, falling back to claude_sonnet for
all coder turns". A 5-second preflight per agent during `orq-lite run` —
"can each declared agent return a result?" — would have caught both
issue #2 (wrong model) in the first second of the run, with a clear
actionable error.

## Recommendations, ranked

In priority order, given test #1 + test #2 evidence:

1. **`orq-lite init` should `git init` if not already a repo, write a
   language-aware `.gitignore`, and pick a viable `full_test_command`.**
   These three together unbreak the whole "looks failed but isn't" trap.
2. **Replace the default codex model `gpt-5` with `gpt-5.5`** in scaffolded
   `team.json`. Empirically the only working value on a ChatGPT-account
   Codex install.
3. **Add a 5-second per-agent preflight on `orq-lite run`** that calls each
   declared agent with a trivial prompt and fails loudly if any does not
   return. This catches stale model names, auth issues, missing CLIs, etc.
   before burning 20 minutes of Claude calls.
4. **`prompts/coder.md`: forbid committing `__pycache__/`, `*.pyc`,
   `.pytest_cache/`** and require `.gitignore` to cover them.
5. **`prompts/critic.md`: explicitly check for scope deviation** in both
   directions (missing requirements, AND files/tests not requested).
6. **`status` should distinguish "shipped but flagged" from "broken"** —
   today both render as `failed`. Run #1 made this column actively
   misleading; run #2 made it correct, but only because the fixes
   eliminated the ambiguity, not because the column got better.

## Verdict

Test #2 validates that test #1's issue list was diagnostic rather than
cosmetic: applying the three identified fixes once, manually, turned a
"green-but-shows-red" run into a clean green run with better code. The
orchestrator design is sound; the friction is entirely in `init` defaults
and the absence of an agent preflight.

---

# Changes to incorporate into orq-lite

Concrete, code-level work derived from tests #1 and #2. Each item lists the
file/area to touch, the change, and the acceptance criterion. Grouped by
priority. Tests #1 and #2 each take ~25 min to re-run end-to-end and are the
natural regression check for items C1–C4.

## P0 — fixes that turn a red run green

These four together close the "looks failed but isn't" gap. Doing only some
of them still leaves users with broken `status` output on a working app.

### C1. `orq-lite init` runs `git init` when not already a repo
- **File:** `cmd/orq-lite/init.go` (or wherever `init` lives — `internal/...`).
- **Change:** before writing `team.json`/scaffolding, check `git rev-parse
  --is-inside-work-tree`. If it fails, run `git init -q` in the target
  directory and make an empty initial commit (`--allow-empty -m "initial
  scaffold"`) so per-task commits have a parent.
- **Acceptance:** `orq-lite init /tmp/foo` in an empty dir produces `.git/`
  with one initial commit. Running it again in a pre-existing repo does
  not create a second one.
- **Alternative if init must stay pure:** make `run` treat absent-git as a
  non-blocking warning instead of marking every task `commit_rejected`.
  Pick one; the current behaviour is the worst of both worlds.

### C2. `orq-lite init` writes a language-aware `.gitignore`
- **File:** init's scaffolding asset list (today writes a 15-byte stub).
- **Change:** detect language by file presence:
  - `pyproject.toml` or `*.py` → Python `.gitignore` (`__pycache__/`,
    `*.pyc`, `.pytest_cache/`, `.venv/`, `.mypy_cache/`, `.ruff_cache/`).
  - `package.json` → Node (`node_modules/`, `dist/`, `.next/`).
  - `go.mod` → Go (`bin/`, `*.test`).
  - None of the above → keep current minimal stub.
- Always include `.orquestalite/results/` and `.orquestalite/run.log` at
  the top.
- **Acceptance:** test #2 cycle 2 created a hygiene task (T010) just to
  delete a tracked `__pycache__/`. After C2 that task should not exist.

### C3. `orq-lite init` picks a viable `full_test_command` (or leaves it empty)
- **File:** init template for `team.json`.
- **Change:** language detection drives the default:
  - Python → `"uv run pytest -q"` (or `"pytest -q"` if no `uv.lock`).
  - Node → `"npm test --silent"` or `"pnpm test"`.
  - Go → `"go test ./..."` (current default — only correct when there's a
    `go.mod`).
  - Ambiguous → `""` and skip the post-task full-suite hook entirely
    rather than running a guaranteed-failing default.
- Bonus: don't run `full_test_command` while *no tests exist yet* (e.g.
  pytest exit code 5). Treat "no tests collected" as success-with-warning
  during T001/T002 scaffolding.
- **Acceptance:** test #1 had T001 and T002 fail with
  `full_suite_failed`. After C3 those should pass.

### C4. Default codex model is `gpt-5.5`, not `gpt-5`
- **File:** init template for `team.json` → `agents.codex_gpt5.model`.
- **Change:** scaffolded value becomes `"gpt-5.5"`. Empirically the only
  Codex model that works on a ChatGPT-account install. Probed
  `gpt-5`, `gpt-5.1`, `gpt-5-codex`, `gpt-5-codex-mini` — all return
  HTTP 400 `"not supported when using Codex with a ChatGPT account"`.
- Rename the agent key from `codex_gpt5` to `codex_gpt55` (or just
  `codex`) so it doesn't lie about its model. Update the three role
  agent lists (`coder`, `tester`, `critic`, `reviewer`) accordingly.
- **Acceptance:** out-of-the-box `orq-lite init` + `plan` + `run` on a
  ChatGPT-Codex install gets `exit_code=0` from codex on the first
  invocation, not the 400 fallback.

## P1 — preflight and observability

### C5. Per-agent preflight at the start of `orq-lite run`
- **File:** `internal/.../run.go` (the run command's entry point).
- **Change:** for each agent referenced by any role, fire a 5-second
  trivial prompt (e.g. `"reply with: ok"`). If the call returns non-zero
  or no result, log a clear preflight error
  (`agent codex_gpt5 unreachable: <stderr>`) and refuse to start the
  loops. Add `--skip-preflight` for power users.
- **Acceptance:** if `team.json` declares a broken model, `orq-lite run`
  exits in <30s with a single actionable error, not after 25 minutes
  of silent fallbacks.

### C6. `orq-lite status` separates work outcome from infra outcome
- **File:** `internal/.../status.go` and the task schema in
  `.orquestalite/tasks.json`.
- **Change:** today `status` packs everything into one `STATUS` column
  with a free-text `REASON`. Split into:
  - `WORK`: `done | in_progress | pending | failed`.
  - `VERIFY`: `tests_pass | tests_fail | skipped | commit_skipped`.
  Existing values stay backwards-compatible; new column appears in
  default output.
- **Acceptance:** test #1's run would render as `done / commit_skipped`
  for tasks T003–T009 instead of `failed / commit_rejected`.

### C7. Persist agent-health within a run, optionally across runs
- **File:** new field on the in-memory agent registry; optional dump to
  `.orquestalite/agent_health.json`.
- **Change:** after N (e.g. 2) consecutive `result_missing` failures for
  the same agent in the same run, mark it skipped for the remainder of
  the run. Without this, test #1 paid ~10 seconds of wasted Codex calls
  even though every one of them was guaranteed to fail.
- Add `--reset-agent-health` to clear the persisted file.
- **Acceptance:** with a broken codex model, the second coder task
  should not even attempt the codex call.

## P2 — prompt-level guardrails (no Go changes required)

### C8. `prompts/coder.md` forbids committing build artefacts
- **Change:** add a "Do not commit" section listing `__pycache__/`,
  `*.pyc`, `.pytest_cache/`, `node_modules/`, `dist/`, `.next/`,
  `target/`, `bin/`. Require that the coder ensures `.gitignore` covers
  language-appropriate artefacts before completing.
- **Acceptance:** test #2's T010 (delete tracked `__pycache__/`) would
  not have been created.

### C9. `prompts/coder.md`: treat fenced layout in the plan as binding
- **Change:** add "If the plan contains a fenced project-layout block
  (```\n<dir tree>\n```), reproduce the directory structure exactly.
  Do not relocate files into `src/`, `lib/`, etc. unless the plan asks
  for it."
- **Acceptance:** test #1 produced `src/app/` despite the plan asking
  for `app/`. After C9, the layout should match.

### C10. `prompts/critic.md` checks scope deviation in both directions
- **Change:** today the critic mostly catches "missing requirement".
  Add an explicit step: "list files/tests in the diff that were not
  named in the task description. If any exist, flag them — additions
  outside the task contract are concerns to surface, not silently
  approve."
- **Acceptance:** test #2 added `test_project_metadata.py` and
  `test_readme.py` outside the plan, with no critic flag. After C10
  those would be called out.

### C11. `prompts/reviewer.md`: when `should_stop=true`, stop
- **Change:** the orchestrator should respect the reviewer's
  `should_stop`. Today test #1 set `should_stop=true` in cycle 1 (note
  about commit_rejected being infrastructural) yet cycle 2 ran anyway,
  hitting the same wall. Either honour it strictly, or make the
  reviewer's contract explicit ("`should_stop` is advisory; the
  orchestrator continues until no new tasks are queued AND no failed
  tasks remain").
- **Acceptance:** behaviour of `should_stop` is documented in
  `CONTEXT.md` and matches the orchestrator's actual logic.

## P3 — quality of life

### C12. `--log-format=human` for `run.log`
- **Change:** today `run.log` is one giant key=value line per agent
  call with truncated `stdout_tail`/`final_text_tail` that interleaves
  base64-ish blobs. Add a `human` mode that emits one line per role
  transition: `cycle 1 / T003 / coder=codex_gpt5 → done in 12s`. Keep
  the current rich format under `--log-format=json` for tooling.
- **Acceptance:** `grep "role=critic" run.log` in human mode shows a
  readable timeline.

### C13. `orq-lite status` shows current cycle and last agent
- **Change:** add columns `CYCLE` and `AGENT` so users can see which
  review cycle a task is in and which CLI ran the most recent attempt.
- **Acceptance:** during a long run, `orq-lite status` alone is enough
  to know what's happening without `tail -f run.log`.

### C14. `orq-lite init --lang <python|node|go|auto>`
- **Change:** explicit override for the language detection used by
  C2/C3, so users in mixed repos can force the right defaults.
- **Acceptance:** `orq-lite init /tmp/foo --lang python` writes a
  Python-flavoured `team.json` and `.gitignore` regardless of what
  files exist.

## Suggested implementation order

1. **C1 + C2 + C3 + C4** in one PR — they're all in the `init` codepath
   and together flip the default UX from broken to working. This is the
   single biggest win and the rest of the list builds on it.
2. **C5** (preflight) — small, high-value, prevents repeats of test #1's
   silent-fallback experience.
3. **C8 + C9 + C10 + C11** — prompt edits, no Go changes, can ship same
   day. Each cuts a known follow-up hygiene cycle.
4. **C6 + C7** — status column split + agent health persistence. Touches
   schema and CLI output; do after C1–C4 ship to avoid churning the
   migration twice.
5. **C12 + C13 + C14** — pure ergonomics, ship whenever.

## Regression tests these changes should add

- A fixture project `tests/fixtures/python-fastapi-sse/` with `plan.md`
  identical to the one used in tests #1 and #2.
- An end-to-end test that runs `orq-lite init` → `plan` → `run` against
  the fixture and asserts:
  - All tasks finish with WORK=done.
  - At least one commit per accepted task.
  - `.gitignore` covers `__pycache__/`.
  - `full_test_command` matches the project language.
  - Codex model in scaffolded `team.json` is reachable (or the agent is
    excluded if preflight fails).
- A negative test: `team.json` with a deliberately-broken model. `run`
  should fail in preflight inside 30 seconds, not after the first task.
