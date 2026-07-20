# Agent guide: initializing orq-lite in a new repo

This is the checklist an agent (or a human in a hurry) should follow to take an
arbitrary project from "no orchestration" to "ready to run `orq-lite factory` /
`orq-lite flow run`". It is ordered: later steps assume earlier ones passed.

The single most important idea: **the deterministic gates must be green at HEAD
before the first run**. The whole control loop (fix loop, merge gate, governance
approval) is built on `full_test_command` and `lint_command` exit codes. If they
are red for pre-existing reasons, every feature inherits that failure, the coder
drowns in feedback about code it never touched, and no review loop can converge.
Budget real time for step 2 — it is where most of the setup work actually is.

**Which engine?** For governed, production-grade builds, use the durable **v2
runtime** and start from [`examples/governed-pack/`](./examples/governed-pack/)
(`development/factory-governed@1`) — §4 covers it. The legacy config-driven
engine (`orq-lite factory`, root `flows.json`) is fine for quick per-feature
batches and the review/issue flows. Steps 0–3 and 5 apply to both; §4 is where
they diverge.

## 0. Preconditions

Verify before touching anything:

- The project is a **git repository** with a clean working tree (`git status`).
  Factory mode refuses a dirty tree; per-task commits need a repo.
- **`orq-lite` is on PATH** (`orq-lite version`). If it isn't installed, install
  it first (see below), then re-check.
- The provider CLIs you plan to use (`claude`, `codex`, `gemini`, `opencode`)
  are installed **and authenticated headless** — an agent that falls back to an
  interactive OAuth prompt is skipped at runtime, not waited on. Verify one is
  authenticated (e.g. `claude -p "ok" | head`) rather than assuming.
- `gh` is authenticated if any flow pushes branches or opens PRs
  (`gh auth status`).

### Installing `orq-lite` (only if missing)

Prefer a published release binary; fall back to building from source if you
have the Go toolchain and no matching release.

```bash
# 1) Release binary (recommended) — pick your platform from the releases page
#    and put orq-lite on PATH. See the repo README "Install" section for the
#    exact curl/checksum commands.
orq-lite version || echo "not installed"

# 2) From source (needs Go ≥ the version in go.mod)
go install github.com/lionelchamorro/orquestalite/cmd/orq-lite@latest
#    or, inside a checkout:  go build -o ~/bin/orq-lite ./cmd/orq-lite
```

Then confirm `orq-lite version` prints and continue.

## 1. Scaffold

```bash
orq-lite init          # writes team.json, prompts/, schemas/, .orquestalite/ + .gitignore entry
```

`init` autodetects the project type and proposes `full_test_command`
(Makefile `test:` target, `pyproject.toml`, `package.json`, `go.mod`, …). For an
unrecognized layout it leaves the command empty rather than guessing wrong —
treat an empty gate as a TODO, not a feature.

Commit `team.json`, `prompts/`, and `flows.json` (project configuration);
`.orquestalite/` stays gitignored (runtime state).

### 1b. Choose the flow(s) for this repo

Pick what fits the work in front of you — you can wire more than one. Match the
repo's needs against the shipped reference configs
([`examples/`](./examples/)):

| If the goal is… | Use | Keyed by |
|---|---|---|
| **Ship features to production, with real review** | `development/factory-governed@1` — the governed v2 pack ([`examples/governed-pack/`](./examples/governed-pack/)) | `features.md` |
| Build features fast, light review, per-feature batches | `factory_fast` (root `flows.json`, see [`examples/go-hello-api/`](./examples/go-hello-api/)) | `features.md` |
| Continuously fix incoming GitHub issues | `issue_fix` ([`examples/issue-fix/`](./examples/issue-fix/)) — reproduce with a failing test → fix until green → PR | `issue_number` |
| Review incoming PRs | `pr_review` ([`examples/pr-review/`](./examples/pr-review/)) — critic + security lenses → one verdict | `pr_number` |
| Grind a checklist plan to done | `ralph_loop` ([`examples/ralph-loop/`](./examples/ralph-loop/)) | `plan.md` |

Decision rule of thumb: **if the output is code someone will run in production,
default to the governed v2 pack** (§4) — its adversary + repair loop is what
stops the class of bug that green tests and a spec-anchored reviewer both miss.
Use the lighter flows for throwaway work, internal tooling, or when a human
reviews every diff anyway. The review/issue flows (`pr_review`, `issue_fix`)
are orthogonal — wire them alongside whatever build flow you choose.

What to add per choice: the governed pack needs its own install + strong
reviewer models (§4); the legacy flows need their `flows.json` + role prompts
copied from the matching example and adapted to your tree. Either way, §2–§3
(green gates, `team.json`) come first.

## 2. Make the baseline green (the critical step)

Run the gates **yourself, from the repo root, with the exact commands you will
put in `team.json`** — not from memory of how the project "usually" runs.

### 2a. Pin the toolchain

Version resolution must be deterministic, or the agents will hit a different
environment than you tested. Typical failure: an unpinned Python project where
`uv` picks the newest installed interpreter and a C-extension dependency has no
wheel for it, so the very first `uv run` dies compiling. Pin explicitly:
`.python-version` / `.nvmrc` / `.tool-versions` / `rust-toolchain.toml`, and a
committed lockfile. In a repo with **two competing lockfiles** (e.g. both
`package-lock.json` and `pnpm-lock.yaml`), decide the package manager now and
delete the loser — otherwise every agent re-decides it at random.

### 2b. Get the test suite passing

Run the full suite once. Fix everything that keeps it from being green at HEAD,
in this order:

1. **Collection/import errors first** — a suite that cannot even collect (a bad
   model definition, a missing plugin) blocks everything behind it.
2. **Missing test dependencies** — config that references a plugin
   (`asyncio_mode`, coverage, custom markers) the dependency set doesn't
   actually include. Add the dep where the gate command will find it.
3. **Environment coupling** — tests must be self-contained: in-memory or
   throwaway databases, no live services, no ordering dependencies, no secrets.
   A suite that needs a running Postgres or a `.env` you have locally will fail
   for every agent. If some tests genuinely need infra, scope them out with
   markers/config **in the project config** (so the tester and the gate agree),
   not in the command line of one of them.

### 2c. Get lint to zero — honestly

A gate with hundreds of pre-existing violations is worse than no gate: the fix
loop feeds the entire output back to the coder as feedback on every attempt.
Triage in this order:

1. **Kill false positives with configuration**, not suppressions — e.g. ruff's
   `flake8-bugbear.extend-immutable-calls` for FastAPI `Depends(...)` defaults,
   or a line-length that matches the house style. Config fixes remove whole
   error classes legitimately.
2. **Apply the linter's safe auto-fixes** — then **re-run the tests**.
   Auto-fixes are not behavior-neutral: removing an "unused" import can delete
   a side-effect import (model registration, plugin loading) that the code
   depends on. If a test breaks after auto-fix, find the removed import and
   restore it with an explicit `# noqa: <correct-code>` and a comment saying
   why it must stay.
3. **Suppress the remaining legacy debt explicitly and visibly** — a documented
   per-rule `ignore` list (or a linter baseline file) with a TODO comment
   naming the debt and the date. Do not silently delete rules. Then queue the
   cleanup as an early feature for the factory itself: its acceptance criterion
   is "ignore list removed, linter green on the full rule set". The loop is
   good at exactly this kind of mechanical, verifiable work.

### 2d. Re-verify

Both commands must exit 0 from a fresh checkout state before you continue.

## 3. Configure `team.json`

- **Agents**: one entry per provider/model, `dangerously_skip_permissions: true`
  (agents must edit files unattended), a `rate_limit_pattern` per provider.
- **Roles**: bind each role to an ordered agent chain (primary + fallbacks on
  different providers, so a rate limit routes around instead of waiting).
  Spend the strongest model on `parser`, `critic`, `reviewer` (judgment);
  mid-tier is usually fine for `coder`/`tester` (volume).
- **Gates** — the commands you just proved green:
  - Must be runnable via `sh -c` from the **repo root**. In a monorepo, prefix
    with the subdir: `cd backend && uv run --extra dev pytest -q`.
  - Must carry their own dependency context (`--extra dev`, `--group dev`,
    `npx …`) — the agent's shell has no activated venv and no node_modules
    guarantee.
  - `full_test_command` = the merge gate (whole suite). `lint_command` = the
    in-loop quality gate. Keep the tester's scope and the full suite consistent
    (same markers/config), or the gate will fail on tests the tester never ran.
- **Monorepo result-path gotcha**: role result contracts are written to
  `.orquestalite/results/<role>.json` *relative to the repo root*, but agents
  in a monorepo `cd` into subdirs (`cd backend && pytest`) and some models
  then write the result relative to that CWD — the orchestrator sees
  `no-result` and burns a fallback invocation even though the work was done
  (look for a stray `backend/.orquestalite/` to confirm). The default prompts
  now spell this out; keep that warning if you customize them.
- **`conventions_file`**: point it at a house-style markdown doc; it is injected
  into coder/critic/reviewer prompts as `{{CONVENTIONS}}`. Without it, agents
  fall back to inferring style from surrounding code — fine, but a real doc
  converges faster. `docs/conventions/` in this repo has worked examples.

## 4. Governed builds: start from the `governed-pack` example

For anything you'd ship, don't hand-author a governance loop — start from
[`examples/governed-pack/`](./examples/governed-pack/), the durable **v2**
pack (`development/factory-governed@1`). It bakes in the field lessons below so
you don't rediscover them the hard way. Its shape:

```
plan_tickets (budget-sized)
└─ develop_tickets  [per ticket: coder → ticket_qa → replan]
integrated_review:
   lint → tests → qa → adversary → critic
   → integration_repair   [loop: reconcile findings]
   → gates → governance
   → governance_repair     [loop ×2: repair → gates → FRESH re-audit]
   → governance_gate       (fail-closed)
```

To adopt it: install the pack under `.orquestalite/packs/development/1/`, point
your `team.json` roles at its prompts, set real reviewer models (see below),
and write your `features.md` (§5). The pack's own README has the exact copy/run
commands.

**Model placement is the whole game.** The review roles are where bugs get
caught — across three benchmark rounds they were ~78% of a governed run's cost,
and that spend *is* the product, not overhead. Put a strong coder on
`coder`/`integrator` and **Opus (or your best model) on the test/gate roles**:
`ticket_qa`, `qa`, `adversary`, `critic`, `gov_reviewer`. A cheap reviewer is a
decorative reviewer.

### Why the flow is shaped this way (field lessons — earned, not theorized)

A governed run that converged in one round with unanimous approvals still
shipped 10 confirmed bugs on its first real project. Later rounds shipped a
crash and a data race *past* an approving governance. Each countermeasure below
is now a piece of `factory-governed@1` — the list doubles as "what each stage
is for":

1. **An ungated stage is advisory.** A linear `coder → lint → tester → critic`
   doesn't stop a commit on a `rejected` verdict — the verdict is decoration.
   *In the pack:* the per-ticket loop retries until lint, ticket_qa, and the
   gates pass; vetoes route feedback back to the coder.
2. **Spec-anchored reviewers share one blind spot.** Reviewers who all judge
   against `features.md` unanimously approve a defect the spec never named — in
   the benchmark, the same *class* of bug shipped every round, just moving from
   "timezones" to "concurrent writers" as the spec got tighter. *In the pack:*
   the **`adversary`** role sets the spec aside and hunts the system's *shape*
   — shared state, concurrent writers, lifecycles, I/O boundaries — reproducing
   each hypothesis against running code. A finding only counts with a
   reproduction.
3. **A veto needs a repair path.** A fail-closed governance that just kills the
   run wastes a finding its own auditor documented to the line; a build died
   over a 10-line fix. But you can't let the fixer's opinion re-approve its own
   work. *In the pack:* the **governance repair loop** feeds findings to the
   integrator, re-runs the gates, and calls a **fresh** governance invocation —
   up to twice, then the run still dies. Separate *who repairs* from *who
   re-approves*.
4. **A reviewer's findings must actually reach the fix path.** This one bit the
   author: a mis-wired role wrote its verdict to the wrong file, the runtime
   substituted the "didn't run" fallback, and the real findings vanished
   silently while the run went green. *Lesson:* verify each review role's
   `result_path` and the flow's `steps.<role>.output` wiring; a step that
   "succeeded" on a fallback is not a step that reviewed anything.
5. **A reproduced finding must become a gate, not prose.** Even correctly
   wired, an adversary finding handed to the integrator as *text* got a
   plausible-but-partial fix that governance rubber-stamped — the bug shipped.
   The durable fix is the `issue-fix` pattern: turn each reproduction into a
   **failing test** in `tests/`, and let the standard gates hold the line —
   red until the real fix lands, and governance can't approve past a red suite.
   (This is the round-4 hardening; wire it in as a step that materializes
   reproductions into tests before the repair loop.)
6. **QA must exercise old flows black-box, not read new tests.** A build's own
   tests pass by construction. The `qa` role starts the app and runs
   pre-existing happy paths against every touched surface.
7. **Conventions encode hard rules, not style.** Put the project's failure-mode
   rules (partial updates, validation depth, single-source constants, id joins,
   "existing consumers keep working") in the `conventions_file` so every role
   sees them on every invocation — a generic style guide prevents none of the
   above.
8. **Scope bounds review depth.** Budgets are per-run, so 3–5 tightly-cut
   features (or the planner's small tickets) get real scrutiny; fifteen broad
   ones do not. Let the planner split infra concerns (streaming, workers,
   lifecycle) into their own tickets, and size the `coder` timeout to your
   heaviest one.

*Authoring your own flow instead?* The legacy `flows.json` path
(`examples/fastapi-governed/`, config-driven engine) still works; declare extra
roles in `team.json` (`config.Resolve` surfaces any declared role, no code
changes), give each a non-overlapping lens and a strict
`status: approved|changes_requested` contract, copy gate commands into every
`command` step (flows don't read `full_test_command`), and surface the
deterministic suite result so reviewers can't approve over a red suite. But the
v2 pack already does all of this correctly — prefer it.

## 5. Write `features.md` so the loops can converge

- **Preamble before the first `## `**: the global contract — stack, gate
  commands, cross-feature conventions. The queue extractor ignores it, but
  governance/review roles read the whole file.
- **One `## ` heading = one feature = one vertical slice**: independently
  shippable, cutting through every layer it needs (schema → API → UI → test),
  one observable outcome, no conjunctions in the title.
- **Acceptance criteria must be mechanically checkable**: exact endpoints,
  status codes, response shapes, file paths, and the tests that must exist.
  Reviewers walk these bullets literally; a vague bullet produces either
  endless `changes_requested` rounds or a false approval.
- **Order = dependency order**: features run sequentially; foundations first.
- **Small batches**: each feature is implemented in one batched coder call and
  the governance loop has a bounded number of rounds for the whole batch.
  3–5 tightly-cut features per run converge; ten broad ones do not.
- **UI features**: in flows there is no visual-verify pass, so express frontend
  acceptance as something a gate can check (e.g. Playwright specs), or run
  those slices through `orq-lite factory` which has browser-driven visual
  verification for `visual: true` slices.

## 6. Preflight and first run

```bash
orq-lite doctor        # team.json resolves, prompts exist, CLIs + credentials, gates set
git add -A && git commit -m "chore: orq-lite setup"   # factory requires a clean tree
orq-lite plan plan.md  # optional: one cheap parser call proves the provider wiring end to end
```

Then launch (`orq-lite factory features.md`, or
`orq-lite flow run <flow> features_path=features.md base_branch=main`) and watch
the dashboard. Interrupted or failed queues resume with `orq-lite factory`
(`--resume` to retry failures, `--replan` to re-decompose).

## Quick checklist

- [ ] git repo, clean tree; `orq-lite`, provider CLIs, `gh` authenticated headless
- [ ] `orq-lite init`; config committed, `.orquestalite/` ignored
- [ ] toolchain pinned (interpreter version, single lockfile)
- [ ] full test suite green at HEAD, self-contained (no live infra)
- [ ] lint at zero: false positives configured away, auto-fixes applied **and
      re-tested**, legacy debt explicitly baselined + queued as a cleanup feature
- [ ] `team.json`: agent fallback chains, repo-root self-contained gate
      commands, `conventions_file`
- [ ] (governed) started from `examples/governed-pack/` (v2 `factory-governed@1`);
      pack installed under `.orquestalite/packs/development/1/`, strong models on
      the test/gate roles (`ticket_qa`/`qa`/`adversary`/`critic`/`gov_reviewer`),
      each review role's `result_path` + `steps.<role>.output` wiring verified,
      `coder` timeout sized to the heaviest ticket
- [ ] `features.md`: preamble contract, one `## ` per vertical slice, checkable
      acceptance criteria, dependency order, 3–5 per batch
- [ ] `orq-lite doctor` all green, everything committed, then run
