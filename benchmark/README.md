# Team benchmark: hybrid vs default

An A/B benchmark for orq-lite team configurations. Both teams build the **same
project** — "Taskflow", a Python service with a full REST API over SQLite, a
Prefect (v3) work-queue processing pipeline, and WebSocket event streaming
([`features.md`](./features.md)) — through the **same** `factory_governed` flow,
with the same prompts, gates, limits, and conventions. The only independent
variable is which model runs each role:

| Role | [`team-default.json`](./team-default.json) | [`team-hybrid.json`](./team-hybrid.json) |
|---|---|---|
| parser, architect, qa, pm, gov_reviewer, reviewer | Opus (+ cross-provider fallbacks) | Opus |
| coder, tester | GPT-5.5 (codex) → Sonnet fallback | Qwen3.6-Coder via `opencode` |
| critic | Opus | Sonnet |

The hypothesis under test: does concentrating the frontier model on
orchestration/validation roles while a cheap coding model does the volume work
(hybrid) match the default mapping's quality at a lower cost?

The two resulting branches are scored with a **60% deterministic / 40%
LLM-as-a-judge** composite — protocol, rubric, judge prompts, and bias controls
live in [`evaluation.md`](./evaluation.md).

## Files

| File | Purpose |
|---|---|
| `features.md` | The spec both teams build: 5 features (skeleton+SQLite, jobs REST API, Prefect pipeline, WebSocket stream, stats+E2E), one `## ` heading each, mechanically checkable acceptance criteria. |
| `team-default.json` | The default role mapping, adapted to the Python gates + governance roles. |
| `team-hybrid.json` | Opus orchestration/validation, Qwen coder/tester, Sonnet critic. Single-agent chains so every artifact is attributable to one model. |
| `CONVENTIONS.md` | Hard failure-mode rules injected into coder/critic/reviewer prompts. Shared by both teams — do not fork it per team. |
| `evaluation.md` | Scoring guide: quantitative metrics, judge rubric with anchors, prompts, score sheet, threats to validity. |

## Prerequisites

- `orq-lite` on PATH, `uv`, `git`, `gh` (only if you let the flow push/open a PR).
- Provider CLIs installed and **authenticated headless**: `claude` (both teams),
  `codex` (default team), `opencode` (hybrid team).
- Edit `team-hybrid.json` first: the `qwen_coder` model id
  (`openrouter/qwen/qwen3.6-coder`) must match the provider/model name your
  `opencode` configuration actually exposes (`opencode models` to list).

## Step 1 — Build the scaffold repo (once)

Both teams start from the same tagged commit with **green gates on an empty
suite**:

```sh
mkdir taskflow-bench && cd taskflow-bench && git init
uv init --name taskflow --python 3.12
uv add fastapi uvicorn "sqlalchemy[asyncio]" aiosqlite "prefect>=3" pydantic-settings
uv add --dev pytest pytest-asyncio httpx ruff
mkdir -p app tests && touch app/__init__.py tests/__init__.py
```

Add to `pyproject.toml`:

```toml
[tool.pytest.ini_options]
asyncio_mode = "auto"

[tool.ruff]
target-version = "py312"
line-length = 100
```

Verify both gates pass (`uv run pytest -q` exits 0 with no tests collected is
fine; if your pytest treats "no tests" as failure, add a trivial placeholder
test), then:

```sh
git add -A && git commit -m "chore: bench scaffold" && git tag bench-base
```

## Step 2 — Install the orq-lite config

From this repo, copy into the scaffold:

```sh
orq-lite init                                  # writes prompts/, schemas/, .orquestalite ignore
cp <orq-lite>/benchmark/features.md    features.md
cp <orq-lite>/benchmark/CONVENTIONS.md CONVENTIONS.md
cp <orq-lite>/examples/fastapi-governed/flows.json flows.json
cp <orq-lite>/examples/fastapi-governed/prompts/*.md prompts/   # architect, qa, pm, gov-reviewer
cp <orq-lite>/benchmark/team-default.json team.json             # team under test
```

Adapt the governance prompts once (same edit for both teams): replace the example's
project-layout section with Taskflow's tree and name the real gate commands, per
`guide.md` §4. Then:

```sh
orq-lite doctor        # must be all green
git add -A && git commit -m "chore: orq-lite bench config (default team)"
```

## Step 3 — Run

One **fresh clone (or worktree) per team per repetition** — never reuse another
run's `.orquestalite/` state. Aim for N ≥ 3 repetitions per team.

```sh
orq-lite flow run factory_governed features_path=features.md \
  base_branch=main work_branch=bench/default-r1 --log-format verbose
```

For the hybrid runs, swap the team file in that clone before launching:

```sh
cp <orq-lite>/benchmark/team-hybrid.json team.json
git commit -am "chore: switch to hybrid team"
orq-lite flow run factory_governed features_path=features.md \
  base_branch=main work_branch=bench/hybrid-r1 --log-format verbose
```

A run that exhausts a `retry_until` budget aborts before the PR — that is a
valid result (score it as non-converged per `evaluation.md`), not something to
patch by hand and resume.

## Step 4 — Capture run data

After each run, from the run's clone:

```sh
orq-lite serve         # dashboard + query API on loopback
curl -s localhost:<port>/api/runs                      # duration, status, totals
curl -s "localhost:<port>/api/stats/cost?by=role"      # cost split: orchestration vs coding
curl -s "localhost:<port>/api/agent-runs?run_id=<id>"  # attempts, rate_limited, tokens
```

Archive per run: the work branch, `.orquestalite/run.log`, the cost-by-role
JSON, and the output of both gates at the branch HEAD from a fresh checkout.

## Step 5 — Score

Follow [`evaluation.md`](./evaluation.md) end to end:

1. **Quantitative first** (§2): acceptance-checklist walk over every
   `features.md` bullet, gates at HEAD, the independent test probe (write it
   **before** looking at either implementation), adversarial bug hunt with
   mandatory reproduction, and the efficiency numbers from step 4.
2. **LLM-as-a-judge** (§3): blind the two branches as `impl_A`/`impl_B`, use an
   out-of-family judge (e.g. Gemini), 3 samples, position-swapped pairwise +
   absolute rubric, evidence required per score.
3. **Composite** (§4): `0.6 × Q + 0.4 × L`, reported as mean ± spread across
   repetitions. A winner inside the within-team spread is a tie.

## Fairness rules (do not skip)

- Everything except `team.json` is byte-identical across teams: spec,
  conventions, prompts, flow, limits, gates, scaffold commit.
- No manual commits on work branches; no resuming across team switches.
- Note any rate-limit incident windows — they distort wall-clock and retry
  counts (see `evaluation.md` §5).
- If a default-team run delivered mostly via fallback agents (check
  `agent-runs`), label it: it no longer measures the intended mapping.
