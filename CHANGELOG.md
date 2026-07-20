# Changelog

All notable changes to orq-lite are recorded here. Versions follow the git
tags cut as GitHub releases (the binary's `--version` is stamped from the tag).

## v0.2.3 — Governed pack example + guide overhaul

Docs and examples release. No engine behavior changes; the pinned binary is
rebuilt with the new version string.

### Added

- **`examples/governed-pack/`** — the recommended production setup for the
  durable v2 runtime: the `development/factory-governed@4` pack, self-contained
  and runnable locally with a cheap haiku team. The flow distils three
  benchmark rounds of field lessons into structure:
  - an **`adversary`** role that hunts what the spec *didn't* say (failure
    hypotheses from the system's shape, reproduction required);
  - a **governance repair loop** — a veto feeds the integrator, gates re-run,
    and a **fresh** governance invocation re-audits (max 2 cycles, fail-closed
    preserved);
  - a **test-integrity audit** in `ticket_qa` and the adversary (would this
    assertion fail if the behavior regressed?);
  - **budget-sized tickets** so streaming/worker/lifecycle concerns get their
    own units.
- **`CHANGELOG.md`** (this file).

### Changed

- **`guide.md` §4** rewritten around the v2 governed pack: model placement on
  the review/gate roles, and the field lessons reframed as "what each stage of
  `factory-governed@4` is for" — now including the round 2/3 lessons: a veto
  needs a repair path, a reviewer's findings must actually reach the fix path
  (verify `result_path` + `steps.<role>.output` wiring), and a reproduced
  finding must become a failing test (a gate), not prose the integrator reads.
- **`examples/README.md`** lists `governed-pack/` as the recommended v2 path
  and clarifies which examples target the legacy config-driven engine.

### Known follow-ups (not in this release)

- **Engine observability:** `agent.invoke` substitutes `fallbackOutput`
  silently when a role's result is missing/invalid
  (`internal/activity/builtin/agent.go`). A review step can degrade to
  "didn't run" and still show green. Emit a signal (log + step field) on
  fallback substitution. This is the one Go-level lesson the benchmark proved
  necessary; see `benchmark/results/round3-r1.md`.
- **Engine v2-awareness:** `orq-lite doctor` (via `config.Resolve`) still
  imposes the legacy `parser`/`coder`/`tester`/`critic`/`reviewer` role set even
  for a v2-pack-only project (`internal/config/config.go`). A pure-v2 team gets
  a spurious "missing orchestrated role" failure; the `governed-pack` example
  works around it by declaring the legacy roles. `doctor` should resolve
  against the installed pack's referenced roles when a v2 flow is present.
- **Round-4 hardening:** a `regression_forge` step that materializes each
  adversary/critic reproduction into a failing test in `tests/` before the
  repair loop, so the gates — not a prose-reading reviewer — hold the line.
  Designed and documented; not yet validated on a run.
