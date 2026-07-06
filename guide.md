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

## 0. Preconditions

Verify before touching anything:

- The project is a **git repository** with a clean working tree (`git status`).
  Factory mode refuses a dirty tree; per-task commits need a repo.
- `orq-lite` is on PATH (`orq-lite version`).
- The provider CLIs you plan to use (`claude`, `codex`, `gemini`, `opencode`)
  are installed **and authenticated headless** — an agent that falls back to an
  interactive OAuth prompt is skipped at runtime, not waited on.
- `gh` is authenticated if any flow pushes branches or opens PRs.

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

## 4. Optional: governance loop (`factory_governed`-style flows)

For a governed flow (fast per-feature build + an approval loop where review
roles propose `new_tasks` until all approve — see
`examples/fastapi-governed/`):

- Declare the extra roles (`architect`, `qa`, `pm`, …) in `team.json` like any
  role; `config.Resolve` surfaces any declared role to configuration-driven
  flows, no code changes needed.
- Write one prompt per role with **deliberately non-overlapping lenses**
  (structure / test depth / scope) and a strict output contract
  (`status: approved|changes_requested` + `new_tasks`). Adapt the example
  prompts: replace the project-layout section with the real tree, and name the
  real gate commands so reviewers can run them.
- In the flow, surface the deterministic suite result to the reviewers (e.g.
  `TESTS_PASS={pytest_res.pass}`) and instruct them they may not approve over a
  red suite — the hard gate must stay deterministic, not vibes.
- Copy the gate commands into every `command` step of the flow (flows do not
  read `full_test_command`), using the same repo-root-relative form.
- Remember the engine's `factory_extract_features` splits `features.md` by
  `## ` headings deterministically — the LLM planner pass only exists in the
  built-in `orq-lite factory` command, not in `flow run` flows.

### Governance blind spots (field lessons — bake these in from day one)

A governed run that converged in one round with unanimous approvals still
shipped 10 confirmed bugs on its first real project. Root causes, and the
countermeasure for each:

1. **An ungated stage is advisory.** If phase 1 runs
   `coder → lint → tester → critic` linearly, a tester `fail` or critic
   `rejected` doesn't stop the commit — the verdict is decoration. Wrap the
   stage in `retry_until {lint_res.pass} && {tester_res.pass} &&
   {critic_res.pass}` so vetoes route feedback back to the coder.
2. **Spec-anchored reviewers share one blind spot.** Architect/QA/PM all judge
   against the features file; a defect the spec never mentions gets three
   unanimous approvals. Add an **adversarial reviewer** role whose prompt reads
   the raw `git diff base...HEAD` with an explicit hunt list of spec-blind
   failure modes: partial-update data loss (`exclude_unset`), alternative write
   paths bypassing router-level validation, broken existing consumers of
   changed contracts, boundary math (inclusive windows, divisors, unit
   constants), name-vs-id joins, N+1s. Gate the loop on its approval too.
3. **"Out of scope" is not "allowed to break".** A backend-only batch can
   still break the frontend (a new default that fails new validation on
   payloads the UI already sends). Make "existing consumers keep working" an
   implicit acceptance criterion in the PM prompt and the features preamble,
   and name the consumer entry points (UI api client, MCP servers) explicitly.
4. **QA must exercise old flows black-box, not read new tests.** The batch's
   own tests always pass by construction. Require QA to start the app and run
   the pre-existing happy paths against every touched surface.
5. **Conventions must encode hard rules, not style.** A generic style guide
   prevents none of the above. Put the project's failure-mode rules (partial
   updates, validation depth, single-source constants, id joins) in the
   `conventions_file` so the coder/critic see them on every invocation.
6. **Batch size bounds review depth.** The governance loop has a fixed round
   budget for the whole batch, so 3–5 tightly-cut features per batch converge
   and get real scrutiny; fifteen do not. To queue several dependent batches
   in one unattended run, use a multi-batch flow (an outer loop over features
   files, each batch getting its own governance budget — see
   `factory_governed_multi` in `examples/fastapi-governed/`).

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
- [ ] (governed) extra roles + non-overlapping prompts + `TESTS_PASS` surfaced,
      phase 1 gated by `retry_until`, adversarial reviewer in the approval
      condition, consumers-keep-working rule in PM prompt + preamble
- [ ] `features.md`: preamble contract, one `## ` per vertical slice, checkable
      acceptance criteria, dependency order, 3–5 per batch
- [ ] `orq-lite doctor` all green, everything committed, then run
