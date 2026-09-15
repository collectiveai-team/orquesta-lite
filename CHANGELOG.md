# Changelog

All notable changes to orq-lite are recorded here. Versions follow the git
tags cut as GitHub releases (the binary's `--version` is stamped from the tag).

## v0.7.2 — a killed agent is not a reviewer

### Fixed

- **A timed-out agent's placeholder was accepted as its answer.** `classify`
  tested `ResultExists` before `TimedOut`, so any file at `result_path` made the
  timeout branch unreachable. Every reviewer prompt in the development pack tells
  the agent to write a schema-valid scaffold *before* it starts work, so a
  reviewer killed at its `timeout_seconds` left exactly such a file behind — and
  the engine recorded the step `succeeded` with it.

  What that looked like in the run that surfaced it: the `adversary` role was
  killed at 1500s with its backend suite still going; the step output became
  `{"status":"partial","decision":"inconclusive","summary":"Review in progress"}`;
  `cost_usd` was `0.0`; and `governance` received it as `ADVERSARY_REVIEW` — a
  verdict from a review that never happened. Schema validation could not catch it,
  because `review-result@2` admits `partial` and `inconclusive` by design. The run
  continued for another fifteen minutes and died at `review_coverage`, a gate
  several steps downstream whose message names neither the role nor the timeout.

  The precedence was collateral, not a decision about timeouts. It arrived with
  the fix for false `rate_limit` and `auth_failed` detections — both of which are
  read out of the agent's own stdout, so an agent that merely printed "usage
  limit" while working was being benched. A written result genuinely disproves a
  text heuristic. It does not disprove `context.DeadlineExceeded` on the
  subprocess, which is ground truth that the process was killed mid-turn.

  A result file now outranks the text heuristics and yields to the clock. The
  fallback chain fires — `case r.TimedOut` existed to trigger it and had been
  unreachable — so a second agent gets the chance to produce a real review. If the
  chain is exhausted, a step that declares a `fallbackOutput` records that instead
  (for the pack's review steps: `status: "unavailable"`, limitation "Provider did
  not produce a valid review checkpoint", which is true), and a step that declares
  none fails as `timeout` at the role that timed out.

- **The timeout error now says what happened.** The message for an exhausted
  chain read `agent "x" (role "y") did not write <path>`, which was not even true
  — the agent had written, and the engine discarded it. It now names the limit,
  the elapsed time and the discarded file: `agent "claude_sonnet" (role
  "adversary") was killed at its 25m0s timeout after 25m0s; the partial
  .orquestalite/results/adversary.json it had written was discarded`. The artifact
  is still on disk under `runs/<run>/agents/`, and without that sentence it is
  indistinguishable from one the engine accepted.

- **A killed attempt is no longer free.** Token usage was read only from the
  provider's terminal result message, which a killed process never emits, so
  twenty-five minutes of opus work priced at exactly `0.0` and `maxCostUSD` never
  saw it. Any run that times out roles repeatedly could overrun its cost budget
  without the budget registering it. Claude's per-turn usage is now captured from
  each assistant message as a distinct `partial_usage` event, and used only when
  no terminal total arrived — the two are never summed, so a completed run prices
  exactly as before. Each partial is a real API call that was really billed, so
  this is a measurement, not the guess `runSpendUSD` refuses to make.

### Known gaps

- A step that fails terminally stays failed across `flow resume`, by design of
  durable execution. Re-running it still means deleting the row from `step_runs`.
- The default and pack retry policies allow `timeout: maxAttempts 2`, which
  retries the whole fallback chain. That is bounded by the run's
  `maxDurationSeconds`, but a role with a long timeout and several agents can
  spend a large fraction of it failing. Left as configured rather than changed
  underneath existing packs.
- Only the Claude adapter reports per-turn usage. A killed attempt on `codex`,
  `gemini`, `opencode` or `agy` still prices at zero.

## v0.7.1 — a declared default now produces a value

### Fixed

- **An input default was a permission, not a value.** `validateInputs` read
  `spec.Default` only to decide that a missing input was not an error, and then
  moved on without writing anything. Declaring a default therefore produced an
  absent key, so every `$ref` to that input dangled.

  Nothing caught it at the top level, because a second code path covered for it:
  the CLI walks `ir.Inputs` and pre-fills defaults into the map before calling
  `Runtime.Start`. A subflow has no such path. Its `with` block *is* the entire
  scope its steps resolve against — there is no outer map to fall back to — so an
  input the call site omitted simply did not exist.

  That is how v0.7.0 shipped a `factory-governed@2` that could not finish a
  single ticket in its non-batch mode. `develop-ticket@1` declares `commit_type`
  with a default of `feat`; `factory-governed@2` and `task-list@1` never passed
  it; `commit_ticket` reads `inputs.commit_type`. A run died with
  `reference "inputs.commit_type" not found` after 32 minutes — after the coder,
  the QA verdict and both gates had passed — at the step that read the input,
  arbitrarily far from the call site that omitted it. `--fast` runs were unaffected
  only because `fast-batch@1` has no commit step.

  `flow validate` was right to stay green. `commit_type` is a declared input and
  the reference is legal; the runtime was the side breaking the contract.

  Defaults are now bound into the input map — for the top-level flow and for
  every subflow call site — before the inputs are marshalled, so the row a resume
  replays from records the values the run actually used, and before they are
  validated, so a default has to satisfy its own schema like any other value. A
  run with no defaulted inputs persists byte-identical inputs to before.

- **A subflow call site's contract is now checked when the flow compiles.** Two
  mistakes at a call site used to survive `flow validate` and surface only when
  some step deep inside the subflow read the input. Omitting an input the subflow
  requires aborted the run partway through. Misspelling one was worse: the stray
  key was silently discarded, the subflow fell back to its default, and the run
  succeeded doing something nobody asked for.

  Both are compile errors now, naming the step and the input. Together with the
  binding fix this closes the class rather than the instance: a reference to
  `inputs.x` inside a subflow already had to name a declared input, a declared
  input either carries a default the engine binds or must be supplied at the call
  site, and so every such reference is guaranteed to resolve before the run
  starts.

  The shipped pack needed no changes to satisfy this — `factory-governed@2` and
  `task-list@1` legitimately lean on `commit_type`'s default, and `issue-fix@1`
  overrides it with `fix`.

- **The runtime had no subflow tests.** That is the gap this defect came through.
  There are now regression tests covering a default bound at a subflow boundary,
  a call site value taking precedence over a default, a default reaching a
  top-level flow without the CLI's help, a default rejected by its own schema,
  and a pack-level assertion that every subflow call site in the shipped pack
  supplies or defaults every input the subflow's steps actually read.

## v0.7.0 — the pack a project runs, and a commit per green ticket

### Added

- **A project is told when its pack has fallen behind the binary.** Pack
  improvements ship inside the existing pack version — the version names the
  contract, and the repo's own convention is to edit flows and prompts in place
  rather than bump it. `init` installs with write-if-missing, so until now a
  project scaffolded months ago kept running the pack that release wrote, and
  updating `orq-lite` changed nothing about it. Silently. That is the worst
  shape for this: the flow still compiles, the run still succeeds, and the
  improvement simply never arrives.

  `orq-lite` now records the release that wrote each installed pack in
  `.orquestalite/pack-state.json` and compares it before every `flow run`. When
  the versions differ *and* files would actually change, the run stops before it
  exists and names the three ways forward: `orq-lite pack sync <name>` takes this
  binary's copy, `orq-lite pack keep <name>` stays put until the next orq-lite
  update, and `flow run --accept-pack-drift` continues this once and records
  nothing. Blocking is the only form the question can take — orq-lite runs
  headless under an agent, and a notice on stdout is a notice an agent scrolls
  past.

  Two cases deliberately stay quiet. A release that leaves the pack untouched
  re-stamps and says nothing: a prompt with no answer behind it teaches readers
  to dismiss the ones that matter. And a binary built without release ldflags
  reports version `dev` and never blocks, so developing orq-lite does not gate
  anyone's run.

  `pack sync` overwrites rather than merges, which is safe because it has nothing
  to destroy: `flow.LoadPack` verifies every file digest on every run, so a
  hand-edited pack does not load in the first place. Customizing means forking
  the pack under another name and `pack install`ing it. `pack sync --dry-run`
  reports what would change without touching disk, and a sync that produced a
  pack the verifier rejects fails rather than recording itself as current.

- **`activity:git.commit@1` lands one commit and reports refusal as data.** The
  ticket loop now commits each ticket that passed QA and both gates, so a run
  leaves a reviewable commit per ticket instead of one undifferentiated worktree.

  A commit could not simply be a gate. A failed gate aborts the run, which is
  right for a red test suite and wrong for a commit hook: most hook failures are
  a formatter rewriting the files it was handed. So `git.commit@1` stages,
  commits, and — when the commit is refused but the worktree changed — re-stages
  and tries once more. `end-of-file-fixer` and `trailing-whitespace` are resolved
  entirely by that retry, with no agent turn spent.

  What the retry cannot fix is reported in the step's output rather than raised:
  `blocked`, the exit code, and the hook's own stdout and stderr. `develop-ticket@1`
  hands that to the `coder` role as `{{COMMIT_FAILURE}}` and then retries the
  commit with `requireCommitted: true`, which *is* a gate. A reproduced failure
  that is only described in prose gets signed off; one promoted to a blocking
  gate does not. If the hooks are still red after the repair pass, the run stops
  there instead of stacking further tickets onto a tree that cannot commit.

  The commit message is assembled by the activity as Conventional Commits
  (`type(scope): subject`), cut to its first line and 72 characters, so a ticket
  title copied verbatim cannot produce a message a `commit-msg` hook rejects. The
  type is per-flow: `issue-fix@1` commits as `fix`, everything else as `feat`.
  `commit_ticket` runs unconditionally and takes the QA verdict as an `enabled`
  input rather than carrying an `if` — a skipped step resolves to nil and `&&`
  does not short-circuit, so guarding it would make the two steps that depend on
  its output fail to resolve and kill the run.

### Fixed

- **A test now pins which prompt placeholders no step supplies.** Interpolation
  leaves an unsupplied `{{VAR}}` in the prompt verbatim, so a role whose prompt
  reads an input the flow never wires receives the literal text `{{QA_REVIEW}}`
  where its findings belong. Nothing caught that. A new test walks every flow and
  subflow in the pack and asserts that each step supplies every placeholder its
  role's prompt reads.

  Twenty such gaps already exist, all of them introduced with the review-result
  rework: the shared review block at the foot of `qa.md`, `critic.md`,
  `adversary.md`, `visual-verifier.md` and `pr-reviewer.md` names four review
  slots that `integrated-review@1`, `fast-batch@1` and `pr-review@1` never pass.
  They are recorded in an explicit allowlist rather than papered over, because
  what each block should receive — an empty string, a real review, or no block at
  all in that prompt — is a question about the review pipeline's design, not
  about the wiring. The list is a ratchet: it may shrink, and any new gap fails
  the build.

- **The parallel-foreach test proved concurrency with a sleep.**
  `incrementExecutor` slept 10ms inside each invocation and asserted that two
  were active at once. That is not synchronization: on a loaded runner the first
  worker can enter, sleep and leave before the second starts, so the count stays
  at one and the test fails for a reason that has nothing to do with the
  scheduler. It is also the exact pattern `ticket-qa.md` instructs reviewers to
  treat as blocking. The sleep is replaced by a rendezvous — each invocation
  waits until the expected number are inside together — bounded by a timeout so
  a scheduler that has genuinely stopped running work in parallel fails the
  assertion instead of hanging the suite.

- **Adding an activity no longer has to be remembered twice.** The CLI and the
  web dashboard each built their own list of built-in activity specs. A flow
  using an activity only one of them knew about did not fail loudly — it vanished
  from the dashboard's catalog. Both now call `builtin.Specs()`.
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
