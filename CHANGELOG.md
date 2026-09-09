# Changelog

All notable changes to orq-lite are recorded here. Versions follow the git
tags cut as GitHub releases (the binary's `--version` is stamped from the tag).

## v0.6.2 — isolated runs and durable cancellation

### Fixed

- **Agent sessions and result files are isolated per workflow run.** Session
  task keys now include the run, scope, foreach item, and step identities, and
  each invocation attempt receives its own result path. Re-running a flow can
  no longer inherit a Claude or Codex conversation from an earlier run.
- **Cancellation, deadlines, and executor ownership are enforced end to end.**
  A workflow observes its durable canceled state, applies its original global
  deadline after resume, terminates provider subprocess trees, and prevents a
  second executor from driving the same state database concurrently.
- **Governed review output is aggregated semantically.** Reviewers now report
  complete, unavailable, and not-applicable coverage explicitly; incomplete
  coverage cannot be interpreted as approval or turned into a product defect.

### Changed

- Run IDs use 128 bits of cryptographic randomness and fail closed if entropy
  is unavailable. The governed pack is upgraded to version 6 while retaining
  the embedded version 5 assets required by pinned configurations.

## v0.6.1 — doctor asks the CLI instead of guessing at the Keychain

### Fixed

- **`doctor` verifies the Antigravity session instead of warning that it
  cannot.** v0.6.0 reported `credentials:agy` as unverifiable on every run,
  because Antigravity keeps its session in the macOS Keychain and there is no
  file to stat. A permanent warning is one a reader learns to scroll past, and
  this one did worse than that: it convinced a reader that a working provider
  was unavailable. The premise was wrong anyway — `agy models` posts to
  `loadCodeAssist`, so the CLI can answer the question the file system cannot.
  `doctor` now runs it, bounded to 8 seconds, and reports
  `credentials:agy session verified`. A failed probe is still a warning, never
  a failure, and it quotes the CLI's first line: from outside, a closed session
  and an unreachable network look the same, and refusing a preflight because
  the link is down would be worse than the warning it replaces. A test pins
  that any future provider whose credentials live outside the file system must
  ship a probe with it, so the silent case cannot come back.

## v0.6.0 — Antigravity provider, Go CI, and a runner that keeps every line

### Added

- **`provider: agy` drives the Antigravity CLI.** The adapter runs `agy` in
  headless print mode (`--print=<prompt> --output-format stream-json`) and maps
  the CLI onto the shared agent options: `model` to `--model`, `effort` to
  `--effort`, `dangerously_skip_permissions` to
  `--dangerously-skip-permissions`, `safe_mode` to `--sandbox`, and session
  resume to `--conversation` with the `conversation_id` the `init` event
  reports. Four shapes of this CLI differ from the sibling providers and are
  handled deliberately:

  - **The prompt travels in argv, not stdin.** `agy` print mode ignores stdin
    and wants the prompt attached to the flag, so it goes out as a single
    `--print=<prompt>` element. Splitting it in two would let a prompt that
    opens with `--` read as an emitted flag to doctor's CLI drift check.
  - **`--print-timeout=24h` is always emitted.** The CLI's own print deadline
    defaults to 5m, far below the 900-2400s role timeouts a flow uses, so
    every long invocation would be cut short from inside. The runner already
    kills the subprocess at its own deadline; the invocation keeps one clock.
  - **Reasoning effort lives in the model id.** `gemini-3.8-flash-high` already
    selects it, and the CLI rejects a `--effort` that disagrees
    (`--model gemini-3.8-flash-high conflicts with --effort=low`), while a bare
    `gemini-3.8-flash` requires the flag. The adapter emits `--effort` only for
    an unsuffixed model, and fails the build on a contradiction so it surfaces
    in `doctor` instead of as a dead invocation mid-run.
  - **`thinking_tokens` is already inside `output_tokens`, and
    `cache_read_tokens` is outside `input_tokens`.** Measured on a real run:
    input 5195 + output 472 equals the reported total of 5667, with thinking
    309 and cache_read 8128 alongside. Adding the first or subtracting the
    second would misreport the ledger, so the mapping does neither.

  The provider is deliberately absent from doctor's `credentialPaths`: `agy`
  keeps its login in the OS keychain, and a file probe there would report a
  PASS it cannot support.

- **Provider names are pinned to their executable.** `doctor` resolves a
  provider binary with `LookPath(providerName)` and the run preflight resolves
  it with `binary := agent.Provider`, so a provider whose name differs from
  its CLI passes every unit test, fails `doctor` with `not on PATH`, and gets
  marked unreachable at run start — surfacing as `all agents for role X are
  marked skipped`, which reads as a role misconfiguration and is not one. The
  invariant was undocumented and is now a test over the whole registry:
  `CLIHelp().Args[0]` must equal `Name()`. It is why the Antigravity adapter
  is registered as `agy` rather than `antigravity`.

- **`doctor` reports a credential it cannot verify instead of saying nothing.**
  A provider absent from `credentialPaths` produced no `credentials:` check at
  all, and an operator reading a clean report could not tell "checked and fine"
  from "never looked". Providers whose session lives in the OS keychain are now
  listed separately and reported as `[WARN] credentials:agy cannot be verified
  from disk`. They stay out of `credentialPaths` on purpose: an entry there
  drives `ProviderHasUsableCredentials`, which the run-time static preflight
  uses to skip agents, so a keychain provider listed there would mark every one
  of its agents unusable and end a run with `all agents for role X are marked
  skipped` — against a provider that is in fact logged in.

### Fixed

- **The runner dropped the end of an agent's output, and a throttle with it.**
  `RunAgent` took `cmd.StdoutPipe`/`cmd.StderrPipe` and called `cmd.Wait` while
  the scanner goroutines were still reading. `Wait` closes those pipes the
  moment it sees the process exit, so whatever remained in the buffer was lost —
  roughly 10 KB of tail once the output exceeds the pipe buffer, deterministic
  and easy to reproduce. The tail is exactly where a CLI prints its rate-limit
  notice, its auth prompt and its error summary, so a truncated stderr was read
  as a permanent failure and surfaced as `all agents for role X are marked
  skipped` against a provider that was merely throttled, ending the run instead
  of waiting the window out.

  Draining before `Wait` is not the fix and the history records the attempt:
  `Wait` is also what reaps the process the context deadline kills, so blocking
  on the pipes first means a timeout never fires. The runner now creates its own
  `os.Pipe` pair for `cmd.Stdout`/`cmd.Stderr`. Pipes it owns are invisible to
  `Wait`, so the order that keeps cancellation working keeps the output whole,
  and the drain runs afterwards under a bounded grace shared by both pipes —
  the scanners only reach EOF when every write end closes, and a backgrounded
  grandchild inherits one, so an unbounded wait would stall the flow step.

- **The test suite depended on the developer's `~/.gitconfig`.** Seventeen tests
  run `orq-lite init`, which creates a repository and makes an initial commit.
  With no configured identity git guesses one from the OS account — a guess that
  works on a workstation and fails with `fatal: empty ident name` on a machine
  whose account has no full name. The suite now sets its own identity.

### Changed

- **Pull requests are gated on `gofmt`, `go build`, `go vet` and
  `go test ./... -count=1 -race`.** Nothing verified that a pull request compiled
  before this: the only check was `claude-review`, which failed on every pull
  request with "Claude Code is not installed on this repository" and was merged
  over each time. That workflow is removed — a check nobody can act on trains
  everyone to ignore the column that now carries a real signal. `claude.yml`
  stays; it only fires on an explicit `@claude` mention. The new gate carries no
  branch filter on `pull_request`, so a PR based on another feature branch is
  checked too. It found both defects above on its first two runs.

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
