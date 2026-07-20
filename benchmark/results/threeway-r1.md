# Three-way comparison — default team vs hybrid team vs Sonnet 5 one-shot

Extends [`comparative-r1.md`](./comparative-r1.md) (the protocol two-team result)
with a third condition: **a single Claude Code session (Sonnet 5 + superpowers)
implementing `features.md` in one shot** — no orchestration, no reviewer loop.
Branch `bench/sonnet5-oneshot` @ `e0d954b`, cut from the same base (`main` =
`c6ed7e6`) as both team runs. Same evaluation pipeline: fresh clone, identical
probe, same checklist items, same bug-hunt list, 3 absolute judge sessions,
blind pairwise vs the default with position swap.

> The one-shot leg measures **harness + model**, not model alone: different
> process (interactive agent with skills vs governed multi-agent flow), different
> model (Sonnet 5 vs GPT-5.5/qwen coders). It answers "what does the
> orchestration buy?", not "which model codes better".

## Headline

**The one-shot run delivered the most spec-complete build, in a quarter of the
time — and shipped 3 production bugs that the governed default run (0 bugs)
would have caught in review.** Two of the three are the *same failure modes*
that sank the hybrid run (naive HTTP timestamps, WS disconnect cleanup), and the
third (`avg_duration_s` truncation) is the exact bug the default's Opus critic
rejected in its own coder's first F005 attempt. The review loop's value shows up
precisely where self-testing can't reach: the one-shot's own tests are green
over all three bugs.

## Score sheet

| Section | Metric | default (governed) | hybrid (governed) | Sonnet 5 (one-shot) |
|---|---|---|---|---|
| Delivery | completed | yes (gov round 2/4) | **no** (died at F004) | yes (single session, 2 self-fix commits) |
| | wall-clock | 6,898 s (1h55m) | 11,296 s (3h08m, incomplete) | **~3,000–3,600 s (~50–60 min)** |
| Correctness | checklist | 25/27 (92.6%) | 20/27 (74.1%) | **26/27 (96.3%)** |
| | gates at HEAD | pass (66 tests) | pass (59 tests) | pass (**32 tests** — leanest suite) |
| | probe (identical file) | **14/14** | 12/14 | **14/14** |
| | confirmed bugs | **0** | 2 | 3 |
| | correctness subtotal /60 | **57.78** | 46.79 | 52.89 |
| Judge (median of 3) | spec/corr/arch/test/conc/code/docs | **4/4/4/3/4/4/4** | 2/2/4/2/3/4/1 | 3/2/4/3/2/4/4 |
| | **L** | **0.770** | 0.510 | 0.600 |
| Pairwise vs default (blind, swapped) | | — | 0W/7L | **1 tie / 6 losses** |
| Cost | tracked | $25.46* | $9.11* | **$15.15 (complete)** |
| | cost per checklist point | ≥$1.02 | ≥$0.46 | $0.58 |
| Efficiency | eff (§2.2, 3-way normalized) | 0.679 | 0.805 | 0.771 |
| **Composite** | 0.6·Q + 0.4·L | **85.2** | 50.0 (capped; 67.5) | **75.6** |

\*Coder cost unreported in both team runs (codex/qwen); hybrid tester also
unpriced — the team costs are FLOORS, the one-shot cost is complete. The
one-shot session's `/cost` also reveals it was not mono-model: $8.15 Sonnet 5 +
$7.00 Sonnet 4.6 (superpowers subagents) + $0.001 Haiku — i.e. the one-shot was
itself multi-agent under the hood, just without an adversarial review role.

**Time accounting for the one-shot:** session wall 2h02m (7,332 s, includes
human-in-the-loop idle), API time 1h04m31s (3,871 s), commit span 49 min. The
eff above uses session wall (7,332 s) — the most conservative choice for the
one-shot. Using API time instead would make it the fastest run (3,871 s) and
shift composites to default 82.5 / one-shot 75.9 / hybrid 50 — the ranking
**default > one-shot > hybrid is stable under every time definition**.

## Sonnet 5 one-shot: detail

**Checklist 26/27** — best of the three. The only failing item is the same one
everybody failed: the WS ordered-sequence test runs without the server-side
`?job_id=` filter (client-side filtering). Notably it was the **only** run that
honored the literal `tests/test_worker.py` filename + session-scoped
`prefect_test_harness` fixture bullets, and it self-corrected twice post-F005
(`fix: PrefectDispatcher coroutine drop and centralize status/event constants` —
the exact class of finding critics produced in the team runs).

**3 confirmed bugs (all reproduced):**
1. **Naive HTTP timestamps** — `Mapped[datetime]` without `timezone=True` +
   aiosqlite round-trip → every REST timestamp lacks `Z`/offset (WS events are
   correct). *Identical to hybrid bug #1.*
2. **`avg_duration_s` integer truncation** — `func.unixepoch()` truncates to
   whole seconds; 0.3 s + 0.7 s jobs average 0.0. Own tests seed whole-second
   offsets and stay green. *The default's critic caught this exact bug in review
   (F005 attempt 1) and forced the `julianday` fix.*
3. **WS subscriber leak under filter** — `WebSocketDisconnect` only surfaces in
   `send_json`; a filtered client disconnecting while the handler blocks on
   `queue.get()` leaks its subscription indefinitely under uvicorn. Masked in
   tests because TestClient's anyio task-group cancels `queue.get()` — a path
   production servers don't take. *Same failure family as hybrid bug #2; subtler.*

Unconfirmed suspicions: `update_status` can't null-out `error`/`result`;
untracked `create_task` in both dispatchers; job stuck `running` if `execute`
exhausts retries; poller skips events for sub-interval jobs.

**Judges (3 sessions, medians)** — strong architecture (4) and docs (4: complete
accurate README, minus a filtered-WS example), but correctness and concurrency
pinned at 2 by the confirmed bugs; test quality 3 (thorough sad-path REST
coverage, but the two green-while-broken tests — whole-second stats seeds,
client-side WS filtering — are exactly what an adversarial reviewer exists for).

**Blind pairwise vs default (position-swapped):** default wins 6 dimensions,
docs is a tie (verdict flipped tie→A across orders → recorded as tie). Both
sessions independently keyed on the same discriminators: `field_serializer` +
`julianday` + `asyncio.wait` disconnect race + tracked dispatcher tasks +
66-vs-32 test depth — i.e. the review loop's fingerprints.

## What this says (post material)

1. **Spec coverage is not the bottleneck for a frontier one-shot agent** — it
   beat both governed teams on checklist and tied the probe, in ~¼ the
   wall-clock of the default run. Breadth is cheap now.
2. **The governed loop's measurable value is bug prevention, not coverage.**
   0 vs 3 confirmed bugs, and the causal chain is visible in the logs: the
   default's critic rejected `unixepoch` truncation in review; the one-shot
   shipped it. Two of the one-shot's three bugs are the same classes the hybrid
   run's critic kept flagging.
3. **Self-tests can't referee themselves.** All three bugs in the one-shot hide
   behind its own green tests (whole-second seeds, TestClient cancellation
   semantics, client-side filtering). The independent probe missed 2 of 3 too —
   only the adversarial reproduce-or-discard hunt surfaced them. Rubric scores
   without deterministic anchors would have called this codebase excellent.
4. **The spec's filtered-WS-sequence bullet defeated all three conditions**
   (three models, two harnesses) — that's a spec defect, not a model signal. Fix
   `features.md` before round 2.

## Threats to validity (this leg)

- N=1, as everywhere in round 1.
- In-family judging is at its worst here: Claude judging Claude's own build.
  The deterministic anchors (probe, reproduced bugs) carry the comparison; the
  L/pairwise numbers need an out-of-family re-judge before publishing.
- The one-shot ran on the developer's interactive setup with superpowers skills
  and spawned Sonnet 4.6 subagents (46% of its cost) — "one-shot" means "no
  adversarial review loop", not "single model" or "single agent".
- Its wall-clock includes human-in-the-loop time (session wall 2h02m vs API
  1h05m vs commit span 49 min); eff uses the conservative session wall.
- Harness and model vary together in this leg by design; don't read it as a
  Sonnet-vs-GPT comparison.
