# Four-way comparison — default vs hybrid vs Sonnet 5 one-shot vs ticketed

Extends [`threeway-r1.md`](./threeway-r1.md) with a fourth condition: the
**per-ticket governed flow** (`factory-governed@2` on the durable workflow
runtime) — `ticket_planner`/`ticket_qa` (Opus) around a **qwen3.7 coder**, i.e.
the same coder that failed the batch flow, retried at finer review granularity.
Run `r20260716T125757Z-e724` in `taskflow-v2-canary` (2026-07-16). Evaluated on
snapshot `fe1564f` (the run's uncommitted working tree; the repo itself was left
untouched and resumable). Same pipeline: identical probe (v1.2 — a
fixture-compat fix, regression-verified score-identical on the prior three
clones), same checklist items, same bug-hunt list, 3 absolute judges, blind
pairwise vs default with position swap.

> ⚠️ The ticketed run was **aborted by a provider session limit at the final
> critic stage** — after all 9 tickets were approved and global lint/tests/qa
> passed, but before the final adversarial review ran. Strictly per §2.1 it is
> non-converged (composite capped at 50); unlike the hybrid abort, the work was
> finished and the abort was pure infrastructure. A resume of the durable
> workflow could still legitimize it.

## Scoreboard (all four)

| | default (batch gov.) | hybrid (batch gov.) | Sonnet 5 one-shot | **ticketed (qwen)** |
|---|---|---|---|---|
| Completed the build | ✅ | ❌ died at F004 | ✅ | ✅ (final critic never ran) |
| Checklist | 25/27 | 20/27 | **26/27** | **26/27** |
| Probe (identical) | **14/14** | 12/14 | **14/14** | **14/14** |
| Confirmed bugs | **0** | 2 | 3 | 2 |
| Correctness subtotal /60 | **57.78** | 46.79 | 52.89 | 54.89 |
| Judge L (median of 3, provisional) | **0.770** | 0.510 | 0.600 | 0.590 |
| Pairwise vs default (swapped) | — | 0W/7L | 6L/1T | 6L/1T |
| Wall-clock | 1h55m | 3h08m✗ | ~1h–2h (see 3-way) | 2h30m |
| Tracked cost | $25.46* | $9.11* | $15.15 (complete) | **$39.16*** |
| eff (4-way normalized) | 0.679 | 0.805 | 0.771 | 0.500 |
| Q | **0.906** | 0.785 | 0.859† | 0.832 |
| **Composite (uncapped)** | **85.2** | 67.5 | 75.6 | **73.5** |
| **Composite (strict §2.1)** | **85.2** | 50.0 | 75.6 | 50.0‡ |

\*Floors (qwen/codex coders unpriced; hybrid tester unpriced). †Sonnet 5 Q
recomputed under 4-way eff normalization stays ≈0.86; composite unchanged to one
decimal. ‡Infra-caused: the abort was a provider limit, not exhausted retries —
flagged per §5 provider-conditions; the uncapped figure is the informative one.

## The headline finding: granularity rescued convergence

The forensic centerpiece: **the same qwen3.7 coder that accumulated 8 critic
rejections on batch-scoped work converged first-attempt on all 9 tickets** —
9/9 `ticket_qa` (Opus) approvals, zero rejections, with substantive reviews
(exact error bodies, timezone checks, bounded-deadline polling, one review even
flagged a test polluting the repo). The variable that changed was not the model,
the prompts, or the feedback plumbing — it was **the size of the unit under
review**. This overturns the simplest reading of round 1 ("qwen can't absorb
feedback"): scope width was doing most of the damage.

## The counterweight: local review ≠ global review

The ticketed build still shipped **2 confirmed bugs — the same two classes as
the one-shot**:

1. **Naive HTTP timestamps** (4th appearance across conditions: hybrid, one-shot,
   ticketed all shipped it; only the batch default's critic forced the fix).
   Aggravating detail: the ticketed tests include a `_naive_utc()` helper that
   strips tzinfo before asserting — the defect was *seen and worked around in
   tests* rather than fixed.
2. **WS filtered-disconnect subscription leak** — here in its purest form: the
   handler has no disconnect detection at all (`queue.get()` + send-only
   `WebSocketDisconnect`), leaking until process shutdown; masked by TestClient
   cancellation semantics.

Meanwhile the ticket-scoped QA **did** catch the in-scope bug the one-shot
shipped: the stats math uses fractional `julianday` (0.3 s + 0.7 s → 0.5,
verified) because a ticket review exercised exactly that path. The pattern is
clean: **per-ticket review catches per-ticket bugs; cross-cutting integration
bugs need a whole-system adversarial pass** — precisely the stage the provider
limit killed. (Fairness note: because that final critic never ran, this run is
*not* evidence that the ticketed flow fails to catch these — it's evidence the
tickets alone don't.)

Judges also surfaced a structural cost of ticket-scoped work: **architecture
median 3** (vs 4 for all three other conditions) — constants re-declared across
modules (`VALID_STATUSES` in a route file, deployment name as a bare literal,
version hardcoded), duplicated DI factories, `HTTPException` raised from the
service layer. Each ticket was locally clean; nobody owned global consistency.
Local optimization, global drift.

## Efficiency

Per-ticket review is expensive: $12.10 of ticket planning + $23.67 of ticket QA
(9 Opus reviews) — the review overhead alone exceeds the default run's entire
tracked cost, for a build that still needed (and never got) the final
adversarial pass. 2h30m wall vs 1h55m for batch default. Fix attempts tell the
story differently though: 0 retry cycles burned vs the default's 5 and the
hybrid's 8+ — the money moved from retries to reviews.

## Updated conclusions for the post

1. *(unchanged)* Spec coverage is cheap; the one-shot and ticketed runs tied for
   the best checklist (26/27).
2. *(refined)* **Coder↔critic convergence is a function of scope size, not just
   model capability.** Ticket-sized units turned an 8-rejection death spiral
   into 9/9 first-attempt approvals with the same coder.
3. *(sharpened)* **Review granularity determines which bugs you catch.**
   Ticket-scoped QA caught the in-scope math bug; only the batch default's
   whole-system critic caught the cross-cutting ones (timestamps, WS lifecycle,
   task tracking). Four conditions, one invariant: **every build without a
   completed whole-system adversarial pass shipped the naive-timestamp bug.**
4. *(unchanged)* Self-tests can't referee themselves — `_naive_utc()` is the
   most explicit case yet: a test helper written *around* a known defect.
5. *(unchanged)* The filtered-WS-sequence spec bullet defeated all four
   conditions. Fix the spec.

## Threats to validity (this leg)

- Non-converged by infra, evaluated from an uncommitted working-tree snapshot;
  a resumed run could differ (final critic + fixes).
- N=1, in-family judges (worst here and in the one-shot leg: Claude judging
  runs whose reviewers were Claude), cost floors — all as in prior legs.
- The ticketed condition changes two things vs batch hybrid (granularity AND
  the reviewer roles/runtime), so it isn't a pure granularity isolation either.
- Probe v1.2 fixture fix applied mid-round; regression-verified identical
  results on all prior branches before use.
