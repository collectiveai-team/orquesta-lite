# Six-way comparison — round 1 closed

Adds **v4 ticketed** (`taskflow-v4-canary`, run `r20260717T152327Z-796b`) to
[`fiveway-r1.md`](./fiveway-r1.md). v4 = the per-ticket governed flow with
**Sonnet 5 as coder/integrator** (Opus planners/ticket-QA/governance, Sonnet 4.6
critic) — the marriage of the one-shot's coder with the orchestrated harness.
Single clean attempt, zero step retries anywhere, converged through critic +
governance in 1h57m. **First fully-priced orchestrated run: $31.03, all roles.**

## Final scoreboard — all six conditions

| | default (batch, GPT-5.5) | hybrid (batch, qwen) | Sonnet 5 one-shot | ticketed v2 (qwen) | ticketed v3 (qwen) | **ticketed v4 (Sonnet 5)** |
|---|---|---|---|---|---|---|
| Converged + approved | ✅ | ❌ | ✅ (no reviewer) | ❌ (infra) | ✅ | **✅ (zero retries)** |
| Checklist | 25/27 | 20/27 | 26/27 | 26/27 | 25/27 | **26/27** |
| Probe (identical) | 14/14 | 12/14 | 14/14 | 14/14 | 14/14 | **14/14** |
| Confirmed bugs | **0** | 2 | 3 | 2 | 4 | **1** |
| Correctness /60 | **57.78** | 46.79 | 52.89 | 54.89 | 49.78 | 56.89 |
| L (median of 3) | 0.770 | 0.510 | 0.600 | 0.590 | 0.550 | **0.800** |
| Pairwise vs default | — | 0W/7L | 1T/6L | 1T/6L | 0T/7L | **2W/2T/3L** |
| Wall-clock | 1h55m | 3h08m✗ | ~1–2h | 2h30m✗ | 1h37m† | 1h57m |
| Tracked cost | $25.46 (floor) | $9.11 (floor) | $15.15 (complete) | $39.16 (floor) | $12.53 (floor†) | **$31.03 (complete)** |
| **Composite** | **84.22** | 66.9* | 74.7 | 72.8* | 72.2 | **84.22** |

\*Uncapped informational; strict protocol caps non-converged runs at 50.
†v3 attribution contaminated by 3 failed same-day attempts.

## The headline: a dead heat — and the §3.5 disagreement flag

**default 84.22 vs v4 84.22.** The two composites differ by less than 0.01 of a
point — far inside any conceivable run-to-run spread. The protocols disagree on
the winner: the absolute rubric favors v4 by a hair (L 0.800 vs 0.770 — highest
of the round, with 5s in architecture, code quality, and docs that no other
condition earned), while the blind pairwise favors the default 3–2 with 2 ties.
Per §3.5, that disagreement is flagged for human review rather than resolved by
fiat. What can be said deterministically:

- **default keeps the correctness crown**: 0 confirmed bugs vs v4's 1.
- **v4 keeps the quality/cost crown**: best L, best checklist tier, and its
  $31.03 is *complete* while the default's $25.46 is a floor (its GPT-5.5 coder
  is unpriced — the true default cost is unknown and plausibly higher than v4's).

## What v4 proves (the arc of the whole experiment)

1. **The recurring bug families are fixable by the harness+coder pairing.**
   Three of the four families that plagued every prior non-default build were
   verified FIXED with reproductions: naive timestamps (an `_assume_utc`
   validator — the first non-default build to solve it), the WS
   filtered-disconnect leak (proper receive-task race), and duplicate prefect
   events (poller excludes `pending` by design). Same model that shipped all
   three when running one-shot without reviewers.
2. **One family survived everything: shutdown lifecycle.** Fire-and-forget
   dispatcher tasks + `except Exception` missing `CancelledError` → jobs stuck
   non-terminal on shutdown. Reproduced in v3, v4, and (unconfirmed) the
   one-shot; only the batch default tracked and drained its tasks — and it's
   the only condition whose reviewer explicitly demanded it. This is now the
   benchmark's canonical example of a bug class that per-ticket review, global
   QA, a critic pass, and governance all miss unless someone asks the specific
   question.
3. **Zero retries.** Sonnet 5 under ticket granularity needed no second attempts
   anywhere — the convergence result from qwen (9/9 first-attempt) replicated
   with a stronger coder and produced far higher quality, separating the two
   effects: granularity buys convergence; coder capability buys quality.
4. **The universal spec defect is now 6-for-6**: every condition failed the
   filtered-WS ordered-sequence test bullet. Fix `features.md` before round 2.

## Judges (medians) — v4

spec 4 · correctness 3 · **architecture 5** · tests 4 · concurrency 3 ·
**code 5** · **docs 5** → L = 0.800. The three 5s are unique in the round.
Judge dissent worth noting: one session scored architecture 3 over status-string
literals repeated outside the constants module — the same nit raised against v3.

## Pairwise vs default (position-swapped, blind)

| Dimension | verdict |
|---|---|
| Spec fidelity | **v4** (both orders) |
| Correctness & robustness | default (both) |
| Architecture & layering | tie (flipped) |
| Test quality | default (both — 66 vs 40 tests, layer-isolated) |
| Concurrency & async | default (both — the shutdown bug decides it) |
| Code quality & idiom | tie (flipped) |
| Docs & DX | **v4** (both orders) |

## Round-1 closing observations

- **Cost per checklist point, complete-cost conditions only**: v4 $1.19/pt,
  one-shot $0.58/pt. The governed harness costs ~2× per point over the same
  coder one-shot — and buys a drop from 3 bugs to 1, plus governance sign-off.
- The efficiency subscore still favors floor-priced runs; until codex/opencode
  spend is captured, cross-team eff comparisons remain one-sided (§5 standing).
- All standing caveats apply: N=1 per condition, in-family judges (at their
  worst for Sonnet-5-built, Claude-judged legs), the v3 contamination, and
  varying bug-hunter aggressiveness across legs (the shutdown-bug reproduction
  standard was applied from v3 onward; the one-shot's count may be understated).

## Recommended round-2 agenda (from the data)

1. Fix `features.md`: the filtered-WS bullet (6/6 failures), the
   `tests/test_worker.py` literalness question, the harness fixture bullet.
2. Add the shutdown-lifecycle question to the critic/gov prompts — the one bug
   class no review stage caught unprompted.
3. Capture codex/opencode spend (price from tokens if needed) to make eff real.
4. N≥3 runs for the two finalists (default batch vs v4 ticketed) — the dead
   heat is the round-2 headline question.
5. Out-of-family (gemini) re-judge before publishing any L or pairwise number.
