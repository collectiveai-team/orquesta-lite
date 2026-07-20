# Benchmark result — hybrid team, continued state (`bench/hybrid-final`)

Evaluated 2026-07-13 per [`evaluation.md`](../evaluation.md), same pipeline as
[`hybrid-r1.md`](./hybrid-r1.md). Branch `bench/hybrid-final` @ `82e7f8b`
(= `bench/hybrid-r1` + committed `features-remaining.md` + the WIP tree of the
aborted continuation run `r20260713T002727Z-5e08`). Evaluated on a fresh clone.

> ⚠️ **This is NOT a protocol-valid benchmark run.** It is a manually continued
> state (§1.2 forbids patch-and-continue), measured with the same yardstick to
> quantify the delta. For team-vs-team comparisons use `hybrid-r1` (or a clean
> hybrid r2) only.

## Verdict

**Still non-converged** — the continuation run also exhausted retries (critic
rejected the WS filtered-sequence *test* 4×; the tester additionally flagged the
timestamp validator's missing coverage) and F005 was never attempted. Composite
stays **capped at 50**; uncapped it improves **69.8 → 75.6**.

| | hybrid-r1 | hybrid-final | Δ |
|---|---|---|---|
| Composite (capped) | 50.0 | **50.0** | — |
| Composite (uncapped) | 69.8 | **75.6** | +5.8 |
| Q (quantitative) | 0.824 | **0.880** | +0.056 |
| L (qualitative, provisional) | 0.510 | **0.570** | +0.060 |

## Delivery and correctness (§2.1) — subtotal 51.02/60 (was 46.79)

| Metric | Score | Detail |
|---|---|---|
| Converged | **NO** | continuation run `r...5e08` ended `error` at feature[0] attempt 4; F005 never started; HEAD is a manual WIP commit |
| Acceptance checklist | 24.44/30 (22/27 = 81.5%) | F1 6/6 · F2 7/7 · F3 **5/5** (was 4/5 — consolidation into `test_worker.py` verified lossless, 11 tests) · F4 **4/5** (was 3/5 — route fixed; only the filtered-sequence test item still fails) · F5 0/4 |
| Gates at HEAD | 10/10 | ruff clean, **61 tests** pass on fresh clone |
| Independent probe | 8.57/10 (12/14) | identical probe file; both failures still = missing `/stats` |
| Confirmed post-hoc bugs | 8/10 (1 bug × −2) | both r1 bugs **fixed** (verified by re-running the original reproductions); 1 new test-only bug (below) |

**r1 bugs re-verified FIXED:**
1. HTTP timestamps now carry `Z` (field validator in `app/schemas.py:28-35`;
   naive/aware/None/string inputs all handled).
2. WS disconnect-under-filter: route restructured; `subscriber_count` → 0,
   zero "Task exception was never retrieved" under `PYTHONASYNCIODEBUG=1`.

**New confirmed bug (test-only):** `tests/test_ws.py:263` calls
`receive_text(timeout=0.1)` — Starlette's `WebSocketTestSession.receive_text()`
takes no `timeout` kwarg → instant `TypeError` swallowed by `except Exception:
break`. The disconnect-under-filter test exits in 0 ms and is **vacuous** (it
would pass with the r1 broken route too). Reproduced.

**Remaining checklist gaps:** F4 item 5 — the filtered-sequence test is a
weakened variant (membership asserts, no `job.created`, job POSTed before the
filtered WS connects); F5 entirely (stats 404, README 0 bytes, both test files
absent).

## Efficiency — cumulative across both hybrid runs

| Metric | r1 | continuation | total |
|---|---|---|---|
| Wall-clock | 11,296 s | 5,452 s | **16,748 s (4h39m)** |
| Tracked cost* | $9.11 | $3.18 (parser $0.46 + critic $2.73) | **$12.82** (+$0.53 false start) |
| Coder/tester attempts | F001-4: 2/2/3/4 | WS-hardening: 4 (exhausted) | 8 critic rejections on WS work total |
| Rate-limit stalls | 0 | 0 | 0 |

*Anthropic roles only — opencode/qwen still reports no `total_cost_usd`.

## Qualitative — LLM judge, L = 0.570 (PROVISIONAL, in-family caveat as in r1)

| Dimension | Weight | S1 | S2 | S3 | Median | r1 median |
|---|---|---|---|---|---|---|
| Spec fidelity | 25% | 2 | 2 | 2 | **2** | 2 |
| Correctness & robustness | 20% | 3 | 3 | 3 | **3** | 2 ↑ |
| Architecture & layering | 15% | 4 | 4 | 4 | **4** | 4 |
| Test quality | 15% | 2 | 3 | 2 | **2** | 2 |
| Concurrency & async | 10% | 4 | 4 | 4 | **4** | 3 ↑ |
| Code quality & idiom | 10% | 4 | 4 | 4 | **4** | 4 |
| Docs & DX | 5% | 1 | 1 | 1 | **1** | 1 |

L = 2.85/5 = **0.570**. Correctness and concurrency each gained a point on the
strength of the two verified fixes; spec fidelity, tests, and docs stay pinned
down by the absent F5 and the weak/vacuous WS tests.

Recurring judge findings worth fixing regardless of the benchmark: untracked
`asyncio.create_task` handles in both dispatchers (no shutdown cancellation
path); mutable module-level engine/session globals in `app/db/session.py`;
`[tool.ruff.lint]` select rules not configured (gates run on default rules only).

## Composite

```
Q = 0.8 × (51.02/60) + 0.2 × 1.0(provisional) = 0.880
L = 0.570 (provisional)
Composite = 0.6 × 0.880 + 0.4 × 0.570 = 0.756 → 75.6
Non-converged cap → FINAL: 50.0
```

## What's actually left to finish the project

1. One real filtered-sequence WS test (connect unfiltered → create job inside the
   WS context → assert full ordered sequence checking `job_id`), replacing the
   weakened variant.
2. Fix the vacuous disconnect test (`receive_text` has no `timeout` kwarg).
3. Coverage for the timestamp validator (tester's a4 blocker).
4. All of F005: `GET /stats` (SQL aggregates), README, `test_stats.py`, `test_e2e.py`.
