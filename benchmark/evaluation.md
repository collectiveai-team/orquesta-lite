# Benchmark: hybrid team vs default team

Compares two `team.json` configurations building the same project
([`features.md`](./features.md): FastAPI + SQLite + WebSocket + Prefect work queue)
through the same `factory_governed` flow, and scores the two resulting codebases
quantitatively (deterministic checks + run telemetry) and qualitatively
(LLM-as-a-judge).

**Teams under test**

| | [`team-default.json`](./team-default.json) | [`team-hybrid.json`](./team-hybrid.json) |
|---|---|---|
| parser / architect / pm / qa / gov_reviewer / reviewer | Opus (+ fallbacks) | Opus |
| coder / tester | GPT-5.5 (codex) → Sonnet fallback | Qwen3.6-Coder via `opencode` |
| critic | Opus | Sonnet |
| gates, limits, prompts, conventions | identical | identical |

The only independent variable is the agent→role mapping. Gates
(`uv run pytest -q`, `uv run ruff check .`), limits, timeouts, prompts, flow, and
`CONVENTIONS.md` are byte-identical. Note: the hybrid team uses single-agent chains
(no cross-provider fallback) so every artifact is attributable to one model;
rate limits are absorbed by `rate_limit_backoff`, not rerouted. Adjust the
`qwen_coder` model id (`openrouter/qwen/qwen3.6-coder`) to whatever provider name
your `opencode` config actually exposes.

## 1. Protocol

### 1.1 Setup (once per team, from the same scaffold commit)

1. Create the scaffold repo: `uv init`, add the dependencies from the `features.md`
   preamble, `.python-version` = 3.12, empty `app/` + `tests/` packages, ruff config,
   `asyncio_mode = "auto"`. Both gates must be green at HEAD (empty suite is fine).
   Tag it `bench-base`.
2. Copy into it: `benchmark/features.md` → `features.md`, `benchmark/CONVENTIONS.md`
   → `CONVENTIONS.md`, the governed flow (`examples/fastapi-governed/flows.json`),
   the standard prompts (`orq-lite init` or repo `prompts/`) plus the governance
   prompts from `examples/fastapi-governed/prompts/`, and the team file under test as
   `team.json`. Commit; `orq-lite doctor` must be all green.
3. Run, one work branch per team and repetition:

   ```sh
   orq-lite flow run factory_governed features_path=features.md \
     base_branch=main work_branch=bench/<team>-r<n> --log-format verbose
   ```

4. Repetitions: **N ≥ 3 runs per team** if budget allows. Single-run deltas smaller
   than the run-to-run spread within one team are noise — report the spread.

### 1.2 Run hygiene

- Fresh clone (or `git worktree`) per run; never resume one team's run on top of the
  other's state (`.orquestalite/` is per-run state).
- Run at comparable hours; a provider outage or heavy rate limiting invalidates the
  *time* metrics of that run (cost/quality remain usable). Record incidents.
- Nothing manual on the branch. If a run aborts (`retry_until` exhausted), that is a
  result — score it as non-converged, do not patch and continue.

### 1.3 Data capture

`orq-lite` writes everything needed to `.orquestalite/run.log`, indexed into a
SQLite read-model:

```sh
orq-lite index            # or: orq-lite serve   (dashboard + query API)
curl -s localhost:<port>/api/runs                     # status, duration, totals
curl -s localhost:<port>/api/stats/cost?by=role       # cost/tokens per role
curl -s "localhost:<port>/api/agent-runs?run_id=<id>" # per-invocation detail
```

Archive per run: the final branch, `run.log`, cost-by-role JSON, and the gate output
at HEAD.

## 2. Quantitative scoring (60% of the composite)

All deterministic — computed before any judge sees the code.

### 2.1 Delivery and correctness

| Metric | How | Points |
|---|---|---|
| Converged | flow reached push/PR with all four approvals | gate: a non-converged run scores its partial checklist but caps the composite at 50 |
| Acceptance checklist | walk **every bullet** of `features.md` (each sub-bullet = 1 item) against the final branch: implemented and observably correct (run the endpoint/test, don't trust naming). Score = pass % | 30 |
| Gates at HEAD | `uv run pytest -q` and `uv run ruff check .` both exit 0 on a fresh clone of the branch | 10 (all-or-nothing) |
| Independent test probe | a fixed, secret pytest file written once by the evaluator before both runs (black-box probes: 422/404/409 bodies, WS event order, stats math, failed-job contract). Same file applied to both branches; score = pass % | 10 |
| Confirmed post-hoc bugs | adversarial review of `git diff bench-base...HEAD` (see §3.3) hunting spec-blind defects; each **confirmed-by-reproduction** bug −2, floor 0 | 10 − 2/bug |

*Subtotal: 60 normalized to 100.* The independent probe exists because each team's
own tests pass by construction; it is the only test signal the teams could not
overfit.

### 2.2 Efficiency (reported, and 20% of the quantitative index)

| Metric | Source |
|---|---|
| Total cost (USD) and tokens in/out | `/api/stats/cost?by=run` |
| Cost by role (orchestration vs coding share) | `/api/stats/cost?by=role` |
| Wall-clock (minus rate-limit stall time) | `/api/runs` + `agent-runs` (`rate_limited`) |
| Agent invocations / fix attempts per feature | `agent_runs` per `task_id`, `attempt` |
| Governance rounds used (of 3) | run log / commit messages |
| Cost per accepted checklist point | derived: cost ÷ checklist score |

Normalize efficiency to the better run: `eff = min(1, best_cost/cost) * 0.5 +
min(1, best_time/time) * 0.5`.

**Quantitative index** `Q = 0.8 * (correctness subtotal / 60) + 0.2 * eff`, on 0–1.

## 3. Qualitative scoring — LLM as a judge (40% of the composite)

### 3.1 Judge setup and bias controls

- **Judge model ≠ any model in either team.** Both teams contain Anthropic and the
  default contains GPT-5.5, so prefer `gemini-3-pro`; if unavailable, use one judge
  per family and average — never a single judge from a family under test
  (self-preference bias).
- **Blind**: export each branch as `impl_A/` and `impl_B/` (random assignment),
  strip `.orquestalite/`, `.git/`, commit messages, and any model-identifying
  strings before the judge reads anything.
- **Position swap**: every pairwise question is asked twice (A,B) and (B,A); a
  verdict that flips with position is recorded as a tie.
- **Sampling**: 3 independent judge sessions per protocol; aggregate with the
  median (absolute scores) and majority (pairwise).
- **Evidence required**: every score must cite `file:line` or a command output;
  a score without evidence is discarded and re-sampled.
- **Deterministic beats vibes**: the judge receives §2's results and may not
  contradict them (e.g. cannot rate "test quality" 5 on a branch whose independent
  probe scored 40%).

### 3.2 Dimensions, weights, and anchors

Score each implementation 1–5 per dimension (absolute protocol), then also a
pairwise A/B preference per dimension with a one-paragraph justification.

| Dimension | Weight | 1 | 3 | 5 |
|---|---|---|---|---|
| Spec fidelity | 25% | endpoints/shapes deviate from `features.md` | contract met, minor drift in edge bodies | every shape, code, and event byte-exact, edge cases included |
| Correctness & robustness | 20% | reproducible crash or data loss path | correct happy paths; some sad paths unguarded | failure modes handled and persisted per contract (failed jobs, disconnects, invalid input) |
| Architecture & layering | 15% | logic in handlers, duplicated constants, tangled imports | layering mostly respected, small leaks | clean routes→service→repo, dispatcher/bus properly abstracted, single-source constants |
| Test quality | 15% | happy-path only, order-dependent, sleeps | criteria covered, some sad paths thin | independent, deterministic, sad-path rich, meaningful assertions (not snapshot noise) |
| Concurrency & async correctness | 10% | blocking I/O in loop, leaked tasks/subscriptions, races | works but fragile (unbounded queues, missing cancellation) | bounded fan-out, cancellation-safe lifespan/WS cleanup, no event-loop blocking |
| Code quality & idiom | 10% | untyped, dead code, copy-paste | typed and consistent with `CONVENTIONS.md` | idiomatic SQLAlchemy 2/Pydantic v2/Prefect 3 throughout, minimal and clear |
| Docs & DX | 5% | README missing/wrong | quickstart works | accurate ops story incl. prefect worker mode and WS example |

### 3.3 Adversarial bug hunt (feeds §2.1)

A separate judge session per implementation, prompted to **refute** quality: read
`git diff bench-base...HEAD` with the hunt list from this repo's field lessons —
partial-update data loss, write paths bypassing validation, broken earlier-feature
consumers, boundary math, name-vs-id joins, N+1 queries, leaked
tasks/subscriptions, event publishing inside transactions. Every claim must come
with a reproduction (test or curl/websocket transcript). Only reproduced claims
count as confirmed bugs.

### 3.4 Judge prompt template (absolute protocol)

```text
You are auditing one Python codebase (impl_X) against a specification.
Inputs: the spec (features.md), the conventions (CONVENTIONS.md), the codebase,
and the deterministic results (gate output, acceptance checklist, probe results).

For each dimension in the rubric below, output:
  {"dimension": ..., "score": 1-5, "evidence": ["file:line — reason", ...],
   "worst_finding": "..."}

Rules: judge only against the spec and conventions, not your own preferences.
You may not contradict the deterministic results. Cite concrete evidence for
every score; no evidence, no score. Do not reward code volume, comments, or
defensive boilerplate. Output JSON only.

[rubric with anchors inlined here]
```

The pairwise variant presents both codebases and asks, per dimension:
`{"dimension": ..., "winner": "A"|"B"|"tie", "justification": "..."}` — run twice
with positions swapped.

### 3.5 Qualitative index

`L = Σ (weight_d × median_score_d) / 5`, per implementation, on 0–1. Report
alongside it the pairwise win-rate (wins / decided comparisons) as a sanity check;
if the absolute and pairwise protocols disagree on the overall winner, flag it and
have a human review the disagreeing dimensions before publishing.

## 4. Final composite and report

```
Composite = 0.6 × Q + 0.4 × L          (per run; report mean ± spread over N runs)
```

Score sheet per run:

| Section | Metric | default | hybrid |
|---|---|---|---|
| Delivery | converged / rounds used / fix attempts | | |
| Correctness | checklist % / gates / probe % / confirmed bugs | | |
| Efficiency | cost USD / tokens / wall-clock / cost-per-point | | |
| Judge (median) | 7 dimension scores + L | | |
| Pairwise | win-rate per dimension | | |
| **Composite** | | | |

The written report must include: winner and margin, whether the margin exceeds the
within-team spread, cost-by-role breakdown (does putting Opus only on
orchestration/validation actually shift spend?), the confirmed-bug list, and the
judge's per-dimension justifications for the three largest gaps.

## 5. Threats to validity

- **N=1 noise** — agentic runs are high-variance; prefer 3 runs, and never claim a
  winner inside the within-team spread.
- **Judge bias** — mitigated by out-of-family judge, blinding, position swap,
  evidence requirement; residual bias is why the composite is majority-deterministic.
- **Spec ambiguity** — any bullet both teams interpreted differently is scored for
  both interpretations and flagged; fix `features.md` before the next round rather
  than penalizing either team.
- **Provider conditions** — rate limits distort time (excluded via `rate_limited`
  stall accounting) but also retry counts; note incident windows in the report.
- **Fallback asymmetry** — the default team has fallback chains, the hybrid does
  not; if a default run's artifact came mostly from fallback agents (check
  `agent-runs`), label the run — it no longer measures the intended mapping.
