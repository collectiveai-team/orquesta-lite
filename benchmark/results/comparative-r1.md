# Benchmark comparative — default vs hybrid, round 1

Per [`evaluation.md`](../evaluation.md), evaluated 2026-07-13. Runs compared:
`bench/default-r1` @ `4c9539d` (run `r20260713T121622Z-5bfb`) vs `bench/hybrid-r1`
@ `a7d2685` (run `r20260712T191207Z-986c`, the protocol-valid hybrid run — the
manually-continued `bench/hybrid-final` is informational only, see
[`hybrid-final.md`](./hybrid-final.md)). Both evaluated on fresh clones with the
identical probe (`benchmark/probe/test_probe.py`), identical checklist items, and
the same judge pipeline.

## Winner: **default team, by a wide margin**

```
Composite (0.6·Q + 0.4·L):   default 85.2   vs   hybrid 50.0 (capped; 67.5 uncapped)
Margin: 35.2 points capped / 17.7 uncapped
```

The margin is dominated by the convergence gate: the default run **converged**
(all four governance approvals, round 2 of 4) while the hybrid run aborted at
F004 and never built F005. But the default also wins every underlying metric
independently of the cap. N=1 per team — within-team spread is unknown; the §4
"margin exceeds spread" check cannot be evaluated until r2/r3 runs exist. Given
the margin's size and that every deterministic metric points the same way, it is
unlikely to be pure run-variance, but treat the *magnitude* as provisional.

## Score sheet (§4)

| Section | Metric | default | hybrid |
|---|---|---|---|
| Delivery | converged | **yes** (gov round 2/4) | **no** (F004 retries exhausted, F005 never attempted) |
| | fix attempts per feature | F1:1 F2:1 F3:3 F4:2 F5:2 | F1:2 F2:2 F3:3 F4:4✗ |
| Correctness | checklist | **25/27 (92.6%)** | 20/27 (74.1%) |
| | gates at HEAD | pass (66 tests) | pass (59 tests) |
| | independent probe | **14/14** | 12/14 |
| | confirmed bugs | **0** | 2 |
| | correctness subtotal | **57.78/60** | 46.79/60 |
| Efficiency | wall-clock (0 rate-limit stall both) | **6,898 s (1h55m)** | 11,296 s (3h08m) |
| | tracked cost* | $25.46 | $9.11 |
| | tokens in/out | 32.3M / 353k | 27.5M / 266k |
| | cost per checklist point* | $1.02 | $0.46 |
| | eff (normalized §2.2) | 0.679 | 0.805 |
| | **Q** | **0.906** | 0.785 |
| Judge (median of 3) | spec / corr / arch / tests / conc / code / docs | **4 / 4 / 4 / 3 / 4 / 4 / 4** | 2 / 2 / 4 / 2 / 3 / 4 / 1 |
| | **L** | **0.770** | 0.510 |
| Pairwise (blind) | win-rate | **7/7 for default** (one order only — see caveats) | 0/7 |
| **Composite** | | **85.2** | **50.0** (67.5 uncapped) |

*Cost caveat below — the bases are asymmetric.

## Confirmed-bug list

- **default: none.** The adversarial hunt closed every item on the hunt list with
  a green reproduction. Its coder pre-empted the exact failure modes that sank the
  hybrid: a `field_serializer` reattaches UTC on every timestamp (vs hybrid's
  naive-datetime bug), the WS handler cancels both racing tasks in a `finally`
  (vs hybrid's `continue`-skips-disconnect bug), and the inline dispatcher tracks
  tasks in a set with `shutdown()` cancellation (the hybrid's open suspicion).
  Unconfirmed smells only: sync `mkdir` in the async lifespan; one
  timing-dependent test; poller-mode events skipped for sub-interval jobs.
- **hybrid: 2** (both reproduced, both since fixed on the non-scored
  `bench/hybrid-final`): naive HTTP timestamps; `ws.py:39` disconnect skip.

## Where the qualitative gap comes from (three largest, per judges)

1. **Docs & DX (4 vs 1):** default shipped a complete, accurate README (verified
   quickstart, 8-route table, websocat example, gates); hybrid's README is 0 bytes.
2. **Spec fidelity (4 vs 2):** hybrid is missing feature 5's entire surface
   (`/stats` → 404) plus contract-level drift; default's only misses are two
   test-organization items.
3. **Correctness & robustness (4 vs 2):** hybrid had two reproduced contract
   bugs; default had none, and handles edge paths the spec doesn't even require
   (job deleted mid-flow, graceful early returns).

## Cost-by-role: does Opus-only-on-orchestration shift spend?

| Role group | default | hybrid r1 |
|---|---|---|
| coder | **unpriced** (codex, 11 inv, 15.3M in / 139k out) | **unpriced** (qwen, 11 inv, 17.4M in / 114k out) |
| tester | $2.30 (Sonnet, 9 inv) | **unpriced** (qwen, 11 inv) |
| critic | $12.70 (Opus, 9 inv) | $6.99 (Sonnet, 11 inv) |
| parser | $2.89 (Opus) | $2.12 (Opus) |
| governance (arch+qa+pm+gov_rev) | $7.58 (8 inv) | $0 (never reached) |
| **tracked total** | **$25.46** | **$9.11** |

The dollar comparison is **not apples-to-apples**: neither coder reports cost,
and the hybrid's tester is also unpriced while the default's (Sonnet) is priced.
On the priced-both-sides subset (parser + critic), default spent $15.59 vs
hybrid's $9.11 — the Opus critic costs ~1.8× the Sonnet critic, but it converged
every feature in ≤3 attempts and the branch shipped zero confirmed bugs, whereas
the cheaper critic loop burned 8 rejections against the qwen coder without
converging. The hybrid's headline "cheaper" is also survivorship of failure: it
never paid for governance because it never got there. Efficiency (`eff`) still
formally favors the hybrid (0.805 vs 0.679) because the protocol normalizes cost
to the cheaper *tracked* run — treat that subscore as unreliable until orq-lite
prices codex/opencode runs (or they're priced from token counts).

## Threats to validity for this round

- **N=1 per team** — margin magnitude is provisional; the direction is supported
  by every deterministic metric agreeing.
- **In-family judge** — all judge sessions (absolute and pairwise) ran on Claude;
  the protocol demands an out-of-family judge (`gemini-3-pro`) since both teams
  contain Anthropic models. Deterministic results anchor most of the composite
  (and the pairwise verdicts cite them), but re-judge before publishing.
- **Position swap incomplete** — the (A,B)-order pairwise session died on a
  session limit; the 7/7 win-rate comes from the (B,A) order only. Re-run the
  swapped session (after the limit resets) to confirm; verdicts that flip get
  recorded as ties. Given the deterministic gaps, flips are unlikely but the
  protocol requires the check.
- **Cost asymmetry** — see above; `eff` and cost-per-point are not comparable
  across teams this round.
- **Probe timing** — the probe was authored after the hybrid run but before the
  default run, and applied byte-identically to both. It contains nothing derived
  from either implementation (spec-only), but strictly it wasn't "written before
  both runs".
- **Spec ambiguity (§5)** — both teams independently violated the same literal
  bullet: worker tests in `tests/test_worker.py` (hybrid put them in
  `test_flow.py`/`test_dispatch_integration.py`, default in
  `test_worker_flow.py`/`test_worker_entrypoint.py`). Scored as fail for both,
  symmetrically. Consider relaxing the filename bullet (or stating it's literal)
  in `features.md` before round 2. Same for the session-scoped
  `prefect_test_harness` fixture bullet, which default also organized differently.

## Field note for orq-lite

The decisive difference was coder↔critic convergence, not raw code quality per
invocation: GPT-5.5 absorbed critic feedback in ≤2 follow-ups every time, while
qwen3.7 looped 4× on the same WS feedback twice (r1 and the continuation). The
per-feature batch design amplifies this — one unresolved task burns whole-batch
attempts (see the flow-granularity discussion). If a hybrid r2 is attempted, the
cheapest interventions are: per-task retry granularity, or letting the critic
propose the exact patch after attempt 2.
