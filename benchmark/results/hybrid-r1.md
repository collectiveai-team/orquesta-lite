# Benchmark result — hybrid team, run 1 (`bench/hybrid-r1`)

Evaluated 2026-07-12 per [`evaluation.md`](../evaluation.md). Repo: `../benchmark-orq-lite`,
branch `bench/hybrid-r1`, run `r20260712T191207Z-986c`. Evaluation performed on a fresh
clone of the branch.

## Verdict

**Non-converged.** The flow aborted during F004 (WebSocket stream): the critic rejected
all 4 coder attempts (`retry_until` exhausted, `run_end status=error` after 3h08m).
F005 (stats + e2e + README) was never attempted. Per §2.1 the composite is
**capped at 50** — uncapped it would have scored **69.8/100**.

| | value |
|---|---|
| Composite (capped, non-converged) | **50.0** |
| Composite (uncapped, informational) | 69.8 |
| Quantitative index Q | 0.824 (0.780 correctness-only) |
| Qualitative index L (provisional — see caveats) | 0.510 |

## Delivery and correctness (§2.1) — subtotal 46.79/60

| Metric | Score | Detail |
|---|---|---|
| Converged | **NO** | F004 attempt 4 rejected by critic; F005 never started. HEAD includes a manual commit ("last commit hybrid") of the unfinished F004 tree — a §1.2 hygiene violation, noted but the tree was evaluated as-is |
| Acceptance checklist | 22.22/30 (20/27 = 74.1%) | F1 6/6 · F2 7/7 · F3 4/5 · F4 3/5 · F5 0/4 |
| Gates at HEAD | 10/10 | `ruff check` clean, 59 tests pass on fresh clone |
| Independent probe | 8.57/10 (12/14) | both failures = missing `GET /stats` |
| Confirmed post-hoc bugs | 6/10 (2 bugs × −2) | see below |

**Checklist failures:** F3: required worker tests exist but in `test_flow.py` /
`test_dispatch_integration.py` instead of the spec-mandated `tests/test_worker.py`.
F4: WS disconnect handling defective (`app/routes/ws.py:39`); `test_ws.py` missing the
filtered-sequence test and any disconnect-under-filter test. F5: `GET /stats` → 404,
`README.md` is 0 bytes, `tests/test_stats.py` and `tests/test_e2e.py` absent.

**Confirmed bugs (reproduced):**
1. **Naive HTTP timestamps** — `DateTime(timezone=True)` + aiosqlite drops `tzinfo` on
   the read path; every `JobResponse` serializes `created_at/started_at/finished_at`
   without a timezone designator (e.g. `2026-07-12T23:29:32.054087`), violating the
   RFC 3339/UTC convention. WS events (hand-rolled `utc_rfc3339()`) are correct; only
   HTTP is broken. Fix direction: `pydantic.AwareDatetime` or a field validator.
2. **`ws.py:39` `continue` skips the disconnect check** — when a filtered-out event and
   a client disconnect land in the same `asyncio.wait` result, the completed receive
   task's `WebSocketDisconnect` is never retrieved ("Task exception was never
   retrieved" logged) and cleanup is deferred a full iteration. This is exactly the
   blocker the run's own critic kept rejecting — the internal review signal was correct.

Not double-counted: the bug-hunt's third finding (F005 entirely absent) is already
penalized in the checklist. Unconfirmed suspicions (logged, no repro): untracked
dispatcher tasks at shutdown; DELETE status-check TOCTOU; poller emitting
`job.created` with non-pending status.

**Independent probe:** `benchmark/probe/test_probe.py` (14 black-box tests: 422/404/409
bodies, exact result math, failed-job contract, WS order + filter, list ordering,
stats math/null). ⚠️ Written *after* this run (protocol wants it authored before both
runs) — it must be applied **verbatim** to the default-team branch.

## Efficiency (§2.2) — reported; eff provisionally 1.0 (single run, nothing to normalize against)

| Metric | Value |
|---|---|
| Wall-clock | 11,296 s (3h08m), 0 s rate-limit stall (no `rate_limited` events) |
| Tracked cost | **$9.11** — parser (Opus 4.8) $2.12, critic (Sonnet 4.6) $6.99. ⚠️ coder/tester ran `opencode-go/qwen3.7-plus`, which reports **no cost** in the log → true total unknown |
| Tokens (main run) | 27.46 M in (≈99% cached) / 266 k out over 37 invocations |
| Invocations by role | coder 11 · tester 11 · critic 11 · parser 4 (architect/pm/qa/governance never reached) |
| Fix attempts per feature | F001: 2 · F002: 2 · F003: 3 · F004: 4 (exhausted) |
| Governance rounds | 0 of 3 (never reached) |
| Cost per accepted checklist point | ≥ $0.46/pt ($9.11 ÷ 20, lower bound — coder cost unreported) |
| Incidents | Two false-start runs same evening (one interrupted at 653 s, +$0.53; one immediate error). No provider outage observed |

Note: the coder/tester model actually used was `qwen3.7-plus` via opencode, not the
`qwen3.6-coder` id written in `evaluation.md` — update the doc or the team file so the
report matches reality.

## Qualitative — LLM judge (§3), L = 0.510 (PROVISIONAL)

⚠️ **Protocol deviation:** §3.1 requires a judge outside both teams' model families
(e.g. `gemini-3-pro`). These 3 sessions were run by Claude (Anthropic is in both
teams) → self-preference bias is possible; re-run with an out-of-family judge before
publishing any cross-team comparison. Blinding was also moot (single implementation,
evaluator knows which). Deterministic results were injected and no session
contradicted them; every score carries `file:line` evidence.

| Dimension | Weight | S1 | S2 | S3 | Median |
|---|---|---|---|---|---|
| Spec fidelity | 25% | 2 | 2 | 2 | **2** |
| Correctness & robustness | 20% | 2 | 2 | 2 | **2** |
| Architecture & layering | 15% | 4 | 3 | 4 | **4** |
| Test quality | 15% | 3 | 2 | 2 | **2** |
| Concurrency & async | 10% | 3 | 3 | 3 | **3** |
| Code quality & idiom | 10% | 4 | 4 | 4 | **4** |
| Docs & DX | 5% | 1 | 1 | 1 | **1** |

L = Σ(w·median)/5 = 2.55/5 = **0.510**

Judge consensus, largest gaps: what qwen3.7 built is architecturally clean and
idiomatic (proper routes→service→repo, Protocol dispatcher, bounded drop-oldest
EventBus, SQLAlchemy 2/Pydantic v2 throughout — both 4s), but spec-*literal*
compliance is weak: an entire feature missing, tests in wrong files, the filtered WS
sequence test skipped, plus the two confirmed contract bugs. Recurring nit across
sessions: status string literals duplicated across modules instead of a `Status`
enum (single-source-of-truth convention).

## Composite

```
Q = 0.8 × (46.79/60) + 0.2 × 1.0(provisional) = 0.824
L = 0.510 (provisional, in-family judge)
Composite = 0.6 × 0.824 + 0.4 × 0.510 = 0.698 → 69.8
Non-converged cap → FINAL: 50.0
```

## Threats to validity for this run

- **N=1** — no within-team spread available; do not compare against the default team
  until it has ≥1 (ideally 3) runs.
- **Post-hoc probe** — authored after this run (spec-only, but note it).
- **In-family judge** — L must be re-scored out-of-family before publication.
- **Manual HEAD commit** — final F004 state was hand-committed, violating §1.2
  ("nothing manual on the branch"); it preserved the working tree rather than
  altering it, but strictly the run should be recorded as aborted-at-F004.
- **Coder cost unreported** — opencode/qwen invocations carry no `total_cost_usd`;
  efficiency comparisons vs the default team will be one-sided unless fixed in
  orq-lite's cost capture or priced from token counts.

## Where the run died (for the next iteration)

The critic (Sonnet) rejected F004 four times for the same `ws.py` disconnect flaw the
bug hunt later confirmed — the governance signal worked; the qwen coder repeatedly
failed to apply the suggested restructuring ("check the filter condition inline
rather than using `continue`"). Rate limiting played no role. If a hybrid r2 is run,
watch whether coder-critic convergence on concurrency-shaped feedback is the
systematic weak point of this mapping.
