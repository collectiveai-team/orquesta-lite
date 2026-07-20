# Benchmark round 2: superpowers one-shot vs ticketed (Sonnet 5) vs ticketed (qwen3.7)

Three conditions build the same project ([`features.md`](./features.md):
Hookrelay — FastAPI + SQLite + async webhook dispatcher + SSE) from an
identical scaffold, and the three resulting codebases are scored with the same
pipeline as round 1: deterministic checks first, LLM-as-a-judge second.

**Conditions under test**

| | one-shot | ticketed-sonnet | ticketed-qwen |
|---|---|---|---|
| Harness | Claude Code + superpowers skills, single session | orq-lite durable runtime, `factory-governed@2` | orq-lite durable runtime, `factory-governed@2` |
| Coder / integrator | Sonnet 5 (self-directed) | Sonnet 5 | qwen3.7-plus via `opencode` |
| Planning / ticket QA / governance | self-administered | Opus | Opus |
| Critic | none (self-review) | Sonnet 4.6 | Sonnet 4.6 |
| Cost accounting | complete (`/cost`) | complete (run.log, all Anthropic) | **floor** (opencode reports no cost) — declared upfront |

The tracked base commit is byte-identical across the three folders; the
orchestration config (`team.json`, prompts, pack) lives untracked in the
ticketed folders and differs between them only in the coder/integrator/tester
agent mapping. This isolates two comparisons: **harness** (one-shot vs
ticketed-sonnet, same coder) and **coder** (ticketed-sonnet vs ticketed-qwen,
same harness).

## 1. Protocol

### 1.1 Setup (already materialized in the three folders)

1. Scaffold per folder (`hookrelay-oneshot`, `hookrelay-ticketed-sonnet`,
   `hookrelay-ticketed-qwen` under `~/Projects/personal/`): `pyproject.toml`
   with the `features.md` dependency preamble, `.python-version` 3.12,
   committed `uv.lock`, empty `app/` + `tests/` packages, ruff config,
   `asyncio_mode = "auto"`, `features.md`, `CONVENTIONS.md`, stub `README.md`,
   `.gitignore`. Both gates green at HEAD. One commit, tagged `bench-base`.
   The `pyproject.toml` description is neutral (round-1 lesson: a
   model/runtime-identifying description leaked into the blind export).
2. Ticketed folders additionally carry (untracked): `team.json` (from
   [`teams/`](./teams/)), root `prompts/`, the `development@2` pack under
   `.orquestalite/packs/`, and the pinned `orq-lite` binary under
   `.orquestalite/bin/`. `orq-lite doctor` must be green before launch.
3. Launch:
   - ticketed (both): from the folder root,
     `.orquestalite/bin/orq-lite flow run factory-governed@2 features_path=features.md`
   - one-shot: a fresh Claude Code session in the folder with superpowers
     enabled and the round-1 prompt, verbatim: *"Implement every feature in
     features.md, in order, committing after each feature. Follow
     CONVENTIONS.md. Both gates (`uv run ruff check .`, `uv run pytest -q`)
     must pass after every feature."* Record wall-clock and `/cost` at the end.
4. Run hygiene as round 1: nothing manual on any branch; an aborted run is a
   result (scored non-converged), not something to patch and continue; record
   provider incidents; comparable hours where possible.
5. N=1 per condition this round (budget); the folders are kept relaunchable
   for repetitions.

### 1.2 Pre-registered probe (round-1 fix)

The independent test probe ([`probe/test_probe.py`](./probe/test_probe.py)) is
written from `features.md` alone, **before any condition runs**, and frozen:
its SHA-256 is recorded below at freeze time and the file may not change after
the first run starts. If the probe itself turns out to be defective (a test
that no conforming implementation could pass), the defect is documented, the
probe is version-bumped, and the fixed probe is re-run against **all**
conditions — never against one.

Probe freeze hash (v2.0):
`ce697cf2d4cb06bcb36c92b2691570b5275c0ec42cbe53283760a5a5ec447b46`
(also in [`probe/PROBE_SHA256`](./probe/PROBE_SHA256))

### 1.3 Data capture

Per run, archive into `benchmark/results/round2-<condition>-artifacts/`: the
final branch (git bundle), `run.log` (ticketed) or the session transcript
pointer + `/cost` output (one-shot), cost-by-role JSON, and the gate output at
HEAD on a fresh clone.

## 2. Quantitative scoring (60% of the composite)

Identical to round 1; all deterministic, computed before any judge sees code.

| Metric | How | Points |
|---|---|---|
| Converged | ticketed: flow reached governance approval; one-shot: session completed all features with green gates | gate: non-converged runs score their partial checklist but cap the composite at 50 |
| Acceptance checklist | every bullet of `features.md` (each sub-bullet = 1 item) verified **by execution** on the final branch | 30 |
| Gates at HEAD | `uv run ruff check .` and `uv run pytest -q` exit 0 on a fresh clone | 10 (all-or-nothing) |
| Independent probe | §1.2's frozen file, applied identically to all three branches; score = pass % | 10 |
| Confirmed post-hoc bugs | §3.3's standardized hunt; each confirmed-by-reproduction bug −2, floor 0 | 10 − 2/bug |

*Subtotal 60, normalized.* Efficiency: `eff = min(1, best_cost/cost) * 0.5 +
min(1, best_time/time) * 0.5` normalized over the three runs (cost floors
flagged; a floor-priced run's eff is reported but marked one-sided).
`Q = 0.8 * (correctness/60) + 0.2 * eff`.

## 3. Qualitative scoring — LLM as a judge (40% of the composite)

### 3.1 Setup and bias controls (as round 1)

- Judge preference: out-of-family (`gemini`); if unavailable, in-family judges
  are used and **flagged in every report** (all three conditions contain
  Anthropic models — the caveat applies to all legs equally this round).
- Blind exports `impl_A/B/C`: strip `.git/`, `.orquestalite/`, `team.json`,
  `prompts/`, any model-identifying strings; verify the `pyproject.toml`
  description is neutral before export.
- 3 absolute sessions per implementation (median per dimension), evidence
  (`file:line` or command output) required, deterministic results provided and
  binding.
- Pairwise: all three pairs, every dimension asked in both orders; a flipped
  verdict is a tie. Random letter assignment per session.

### 3.2 Dimensions and weights

Same as round 1 with domain nouns updated: Spec fidelity 25%, Correctness &
robustness 20%, Architecture & layering 15%, Test quality 15%, Concurrency &
async correctness 10% (bounded fan-out, cancellation-safe lifespan/SSE
cleanup, dispatcher shutdown), Code quality & idiom 10%, Docs & DX 5%.
Anchors as in round 1's §3.2. `L = Σ (weight × median) / 5`.

### 3.3 Standardized adversarial bug hunt (round-1 fix)

One hunter session per implementation, **same evaluator model, same prompt,
run only after all three conditions have finished** (round 1's hunter
aggressiveness drifted between legs; the shutdown-bug reproduction standard
arrived mid-round). Hunt list for this project: naive datetimes surviving the
aiosqlite round-trip (response bodies, SSE `ts`, signature timestamp);
duplicate deliveries or duplicate attempt POSTs; signature computed over
re-serialized rather than sent bytes; SSE subscriptions leaked on client
disconnect; deliveries stuck in `sending` after shutdown /
`CancelledError` swallowed; publish-inside-transaction; inactive
subscriptions still receiving deliveries; secrets leaking into responses,
logs, or SSE frames; unbounded dispatcher concurrency; N+1 queries in
stats/list endpoints. Every claim needs a reproduction (test, curl, or
transcript) against the running code; only reproduced claims count.

## 4. Composite and report

`Composite = 0.6 × Q + 0.4 × L`, cap 50 for non-converged. Report per
condition: convergence, checklist %, probe %, confirmed bug list, cost
(complete vs floor), wall-clock, L with per-dimension medians, pairwise
matrix, and the two isolated comparisons named in the preamble (harness
effect: one-shot vs ticketed-sonnet; coder effect: ticketed-sonnet vs
ticketed-qwen). Flag §3.5-style absolute/pairwise disagreements for human
review rather than resolving them silently.

## 5. Threats to validity (standing)

- N=1 per condition; margins inside plausible run-to-run spread are reported
  as ties.
- In-family judges unless gemini is used; mitigations: blinding, position
  swap, evidence requirement, deterministic-majority composite.
- qwen leg cost is a floor — cost-based comparisons involving it are
  one-sided by construction.
- Spec ambiguity protocol: any bullet two conditions read differently is
  scored under both readings and flagged; the spec gets fixed for round 3,
  no condition is penalized. (Round-1 spec-defect classes — an
  unimplementable ordered-sequence bullet, literal test filenames, and an
  ambiguous harness fixture — were designed out of this spec: the SSE leak
  check is observable via `feed_clients`, test bullets name behaviors instead
  of filenames, and the delivery-client seam is specified with its exact
  override snippet.)
- The one-shot condition self-reports cost (`/cost`) and has no run.log;
  wall-clock relies on session timestamps.
