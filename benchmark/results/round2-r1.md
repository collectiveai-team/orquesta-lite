# Round 2 — scored evaluation (Hookrelay)

Full protocol run per [`../round2/evaluation.md`](../round2/evaluation.md) over
the three protocol conditions. Execution story and incident log:
[`round2-execution-report.md`](./round2-execution-report.md). The gov-loop leg
(qwen @ flow-v3) is a flow-design A/B, not a protocol condition, and is not
scored here.

## Final scoreboard

| | one-shot (superpowers, Sonnet 5) | ticketed-sonnet @v2 | ticketed-qwen @v2 |
|---|---|---|---|
| Converged | ✅ | ✅ governance approved | ❌ governance veto |
| Gates (fresh clone) | 10/10 | 10/10 | 10/10 |
| Checklist (30 items, by execution) | **30/30** | **30/30** | 29/30 |
| Probe v2.2 (frozen, 15 tests) | **15/15** | **15/15** | **15/15** |
| Confirmed behavioral bugs | 1 (universal race) | 1 (universal race) | 1 (universal race)* |
| Correctness /60 | 58 | 58 | 57 |
| eff (cost+time, normalized) | **1.000** | 0.287 | 0.168† |
| Q | **0.973** | 0.831 | 0.794 |
| L (median of 3 blinded judges) | 0.91 | **1.00** | 0.72 |
| Pairwise (stable, position-swapped) | beats A 5-0-2; **beats C 2-1-4** | beats A 7-0-0; loses to B 1-2-4 | — |
| **Composite (0.6Q + 0.4L)** | **94.8** | 89.8 | 76.4 uncapped / **50 capped** |

\*qwen's second confirmed bug (the inactive→dead crash) is already penalized
via its failed checklist item; counting it again (−2) would give uncapped 74.8
— capped result unchanged. Both readings reported; the no-double-count rule was
fixed before any scores were computed.
†qwen cost is a floor ($48.01 Claude roles; coder unpriced) — eff one-sided.

## The verdict: the one-shot wins round 2

**one-shot 94.8 · ticketed-sonnet 89.8 · ticketed-qwen 50 (capped).**

The 5-point margin decomposes exactly: efficiency contributes +8.6 composite
points to the one-shot ($8.84/25min vs ≈$38/~1h14m net), while the judges'
absolute quality contributes −3.6 (L 0.91 vs a **perfect 1.00** for
ticketed-sonnet — two of its three blinded sessions scored 5s on all seven
dimensions). Correctness is a dead tie (58/60 each, same single universal
bug).

**§3.5 disagreement flag (again):** the absolute protocol says ticketed-sonnet
is the better codebase (L 1.00 vs 0.91); the position-swapped pairwise says
the one-shot is (2 stable wins — architecture, code idiom — vs 1 for sonnet —
tests — with 4 ties). Per protocol this is flagged for human review, not
resolved by fiat. Round 1 ended with the same class of disagreement between
its finalists; this is now a benchmark-recurring phenomenon worth its own
analysis: *absolute rubrics reward completeness; head-to-head comparison
rewards structural discipline.*

## What changed vs round 1 (the headline for the post)

1. **The one-shot's quality gap closed.** Round 1: one-shot shipped 3
   confirmed bugs vs the governed winner's 0. Round 2: both shipped exactly
   the same single (universal) bug, tied the checklist at 30/30, tied the
   probe at 15/15 — and the one-shot did it 4× cheaper and 3× faster. The
   spec is the suspect variable: round 2's contract was written with the
   round-1 defect classes designed *in* as explicit or observable
   requirements, and a spec precise enough for reviewers to verify is also
   precise enough for a strong coder to satisfy unreviewed.
2. **The four round-1 bug families are all dead on arrival.** Naive
   timestamps: every implementation ships a UTCDateTime TypeDecorator (probe
   + hunters verified tz-aware output everywhere). SSE leak: all three release
   subscriptions on every disconnect path (observable via `feed_clients`).
   Duplicate deliveries: all three carry the spec'd UNIQUE constraint.
   Shutdown: all three revert `sending` and resume (probe-verified by killing
   the app mid-delivery). What the spec names, everyone builds.
3. **The new universal bug is what the spec didn't name**: a TOCTOU race on
   `POST /subscriptions` (no partial-unique constraint on active
   `target_url`; concurrent requests all read "no duplicate" then all
   insert: 5×201, quintuplicated deliveries). Reproduced identically in all
   three conditions; caught by zero reviewers across every condition —
   per-ticket QA, global QA, critic, governance, and superpowers self-review
   all missed it. **The round-1 thesis survives translation: review — of any
   dose — catches what someone asks about; the spec-blind class just moved
   one level up, from "did you handle timezones" to "did you handle
   concurrent writers."**
4. **qwen's veto is deterministically vindicated.** The governance blocker
   (inactive→dead crash + missing SSE frame, no covering test) is precisely
   the one checklist item qwen fails — confirmed by execution, independent of
   any judge. The fail-closed veto was a true positive, and (per the gov-loop
   A/B in the execution report) a 2-iteration repair loop converts it into
   legitimate convergence for ~$2.

## Deterministic detail

- **Probe:** pre-registered before any run (v2.0, hash frozen); two probe
  defects surfaced during evaluation — an unresolvable `Request` annotation
  that 422'd every delivery, and TestClient's inability to read infinite SSE
  streams — both fixed minimally, version-bumped (v2.1, v2.2, hashes
  recorded), and re-run against **all** conditions per §1.2. Final: 15/15
  everywhere. Defect log: `../round2/probe/PROBE_DEFECTS.md`. Lesson: the
  probe author is inside the experiment too.
- **Checklist:** one agent per condition, identical 30-item list, execution
  required. qwen's single miss is the veto bug (`_dispatch_one` crashes with
  KeyError on the `dead_inactive` claim; frame never published; DB row
  correct).
- **Standardized hunt:** same prompt, same model, run after all conditions
  finished. Evaluator-side cross-application: the subscription race found on
  one condition was re-reproduced by the evaluator on the other two (round-1
  shutdown-standard practice). Non-behavioral findings (universal full-table
  event matching; one vacuous `"T" in ts` assert in the one-shot's suite;
  one bare `sleep(0.3)` in qwen's) were routed to the judges, not the bug
  count.

## Judges (in-family — flagged)

All three conditions were built by Anthropic-family coders and judged by
Claude agents (blinded impl_A/B/C, random assignment, evidence required,
deterministic results binding, position swap). The in-family caveat applies
symmetrically to every leg this round, but an out-of-family re-judge remains
the standing pre-publication step.

Medians (A=qwen, B=one-shot, C=sonnet):

| dim (weight) | A | B | C |
|---|---|---|---|
| spec fidelity (25) | 4 | 5 | 5 |
| correctness (20) | 2 | 4 | 5 |
| architecture (15) | 4 | 5 | 5 |
| tests (15) | 4 | 4 | 5 |
| concurrency (10) | 4 | 5 | 5 |
| code idiom (10) | 4 | 4 | 5 |
| docs (5) | 4 | 5 | 5 |
| **L** | **0.72** | **0.91** | **1.00** |

Pairwise stable verdicts (winner only when both orders agree; flips = tie):

| | vs qwen | one-shot vs sonnet |
|---|---|---|
| one-shot | W spec, corr, arch, tests, code · T conc, docs | W architecture, code idiom |
| sonnet | W all 7 (clean sweep, both orders) | W test quality · T spec, corr, conc, docs |

Notable judge evidence: sonnet's `tests/test_conventions.py` (a meta-test
enforcing single-source-of-truth mechanically) and its hand-rolled ASGI
`_FeedSession` SSE driver; the one-shot's centralized `core/errors.py` +
app-level exception handlers and transaction-per-request session pattern;
qwen's services raising `HTTPException` (layer leak) and the untested
`dead_inactive` branch.

## Efficiency

| | one-shot | ticketed-sonnet | ticketed-qwen |
|---|---|---|---|
| Cost | $8.84 complete | ≈$37.99 complete (cache-aware est.) | $48.01 floor |
| Review share of cost | $0 | ≈78% | ≈93% of priced |
| Net productive time | 25m | ~1h14m | ~2h46m |
| Cost per checklist point | $0.29 | $1.27 | $1.66+ |

## §3.5 re-judge (evaluator review of the disagreement — 2026-07-19)

Performed directly by the evaluator (still Anthropic-family — this satisfies
the *human-review-of-disagreement* step, NOT the out-of-family re-judge, which
remains pending). Method: personally re-read the disputed evidence in both
blinded codebases rather than sampling more judge sessions.

**Every load-bearing pairwise claim against impl_C verified real:**

- `routes/feed.py:17-38` — the complete SSE generator (filter, poll,
  unsubscribe) is inlined in the route handler. Thoughtfully written (the
  comment explaining the Starlette disconnect race is the best line of code
  in the round), but a literal violation of "route handlers hold zero
  business logic".
- `dispatcher.py:124-127` — `except Exception: return` silently swallows any
  unexpected error in `_process`, stranding the delivery in `sending` until
  the shutdown revert; a second `except Exception: pass` guards `_publish`.
  The absolute judges cited this pattern as *positive* evidence ("prevents
  exception propagation") while scoring correctness 5 — that is rubric-anchor
  satisfaction, not code reading.
- Mixed commit ownership: the subscriptions repository commits internally
  (`subscriptions.py:10,42`) while events/deliveries repositories leave
  commits to callers — an inconsistency invisible to a checklist.

impl_B's counterpart flaws (production `assert`s at dispatcher.py:100-101,165;
one vacuous test assert; duplicated test helper) are also real but smaller in
architectural consequence, and its centralized `core/errors.py` + app-level
exception handlers + transaction-per-request session pattern are genuinely
the cleaner structure.

**Verdict:** the absolute L = 1.00 for ticketed-sonnet is inflated;
correcting the two dimensions the evidence contradicts (correctness 5→4,
architecture 5→4) gives L ≈ 0.93. Sensitivity: composite becomes one-shot
94.8 vs ticketed-sonnet 87.0 — **the ordering is robust in the direction the
pairwise indicated; the disagreement resolves in favor of the pairwise
protocol.** Published headline numbers keep the original medians (protocol),
with this review note attached. Meta-lesson for the benchmark: absolute
rubric sessions saturate on well-specified builds; position-swapped pairwise
retains discriminating power at the ceiling — weight accordingly in round 3.

## Threats to validity

- N=1 per condition; the 5-point one-shot margin is inside plausible
  run-to-run spread — treat the round-2 winner as "one-shot, provisionally".
- In-family judges (symmetric this round); pre-publication gemini re-judge
  pending, as in round 1.
- Deterministic ceiling: gates/checklist/probe no longer discriminate (three
  near-perfect builds). Round 3 needs a harder spec or adversarial checklist
  items, or the composite becomes judge-dominated.
- Probe defects were found and fixed mid-evaluation (documented, all
  conditions re-run identically) — the pre-registration guarantee held for
  test *intent*, not implementation.
- qwen cost floor; time nets are incident-adjusted approximations (host
  sleeps, timeout kills — see the execution report's incident table).
- The one-shot ran headless (`claude -p`) vs round 1's interactive session.

## Round-3 agenda (from the data)

1. Spec the concurrency contract explicitly (or don't — and make the TOCTOU
   class the next implicit-trap generation; either way, decide deliberately).
2. Add a partial-unique-index checklist item or probe test for concurrent
   writers.
3. Harder discrimination: either a bigger system, tighter perf bounds, or
   adversarial acceptance items the coder can't satisfy by reading carefully.
4. Ship the governance repair loop (validated by the A/B) as the default
   flow; A/B it against the fail-closed variant at N≥3.
5. Out-of-family judge before publishing L/pairwise numbers.
