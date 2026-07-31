# Governed pack v4 — batch implementation (T1–T14)

Spec: `docs/superpowers/specs/2026-07-30-governed-pack-v4-design.md`

## Engine (Go)

- [x] T1 — `metadata.policy` field + compile-time resolution + `loadWorkflowPolicy`
      precedence (`--policy` > `metadata.policy` > `DefaultPolicy()`) + `policy_source`
      token on `flow run`.
- [x] T3 — semver ordering (`internal/flow/version.go`) + `pack[@V]/flow@V` refs +
      error listing installed versions.
- [x] T4 — `WhileSpec.MaxIterations` becomes `Value` (literal stays compatible).
- [x] T6 — `executeWhile` re-resolves the bound every pass; `whileIterationCeiling = 1000`.
- [x] T7 — `config.<key>` resolver namespace + `validateReference` case + `Runtime.Config`.
- [x] T8 — fail-fast validation of every `config.*` ref before `runtime.Start`.
- [x] T9 — `gate.assert@1` builtin (`EffectPure`).
- [x] T12 — `orq-lite pack list`.
- [x] T13 — `flow run` notice about an existing unfinished run of the same flow.
- [x] (prereq for T5) `minimum`/`maximum` in the JSON Schema subset — the pinned
      schema validator rejects unknown keywords, so `iteration_budget`'s bounds do
      not validate without it.

## Pack content

- [x] T2 — `policies/development@3.json` (attempt-free) + `metadata.policy` on all 7 flows.
- [x] T5 — `schemas/workflow-state@2.json` with `iteration_budget` + all 8 refs + prompts.
- [x] T6b — the 3 plan-driven `while` steps read `item.state.iteration_budget`.
- [x] T10 — every pack gate reads `config.lint_argv` / `config.test_argv`;
      `ticket_plan_complete` becomes `gate.assert@1`; `grep -rn "uv " examples/governed-pack/` = 0.
- [x] T11 — hardened `ticket-planner.md`, `pack.json` version 4, final digest regen.
- [x] T14 — docs stop demanding `--policy` discipline.

## Gates

- [x] `go vet ./...`
- [x] `go test ./...`

## Review

All 14 tickets landed. `go vet ./...` and `go test ./...` both exit 0.

Verified live, not just in tests:
`orq-lite pack install examples/governed-pack/pack` → `installed development@4`;
`orq-lite pack list` → `development@4 digest=cc26fe… files=35 (default)`;
`orq-lite flow run development/pr-review@1` **without `--policy`** →
`policy=policy:development@3 policy_source=flow-metadata`. That last line is the
finding the whole spec exists for.

Parity gates: `grep -rn "uv " examples/governed-pack/pack/` = 0; all 7 flows
carry `metadata.policy`; no `workflow-state@1` refs remain in flows/subflows.
Each is also asserted by a test, so they cannot rot silently.

### Two additions the spec did not budget for

1. **`minimum`/`maximum` in the schema subset.** `DecodeSchema` uses
   `DisallowUnknownFields`, so `workflow-state@2`'s `"minimum": 1, "maximum": 200`
   would not have loaded at all. T5 is impossible without it.
2. **Array element access in the resolver** (`steps.<id>.output.<n|last>`).
   The spec notes the resolver cannot index arrays and then asks for a
   `gate.assert@1` on a value only reachable through a `while` aggregate. Without
   indexing, neither Python gate could be removed and T10's zero-`uv` bar was
   unreachable. Added narrowly (decimal index or `last`), and the compiler
   descends into the pinned element schema so these paths stay type-checked.

### Deviations worth a reviewer's eye

- **`ticket-planner.md`'s round3 merge is a reconstruction, not a diff.** The
  file `benchmark/round3/pack-development-4/pack.json` lists by digest was never
  committed (confirmed: absent from the tree and from `git log --all`). The
  hardened-`completed` instruction was rebuilt from the design doc's prose. The
  wording is mine and should be read on its own merits.
- **`integrated-review@1` was touched**, which "Lo que este spec no hace" says it
  would not be. Its `governance_gate` was the second `uv run python -c` in the
  pack, and T10/T11's parity gate demands zero. It is now
  `gate.assert@1` on `steps.governance_repair.output.last.approved`, guarded by
  `if: steps.governance.output.approved != true` — semantically identical to
  reading the latest `gov_reviewer.json` (skipped when the first review already
  approved; otherwise the last repair cycle's verdict must be `true`).
- **`team.json` keeps `uv ` in `lint_command`/`full_test_command`.** The design
  explicitly says to leave those string keys alone for engine v1, which conflicts
  with a literal `grep -rn "uv " examples/governed-pack/` = 0. The parity gate is
  enforced over `pack/` — the thing that ships and that uninstalling cannot fix.
- **The bound is resolved *after* the condition**, not before. Resolving first
  fails a loop whose final carried value no longer carries the bound's path. The
  set of executed passes is unchanged either way.
- **`TestFlowCLIMissingPackHasActionableError` changed its expected message.**
  An unpinned ref no longer searches for a pack version taken from the flow, so
  the old text named a version it never looked for. The test's intent (an
  actionable error) is intact.
- **The version bump forced a path sweep**: every
  `.orquestalite/packs/development/1/prompts/...` in both `team.json` files and
  `guide.md` now points at `/4/`. A prior plan avoided bumping specifically to
  dodge this; bumping was T11's explicit instruction.
