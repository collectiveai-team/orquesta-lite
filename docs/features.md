# Features

Planned features for `orq-lite`. Each entry describes the capability, how it
fits the existing architecture, and the acceptance tests that prove it.

---

## 1. Skill definitions and explicit skill requests

### Summary

Let a project define **skills** as versioned markdown files in a `skills/`
directory. A plan (or feature) can name the skills a task requires, and that
request is propagated from the plan all the way down to each task. When a role is
invoked, the named skills' content is injected into the coder/critic prompt as
`{{SKILLS}}`, exactly the way `{{MEMORY}}` and `{{CONVENTIONS}}` are injected
today.

### Design

- **Skill file format.** Each skill is one markdown file under `skills/`, holding
  three things: a `name`, a `description`, and a `procedure`. Skills are checked
  into the repo so they are versioned with the project.
- **Loader.** A new package `internal/skills` discovers and parses every file in
  `skills/`, exposing a lookup by skill name. Parsing mirrors the lightweight
  front-matter/section style already used elsewhere.
- **Explicit request propagation.** The plan/feature can name required skills via
  a `skills:` field (or an inline marker). The parser/planner carries those names
  onto every `tasks.Task` it produces, so the request travels plan → task.
- **Injection.** `invoke.Role` loads the content of the named skills and
  interpolates it as `{{SKILLS}}` in the role prompt, alongside the existing
  `roleVars["MEMORY"]` and `roleVars["CONVENTIONS"]` substitutions in
  `internal/invoke/role.go`. Prompts (`prompts/coder.md`, `prompts/critic.md`)
  gain a `{{SKILLS}}` placeholder.
- **Failure mode.** Naming a skill that does not exist on disk is a clear,
  immediate error — not a silent skip.

### Affected components

- New: `internal/skills` (loader + parser).
- `internal/tasks/tasks.go` — add the skill names carried on each `Task`.
- Parser/planner result types (`results.ParserTask` / planner) — surface the
  requested skill names.
- `internal/invoke/role.go` — resolve skills and inject `{{SKILLS}}`.
- `prompts/coder.md`, `prompts/critic.md` — add the `{{SKILLS}}` placeholder.

### Acceptance tests

- The loader discovers and parses a skill file (name, description, procedure).
- A task that requests a skill receives that skill's content injected into the
  role prompt.
- A request for a non-existent skill fails with a clear message.

---

## 2. Per-project-type pre-commits with rule checks

### Summary

Extend the existing project-type detection so that pre-commit configuration and
rule checks are appropriate to the detected language. Violations are routed
through the **lint gate** that already runs inside the fix loop, so a failure is
handed back to the coder to fix — instead of a raw git hook aborting the agent's
commit.

### Design

- **Detection.** Build on `internal/commands/testdetect.go`
  (`detectTestCommand` / `detectLintCommand`) to pick a per-type rule set:
  - **Go** → `gofmt` + `go vet` + `golangci-lint`
  - **Python** → `ruff` + formatting + typecheck (in the style of the `prek`
    gate)
  - **JS/TS** → `eslint` + `prettier` + `tsc`
- **Scaffolding.** A scaffolding step writes a `.pre-commit-config` matching the
  detected type — exposed as `orq-lite init --precommit` (or a dedicated
  subcommand).
- **Routing through the lint gate.** The detected rule checks become the
  `FixConfig.LintGate` that already runs after the coder and before the tester in
  `internal/loops/fix.go`. On a violation (`ok=false`) the gate's feedback is
  injected as `{{LINT_FEEDBACK}}` and the loop returns to the coder, rather than
  letting a hook hard-abort the commit.

### Affected components

- `internal/commands/testdetect.go` — per-type rule-set selection.
- `init` scaffolding (`cmd/orq-lite`, `internal/commands`) — write
  `.pre-commit-config` and set the lint command in `team.json`.
- `internal/loops/fix.go` — wire the detected checks into `LintGate`.

### Acceptance tests

- For a Go repo, the Go rule set is written and the lint gate is configured.
- For a Python repo, the Python rule set is written.
- A violation triggers feedback to the coder inside the fix loop (not a raw hook
  abort).

---

## 3. `review` command: PR code review with the critic role

### Summary

A new `orq-lite review` command that, given a PR, builds the diff and runs the
**critic** role against it to produce an approve/reject verdict plus
observations. The result is posted as a PR review via `gh pr review` and archived
under `results/`.

### Design

- **Input.** Either a PR number (resolved through `gh`) or an explicit
  `--base`/`--head` ref pair.
- **Diff assembly.** Use `internal/gitx` (`ShowCommit` / a base..head diff
  helper) to construct the unified diff between the two refs.
- **Invocation.** Call the critic through `invoke.Role` with a review-oriented
  prompt `prompts/review.md` that receives the diff as `{{DIFF}}`. The role
  returns `approved` / `rejected` plus observations.
- **Publish.** Post the verdict and observations as a PR review with
  `gh pr review`, reusing the existing `gh` wrapper pattern (see
  `factorycmd.go`).
- **Archive.** Store the review result under `.orquestalite/results/` like other
  role outputs.

### Affected components

- New subcommand `review` in `cmd/orq-lite/main.go` + `internal/commands`.
- New prompt `prompts/review.md` with a `{{DIFF}}` placeholder.
- `internal/gitx` — base..head diff helper if not already present.
- `gh` wrapper — add `gh pr review` publishing.

### Acceptance tests

- The diff is assembled correctly between two refs.
- The review result is parsed correctly.
- Publication via `gh` posts the observations (with `gh` mocked).

---

## 4. `intake` role and issue → run flow

### Summary

An **intake** role and an `orq-lite intake` command that turn a GitHub issue
body into actionable work. The role decides whether an issue is actionable; if
not, it emits the missing information; if it is, it emits a derived plan that is
parsed into tasks and handed to the run loop.

### Design

- **Role.** Add an `intake` role to `team.json` (agent chain +
  `prompts/intake.md` + `result_path`). Given an issue body, it returns
  `actionable` (bool) plus **either** `missing_info` **or** a derived plan.
- **Command.** `orq-lite intake --issue <file>`:
  - Not actionable → write `missing_info` so it can be commented back on the
    issue.
  - Actionable → dump the derived plan to `plan.md`, run the parser to produce
    `tasks.json`, and trigger `orq-lite run` for one or more tasks.
- **State.** All state lives in files, consistent with the rest of the tool
  (`.orquestalite/`).

### Affected components

- `team.json` — new `intake` role.
- New prompt `prompts/intake.md`; new result type for the intake contract.
- New subcommand `intake` in `cmd/orq-lite/main.go` + `internal/commands`,
  reusing the existing `Plan` and `run` machinery.

### Acceptance tests

- An actionable issue produces `tasks.json` and reaches the run loop.
- An incomplete issue emits `missing_info`.
- The intake contract parses correctly.

---

## 5. Per-project watcher daemon: polling issues and PRs

### Summary

An `orq-lite watch <project>` command that runs a long-lived loop for a single
project (one daemon per active project, where the agents are already
authenticated). It polls GitHub for new/updated issues and PRs and routes them to
**intake** (Feature 4) and **review** (Feature 3) respectively, with strict
idempotency.

### Design

- **One daemon per project.** Each active project runs its own watcher. The
  daemon reads from the registry / management plane which watch types are enabled
  for the project (PRs and/or issues) and polls only those.
- **Polling.** Every interval (default `60s`) it queries GitHub through the
  already-authenticated `gh` — **no tokens in config**:
  - new/updated issues since the last cursor → **intake**
  - new/updated PRs since the last cursor → **review**
- **State & idempotency.** The cursor (`last_seen` per type) and the set of
  processed items are persisted in `.orquestalite/watch.json`. An item is never
  processed twice.
- **Own-PR handling.** By default the watcher skips PRs that `orq-lite` itself
  opened; a `--review-own-prs` flag opts into reviewing them.

### Affected components

- New subcommand `watch` in `cmd/orq-lite/main.go` + `internal/commands`.
- New state file `.orquestalite/watch.json` (cursor + processed set).
- Registry / management plane — per-project enabled watch types.
- Depends on Feature 3 (`review`) and Feature 4 (`intake`).

### Acceptance tests

- The cursor advances and items are not reprocessed.
- With only issues enabled, PRs are not polled.
- A new issue triggers intake.
- A new PR triggers review.
- An already-processed item is skipped.

---

## Suggested implementation order

1. **Feature 1 (skills)** and **Feature 2 (pre-commits / lint gate)** are
   independent and can land in parallel.
2. **Feature 3 (review)** — standalone; foundation for the watcher.
3. **Feature 4 (intake)** — standalone; foundation for the watcher.
4. **Feature 5 (watch)** — depends on Features 3 and 4.
