# Changelog

All notable changes to orq-lite are recorded here. Versions follow the git
tags cut as GitHub releases (the binary's `--version` is stamped from the tag).

## v0.3.5 — Watch v2 reaches the pack flows

### Fixed

- **`watch --engine=v2` could never trigger a flow.** The watch loop reports a
  polled item as six generic GitHub fields (`type`, `number`, `title`, `body`,
  `author`, `updated_at`) and forwarded all of them to the flow, but the pack
  flows it defaults to declare domain inputs — `issue-fix@1` takes `issue_path`
  and `run`, `pr-review@1` takes `pr`/`base`/`head`/`publish` — and `flow run`
  rejects undeclared inputs. Every trigger failed with `unknown input "author"`
  and the tick aborted. The trigger now narrows its payload to the inputs the
  compiled flow actually declares (the same IR the startup fail-fast compiles),
  materialising the issue at `.orquestalite/watch-issue-<n>.md` for `issue_path`
  and mapping the PR number and `--publish-prs` onto `pr`/`publish`. The
  narrowing is driven by the flow's IR rather than an allow-list, so a custom
  watch flow that does declare the generic fields still receives them.

## v0.5.0 — Provider usage guard, Claude-primary defaults

### Changed

- **The scaffolded `team.json` now defaults to a Claude-primary, Codex-fallback
  team on the current model generation.** The old scaffold mixed one Claude
  Sonnet 4.6 agent, one Opus 4.8 agent and a single `codex_gpt5` (`gpt-5.5`)
  that was the *primary* coder — so a project whose Codex auth could not reach
  that model failed every coding role before falling back. `init` now writes
  four agents — `claude_opus` (`claude-opus-5`), `claude_sonnet`
  (`claude-sonnet-5`), `codex_sol` (`gpt-5.6-sol`) and `codex_terra`
  (`gpt-5.6-terra`) — and pairs them per role tier: the roles that judge work
  (`ticket_planner`, `intake`, `qa`, `adversary`, `critic`, `gov_reviewer`,
  `pr_reviewer`) run `claude_opus` then `codex_sol`; the roles that produce it
  (`coder`, `batch_coder`, `integrator`, `ticket_qa`, `visual_verifier`) run
  `claude_sonnet` then `codex_terra`. Every role therefore has a two-provider
  chain, and `visual_verifier` — shipped in the pack but missing from the old
  scaffold — is declared. `qa`, `adversary`, `critic` and `integrator` get
  longer timeouts, since each now reads a whole diff. Existing `team.json`
  files are untouched: `init` only rewrites the prompt paths of an older pack
  version, never the agent list.
- **`provider: claude` falls back to `claude-sonnet-5`** when an agent declares
  no `model` (was `claude-sonnet-4-6`).
- `init` also warns when the `claude` CLI is missing, not only `codex`. Claude
  is now the primary of every scaffolded role, so its absence stops a run
  before the fallback can help.

### Fixed

- **The two Codex models that became the default fallback of every role had
  no price at all.** `gpt-5` was in the price map but not in the prefix list,
  so `gpt-5.6-sol` and `gpt-5.6-terra` matched nothing. `orq-lite cost`
  reported every Codex invocation as unpriceable, and `runSpendUSD` returns 0
  for an unknown model, so those runs contributed nothing to the workflow cost
  budget — the pack policy's `maxCostUSD` could never trip on a run that fell
  back to Codex, which is precisely the path the usage guard creates. Both
  models are now priced ($4/$20 and $2/$12 per MTok) and resolve dated
  snapshot ids by prefix. A bare `gpt-5` prefix was deliberately not added: it
  would price an unrecognised future `gpt-5.x` at another model's rate, and a
  guessed figure feeding a hard budget check is worse than none. A test now
  ties every model in the scaffolded `team.json` to the price table, so
  changing a default model cannot silently make it unpriceable again.

- **The embedded cost table priced Claude Opus 4.8 at the Opus 4 rate**
  ($15/$75 per MTok instead of $5/$25), overstating the cost of every run that
  used it by 3x. The rate is corrected, and `claude-opus-5` ($5/$25) and
  `claude-sonnet-5` ($2/$10) are added, so `orq-lite cost` reports a figure for
  the new default team instead of nothing.
- **`dangerously_skip_permissions` was a silent no-op on `provider: opencode`.**
  The adapter emitted `--dangerously-skip-permissions`, which is Claude's
  spelling; `opencode run` calls it `--auto`. Because opencode's argument parser
  ignores unknown flags and still exits 0, nothing surfaced the mistake: the
  team declared auto-approval, orq-lite believed it had applied it, and the
  agent ran with permission prompts active until the role timed out. The
  adapter now emits `--auto`.
- **`provider: codex` ignored `dangerously_skip_permissions` entirely** and
  always emitted `--dangerously-bypass-approvals-and-sandbox`, so an agent that
  explicitly declared `false` still ran fully unsandboxed. The flag is now
  emitted only when the field is true.

### Added

- **`limits.usage_guard` — a provider subscription guard that runs before an
  agent starts.** A role used to discover its provider was exhausted only by
  spending an invocation on it and reading the refusal, which burns the
  remaining budget to learn there is none. The guard reads local Claude and
  Codex usage first and skips any agent whose 5-hour or 7-day window is at or
  above `max_used_percent`, so the role advances to its next agent instead. It
  never waits for a reset: the decision is which agent to run, not when.
  Thresholds are per provider and per window, and the feature stays off until
  at least one provider is configured, so existing projects are unaffected.
  A partial reading is still enforced on the windows that were reported
  (`provider_usage_partial`); a reading older than `max_reading_age_seconds` is
  treated as no reading rather than a low one; and an unreadable source follows
  `on_unavailable`, which defaults to `fallback`. Custom `cmd` agents that
  wrap a provider declare it with `usage_provider`. Blocked invocations emit
  `provider_usage_blocked` with the window and its reset time.

- **`extra_args` per agent** — provider-only argv suffix, appended after the
  adapter's own flags (and before OpenCode's positional prompt). Unlike `cmd`,
  it keeps the provider contract intact: session resume, usage accounting,
  rate-limit detection, and JSON parsing all keep working. Flags the adapter
  owns (output format, model, session, permission mode) are rejected, since
  overriding them breaks the parser rather than the CLI call.
- **`orq-lite doctor` verifies every flag a provider will emit** against the
  installed CLI's own `--help`, for the agents the config actually references.
  This is what makes the two fixes above detectable instead of silent: opencode
  accepts unknown flags and exits 0, so drift between an adapter and a CLI
  release cannot be caught by running the command. Parsing is exact-token and
  declaration-anchored (a flag merely mentioned in prose does not count); an
  unreadable or unrecognizable help page reports "could not verify" rather than
  passing, and only a timeout degrades to a warning.
- **`orq-lite pack install <dir>`** — verifies a pack against its `pack.json`
  manifest (digests, no unlisted files, no symlinks) and installs it to
  `.orquestalite/packs/<name>/<version>/`, replacing the manual `cp -R`.
- **`benchmark/cutover-evidence.json`** — machine-readable cutover-gate
  evidence; `orq-lite cutover check` output is now the authoritative gap list.
- **`plan-tickets@1`** — planning-only flow; powers `orq-lite plan` alias.
- **`task-list@1`** — per-task develop loop; powers `orq-lite run` alias.
- **`factory-fast@1`** — standalone single-batch fast path (`factory-governed@1` reaches the same batch path via its `fast=true` input).
- **`issue-fix@1`** — triage → plan → develop; powers `orq-lite intake` alias and `watch --issues` default.
- **`pr-review@1`** — agent-driven PR review; powers `orq-lite review` alias and `watch --prs` default.
- **`fast-batch@1`** subflow — shared one-batch develop step extracted from `factory-fast@1` and `factory-governed@1` (when `fast=true`).
- **`team.json`: three new roles** — `batch_coder` (whole-backlog fast-path implementation), `intake` (issue triage), and `pr_reviewer` (end-to-end PR review) — with their prompts shipped inside the pack.

### Changed

- **Scaffolded and benchmark `team.json` now declare
  `"dangerously_skip_permissions": true` on the codex agent.** This keeps the
  shipped defaults behaving exactly as before the codex fix above. **Existing
  projects must do the same:** `codex exec` runs with `approval: never` and
  `sandbox: read-only`, so a codex agent that does not declare the field can no
  longer write files and will fail every implementation ticket. Review-only
  codex roles (`critic`, `gov_reviewer`) can be left read-only deliberately.
- **`orq-lite doctor`** no longer fails pack-only projects: a `team.json`
  without the legacy `parser`/`tester`/`reviewer` roles now resolves, with a
  `legacy roles` warn noting that only `plan`/`run`/`factory` need them.
- **`examples/governed-pack/team.json`** dropped its unused legacy shim roles.
- **`orq-lite watch --engine=v2`** now compiles the configured issue/PR flow refs at startup and exits with a clear error if one does not resolve, instead of surfacing the failure only when the first event fires.
- **`factory-governed@1` `governance` output** now carries the full
  `integrated_review` result object (previously the `.governance` sub-key);
  this resolves cleanly when `fast=true` skips the review step (nil sub-property
  navigation would previously cause a "reference not found" run failure).

## v0.2.3 — Governed pack example + guide overhaul

Docs and examples release. No engine behavior changes; the pinned binary is
rebuilt with the new version string.

### Added

- **`examples/governed-pack/`** — the recommended production setup for the
  durable v2 runtime: the `development/factory-governed@1` (originally shipped as @4; renumbered to @1) pack, self-contained
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
  `factory-governed@1` is for" — now including the round 2/3 lessons: a veto
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
