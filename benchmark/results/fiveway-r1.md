# Five-way comparison — round 1 complete

Adds the **v3 ticketed run** (`taskflow-v3-canary`, run `r20260717T123326Z-dc1f`,
2026-07-17) to [`fourway-r1.md`](./fourway-r1.md). v3 = the per-ticket flow on the
durable runtime **after orquesta-lite fixes**, same qwen coder, and — for the first
time in the ticketed variant — a run that **fully converged**: 6 tickets, 2
integration-repair passes, global lint/tests/qa, whole-system critic, and
governance all completed and approved. Same evaluation pipeline as every other leg.

## Scoreboard — all five conditions

| | default (batch gov.) | hybrid (batch) | Sonnet 5 one-shot | ticketed v2 | **ticketed v3** |
|---|---|---|---|---|---|
| Completed + approved | ✅ | ❌ | ✅ (no reviewer) | work done, never approved | **✅ converged** |
| Checklist | 25/27 | 20/27 | 26/27 | 26/27 | 25/27 |
| Probe (identical) | 14/14 | 12/14 | 14/14 | 14/14 | **14/14** |
| **Confirmed bugs** | **0** | 2 | 3 | 2 | **4** |
| Correctness /60 | **57.78** | 46.79 | 52.89 | 54.89 | 49.78 |
| L (median of 3) | **0.770** | 0.510 | 0.600 | 0.590 | 0.550 |
| Pairwise vs default | — | 0W/7L | 1T/6L | 1T/6L | **0T/7L** |
| Wall-clock | 1h55m | 3h08m✗ | ~1–2h | 2h30m✗ | 1h37m† |
| Tracked cost | $25.46* | $9.11* | $15.15 | $39.16* | $12.53*† |
| **Composite (uncapped)** | **84.2** | 66.9 | 74.7 | 72.8 | **72.2** |
| **Composite (strict)** | **84.2** | 50.0 | 74.7 | 50.0 | **72.2** (no cap — converged) |

\*Floors (qwen/codex unpriced). †dc1f only; 3 failed/cancelled attempts the same
day add ≈$10.70 floor and may have left partial work the final run built on
(time/cost attribution caveat). Composites renormalized 5-way (best time = v3's
5,801 s; best tracked cost = hybrid's $9.11 floor).

## The uncomfortable headline: v3 converged — and shipped the most bugs

The 4 confirmed-by-reproduction bugs, all surviving the completed critic +
governance pass:

1. **Naive HTTP timestamps** — now 5-for-5 minus one: *every condition except the
   batch default shipped it.* v3 even added `DateTime(timezone=True)` (necessary
   but not sufficient with aiosqlite) and stopped there; no serializer, no test
   asserting the format.
2. **WS filtered-disconnect leak** — v3's `wait_for(…, timeout=1.0)` + `continue`
   bounds the *blocking* but still never touches the socket on non-matching
   traffic: subscription leaks forever under uvicorn. Reproduced.
3. **Duplicate `job.created` in prefect mode** — service publishes on POST and the
   poller re-detects the row next cycle. The exact bug the batch critics caught in
   BOTH r1 team runs; ticketed v2 avoided it by design; **v3 regressed it.**
4. **Fire-and-forget dispatcher tasks + `except Exception` missing
   `CancelledError`** — shutdown mid-flight leaves jobs stuck in `running`
   forever. v2 tracked and cancelled its tasks; **v3 regressed this too.**

Two of the four are *regressions relative to v2* — the unapproved snapshot was
safer than the approved one on those axes. And the review stack that approved v3
was substantial on paper: 6 per-ticket QA reviews, 2 integration repairs, global
QA, a critic pass, and governance.

## Why the default's zero still stands (the refined thesis)

Compare adversarial-review *dose*, not presence:

| | reviewer invocations over the build | reviewer spend |
|---|---|---|
| default (0 bugs) | 9 critic reviews (per-feature diffs) + 8 governance-role reviews over 2 rounds | ≈$20 |
| v3 (4 bugs) | 6 ticket-QA + 1 qa + **1 critic (478 s)** + 1 governance round | ≈$12.5, mostly ticket-scoped |

The only zero-bug build is the one where an adversarial reviewer with veto power
examined **every diff, repeatedly, at system scope**. A single 8-minute
whole-system critic pass at the end — even a legitimate, approving one — caught
none of the four. "Has a critic stage" is a checkbox; **review dose × scope is
the variable that predicts shipped bugs** in this round's data.

Meanwhile the per-ticket convergence result from v2 held up in v3: no retry
cycles were burned; qwen's tickets sailed through. Granularity buys convergence;
it demonstrably does not buy integration correctness.

## Judges (medians) and pairwise

v3: spec 3, correctness 2, architecture 3, tests 3, concurrency 2, code 3,
docs 4 → **L = 0.550** (lowest of the completed builds). Recurring judge notes:
flow tasks bypass the repository entirely (raw sessions), stats route skips the
service layer, 9 sequential COUNT queries, `_log_done` swallowing exceptions,
`time.sleep` synchronization in WS tests, and a README WS example importing a
library that isn't in the dependencies. Blind pairwise vs default: **default
7/7, both orders, no ties** — the only clean sweep of the round.

## Round-1 final ranking

```
1. default (batch governed, GPT-5.5 coder)   84.2   ← converged, 0 bugs
2. Sonnet 5 one-shot                          74.7   ← fastest coverage, 3 bugs
3. ticketed v2 (qwen)                         72.8*  ← *uncapped; strict 50 (infra abort)
4. ticketed v3 (qwen)                         72.2   ← converged, 4 bugs
5. hybrid batch (qwen)                        66.9*  ← *uncapped; strict 50 (real abort)
```

## Threats to validity (v3 leg)

- v3's tree accumulated across 3 failed/cancelled same-day attempts before the
  converged run — time, cost, and even authorship attribution are blurred.
- Bug-hunter aggressiveness varied across legs: v3's hunter reproduced the
  shutdown-cancellation bug that sonnet5's hunter left unconfirmed — sonnet5's
  count may be understated by 1 (its dispatcher is also fire-and-forget).
- All standing caveats: N=1 per condition, in-family judges, cost floors, the
  unfixable-as-written filtered-WS spec bullet (now 5-for-5 failures).
