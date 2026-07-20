# Round 3 — the improved flow, and the lesson that recurred against me

Round 3 built a new flow (`development@4`, `factory-governed@4`) that
generalizes the two-round pain points into **design**, not hardcoded checks,
and ran it against **both** specs (Taskflow from round 1, Hookrelay from round
2) with **Sonnet 5 as coder/integrator** and **Opus on every test/gate role**
(planner, ticket-QA, QA, adversary, critic, governance).

The headline is not the win I expected. It is the round-2 lesson —
*a reviewer that raises issues needs its findings to reach a fix path* —
**recurring one level up, against the person who wrote it.** The benchmark's
own evaluation caught it deterministically.

## What flow-v4 changed (design, not patches)

| Two-round pain point | v4 design change (generalized) |
|---|---|
| The universal bug each round is always *what the spec didn't name*; no reviewer asks new questions | **New role `adversary` (Opus)**: a falsifier that derives 5-8 failure hypotheses from the system's *shape* (shared state, concurrent writers, lifecycles, I/O boundaries) — explicitly NOT from the acceptance bullets — and must reproduce them against running code. Runs at system scope, before the critic. |
| Self-refereeing tests (`_naive_utc`, `try/except: pass`) | **Test-integrity audit** added to `ticket_qa` and the adversary: for each assertion, would it FAIL if the behavior regressed? |
| Write-only governance veto kills repairable runs | **Governance repair loop** (validated by the round-2 A/B): findings → integrator → gates → fresh re-audit, max 2 cycles, fail-closed intact. |
| The streaming ticket blew the coder timeout in both r2 coders | **Budget-sized tickets**: planner splits infra concerns (streaming, workers, lifecycle) from API surface; coder timeout 2400s. |

## The adversary works — as a detector

On Taskflow, with a prompt that names **no specific bug**, the adversary
(6.6 min, $2.02) found and **reproduced three blockers against running code**:

1. **The flow raises on a deleted job** — a *new* bug class, not in round 1's
   four families: `DELETE` a `pending` job → the fire-and-forget task hits
   `session.get → None` → unhandled `AttributeError` on the event loop. The
   spec says "the flow itself must not raise." Reproduced end-to-end.
2. **Naive timestamps** — *round 1's universal bug, caught by method*: it
   noticed `created_at` returns without an offset while the event stream's
   `ts` carries `+00:00`, and that the two public surfaces disagree.
3. **Process-global event bus** instead of `app.state` (spec says one bus on
   `app.state`): with two apps per process, A's events route to B's bus.

It also produced exactly the test-integrity findings the design intended:
"nothing deletes a *pending* job or asserts the flow survives a missing row
(hides #1)" and "`datetime.fromisoformat` accepts offset-less strings so no
test would catch #2." And it was epistemically honest — hypotheses it could
not reproduce (Prefect-mode duplicate events, task-GC shutdown races, delete
TOCTOU) went to the summary, *not* the findings, per the reproduction rule.

This is the role working exactly as designed.

## …but the findings never reached the fix path

`ls .orquestalite/results/` tells the story: there is **no `adversary.json`**.
The adversary wrote its 6-finding verdict to **`qa.json`** — my
`adversary.md` prompt, unlike every other pack prompt, never named its
checkpoint file, so the agent defaulted to the filename of the role it was
structurally closest to (QA), clobbering QA's real output in the process. The
role's `result_path` in the team file was `adversary.json`, so the durable
runtime looked there, found nothing, and captured the step's **`fallbackOutput`**:

> `{"approved": false, "findings": ["adversarial falsification did not produce
> a valid checkpoint..."], "summary": "adversary unavailable..."}`

That fallback — a content-free "the adversary didn't run" — is what fed the
critic, the integration-repair loop, and governance via `steps.adversary.output`.
**The three real reproduced bugs never propagated.** Verified against the
converged code:

| Adversary finding | Shipped in the approved build? |
|---|---|
| #1 delete-pending crash | **SHIPPED** — reproduced on the final tree: `AttributeError: 'NoneType' object has no attribute 'type'` unhandled on the loop |
| #2 naive timestamps | Fixed — `created_at` now returns `…Z` (an independent reviewer/coder caught it; not the adversary) |
| #3 process-global bus | **SHIPPED** — `app/events.py` still has `global _active_bus` |

Governance approved. The gov repair loop *did* fire (one cycle) — but on the
fallback signal, not the real findings, so it repaired nothing that mattered.

## The result

**Taskflow r3 converged and shipped ≥2 confirmed bugs** (the delete-pending
crash, reproduced; the global-bus defect, real but conditional on >1 app per
process) — **no better than the flow-v2 baseline**, because the one component
built to prevent exactly this was disconnected by a one-line prompt omission.

This is the most valuable result of the three rounds, precisely because it is
negative:

- **The round-2 thesis is now self-demonstrating.** "If a role can raise
  issues, its findings must reach a fix path" — I added a veto-capable
  detector role and *failed to verify its own output was wired to the repair
  path*. The exact write-only-reviewer failure mode, one level up. The
  fallbackOutput I wrote to be safe ("assume the worst if the adversary
  didn't run") silently absorbed a working adversary's real findings.
- **Detection and remediation are separately fallible.** The adversary's
  detection was excellent; the plumbing between detection and repair was the
  weak link — and nothing in the flow surfaced that the adversary's verdict
  was the fallback rather than a real 6-finding rejection.
- **The evaluation pipeline earned its keep again.** Governance approved; the
  independent by-execution checklist and the reproduction of the adversary's
  own findings against the final tree are what exposed the truth. Self-tests
  and in-flow reviews cannot referee themselves — a third time.

## The fix (applied) and what a corrected re-run would test

One line: `adversary.md` now instructs writing to
`.orquestalite/results/adversary.json` explicitly (and never qa.json); pack
digests rebuilt. The corrected pack is `benchmark/round3/pack-development-4/`.

A re-run would answer the real round-3 question that this run could not:
*with the adversary's findings actually reaching the integrator and a fresh
governance re-audit, does flow-v4 drive shipped bugs below the v2 baseline?*
The detector's quality (3-for-3 reproduced, including the universal bug by
method) suggests the ceiling is high — but that is now a hypothesis for round
3b, not a result.

## Round 3b — the corrected re-run (wiring fixed)

Rather than a full from-scratch re-run, I re-ran **only the review phase**
against the converged Taskflow tree: a `review-existing@4` flow calling the
same `integrated-review@3` subflow (adversary + gov repair loop) with the
**fixed** `adversary.md`. Run `r20260719T232314Z-730c`. This is a *corrected
continuation*, not a clean N=1 — the tree already had one review pass (so the
timestamp fix was already in) — but it isolates the exact question: *with the
adversary's findings actually reaching the integrator, does the loop fix
them?*

**The wiring fix worked.** `adversary.json` now exists (3 findings, approved:
false); the runtime captured the real verdict, not the fallback. The adversary
— a fresh, non-deterministic session — re-reported the delete-pending crash
(#1), a spurious-lifecycle-event facet of it (#2), and a new test weakness
(the `?job_id=` WS filter's positive path is never asserted, #3). It did *not*
re-flag the global-bus this pass (different session, different hypotheses).
The findings propagated to critic, integrator, and governance.

**And yet the load-bearing bug still shipped.** The integrator changed exactly
two files — `app/events.py` (the spurious-event facet #2) and `tests/test_ws.py`
(a new filter test, #3) — and **never touched `app/worker/flow.py`**, the core
of finding #1. Verified on the final tree: the delete-while-pending sequence
*still* raises unhandled `AttributeError` on the event loop (`execute` and
`finalize` both deref a `None` job). Governance approved anyway, on a single
pass, without re-running the adversary's reproduction. No regression test for
the missing-row path was added.

**The sharpened lesson (round-4 material):** wiring a reviewer's findings into
the repair loop is *necessary but not sufficient*. A finding that arrives
**with a reproduction** must have that reproduction promoted to a **blocking
gate** — a regression test that governance cannot approve past — or the loop
degrades into: plausible-partial-fix by the integrator + prose-reading
sign-off by governance, and the bug ships regardless. The reproduction is the
asset the adversary produces; leaving it as prose for a human-style reviewer
to weigh reintroduces exactly the self-refereeing failure the whole harness
exists to prevent. **The fix isn't more review — it's turning each reproduced
finding into a test the gates must run.** That is the concrete round-4 change:
an activity that materializes every adversary/critic reproduction into
`tests/` and fails the build until it passes.

Two-line summary of the arc: round 3a proved the detector works and I mis-wired
its output; round 3b proved that even wired correctly, a reproduced finding
without a gate is a suggestion, not a fix.

## Status and caveats

- **Taskflow r3**: converged (75 steps, governance approved), evaluated here.
  Gates green on fresh clone (46 tests, ruff clean). Run
  `r20260719T173533Z-f326`.
- **Hookrelay r3**: converged (82 steps, governance approved; run
  `r20260719T173529Z-f7a5`, gates green, 68 tests), on the **pre-fix** flow —
  and it confirms the 3a defect on a second spec, symmetrically. No
  `adversary.json`; the step captured the fallback; the adversary's real
  verdict (3 findings, first: the delivery client is opened and closed
  per-attempt, so a reused-closed-client `RuntimeError` escapes the
  `(TimeoutError, httpx.HTTPError)` catch) was misfiled to `qa.json` and never
  propagated. That real bug shipped. The round-2 universal TOCTOU
  subscription race also still ships (5×201 concurrent, reproduced) — the
  adversary didn't target it this non-deterministic pass, and would have been
  swallowed regardless. **Both specs, same story: the detector found a real
  bug by method, the wiring swallowed it, the bug shipped.** Artifacts:
  `round3-hookrelay-artifacts/`.
- **Round 3b re-run**: `review-existing@4` over the converged tree,
  `r20260719T232314Z-730c`; corrected `adversary.md`. The adversary took 11
  rate-limit retries overnight (30-min backoff spacing) before succeeding —
  wall-clock inflated, no bearing on the result. Artifacts:
  `round3-taskflow-artifacts/` (original run) + the re-run's `adversary.json`.
- Both runs hit coder infrastructure stalls (a session-limit death and a
  coder that hung on a background pytest) — durable-runtime resumes, no
  repeated work; documented, wall-clock affected only.
- N=1; in-family throughout; the delete-pending bug is a genuine spec
  violation ("the flow must not raise"), the global-bus bug is real but needs
  the multi-app precondition to bite.
