# Benchmark round 2 — Hookrelay

> **Execution status (2026-07-18): all four runs complete.** See
> [`../results/round2-execution-report.md`](../results/round2-execution-report.md)
> for outcomes, incidents, and the governance-loop A/B. Scoring pending.

Design record + setup for the second round: **superpowers one-shot vs orq-lite
ticketed (Sonnet 5) vs orq-lite ticketed (qwen3.7)**, building a new project so
round 1's findings can be tested for generalization.

## Design rationale (decided 2026-07-17)

- **Goal: replication in a new domain.** Same stack (Python 3.12, FastAPI,
  SQLite async) so the evaluation pipeline transfers, but a different system:
  [Hookrelay](./features.md), a webhook delivery service. Each of round 1's
  four recurring bug families has a natural re-incarnation here:

  | Round-1 family | Hookrelay incarnation | Spec treatment |
  |---|---|---|
  | Shutdown lifecycle (the "immortal bug") | deliveries stuck in `sending` after SIGTERM | **explicit requirement** (graceful-shutdown feature) — measures whether teams satisfy it *when asked* |
  | Naive timestamps | response bodies, SSE `ts`, HMAC timestamp header | implicit trap — measures whether reviewers catch it *unprompted* |
  | Duplicate events | duplicate delivery rows / duplicate attempt POSTs | implicit trap |
  | Realtime subscription leak | SSE client disconnect | implicit trap, but **observable** via `feed_clients` in `/stats` (round-1 spec-defect fix) |

- **Round-1 spec defects designed out**: no unimplementable ordered-sequence
  bullet, no mandated literal test filenames, the test-harness seam
  (`delivery_client_factory`) is specified with its exact override snippet,
  and dispatcher claim latency is bounded explicitly (≤1 s) so the probe can
  poll deterministically.
- **Protocol fixes vs round 1** (see [`evaluation.md`](./evaluation.md)):
  probe pre-registered and hash-frozen before any run; standardized bug hunt
  (same model, same prompt, run after all conditions finish); cost floors
  declared upfront.
- **Replication purity**: the ticketed prompts/pack are copied from the
  round-1 v4 canary. The critic prompts were NOT given a shutdown-lifecycle
  question — F005 makes shutdown part of the spec instead, so the conditions
  are measured on implementing it, not on being reminded of it. One
  domain-noun adaptation in the pack (`qa.md`: WebSocket/Prefect →
  SSE/delivery) is the only prompt change.

## Contents

- `features.md` — the Hookrelay contract (master copy; duplicated into each
  run folder).
- `CONVENTIONS.md` — hard rules for the reviewers (adapted from round 1).
- `evaluation.md` — scoring protocol.
- `probe/test_probe.py` — pre-registered independent probe (15 tests), frozen
  at `probe/PROBE_SHA256`.
- `teams/team-ticketed-sonnet.json`, `teams/team-ticketed-qwen.json` — the two
  orq-lite team configs (identical except coder/integrator/tester agents).

## Run folders

`~/Projects/personal/hookrelay-oneshot`, `…/hookrelay-ticketed-sonnet`,
`…/hookrelay-ticketed-qwen` — identical tracked base commit (tag `bench-base`);
ticketed folders carry the untracked orchestration config (team.json, prompts,
`development@2` pack, pinned `orq-lite` binary copied from the round-1 v4
canary).

Launch commands:

```sh
# ticketed (either folder)
.orquestalite/bin/orq-lite flow run factory-governed@2 features_path=features.md

# one-shot: fresh Claude Code session in hookrelay-oneshot with superpowers,
# prompt (verbatim, as round 1):
#   Implement every feature in features.md, in order, committing after each
#   feature. Follow CONVENTIONS.md. Both gates (`uv run ruff check .`,
#   `uv run pytest -q`) must pass after every feature.
# Record wall-clock and /cost at the end.
```
