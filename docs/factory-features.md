# Factory features backlog

Input for `orq-lite factory docs/factory-features.md`. Each `## ` heading below
is one feature; everything under it (until the next `## `) is that feature's
plan. This preamble is ignored by the parser. See `docs/features.md` for the
full human-readable spec.

## Skill definitions and explicit skill requests

Let a project define skills as versioned markdown files in a `skills/`
directory, each holding a name, a description, and a procedure. Add a new
package `internal/skills` that discovers and parses every file in `skills/` and
exposes a lookup by skill name.

Propagate explicit skill requests from the plan to the task: a plan/feature can
name required skills via a `skills:` field or inline marker, and the
parser/planner carries those names onto every `tasks.Task`. When a role is
invoked, `invoke.Role` (in `internal/invoke/role.go`) loads the named skills'
content and injects it as `{{SKILLS}}` in the coder/critic prompt, exactly the
way `roleVars["MEMORY"]` and `roleVars["CONVENTIONS"]` are injected today. Add a
`{{SKILLS}}` placeholder to `prompts/coder.md` and `prompts/critic.md`. Naming a
skill that does not exist on disk is a clear, immediate error.

Acceptance criteria:
- The loader discovers and parses a skill file (name, description, procedure).
- A task that requests a skill receives that skill's content injected into the
  role prompt.
- A request for a non-existent skill fails with a clear message.

## Per-project-type pre-commits with rule checks

Extend the existing project-type detection in
`internal/commands/testdetect.go` to select a pre-commit rule set appropriate to
the detected language: Go -> gofmt + go vet + golangci-lint; Python -> ruff +
formatting + typecheck (in the style of the prek gate); JS/TS -> eslint +
prettier + tsc.

Add a scaffolding step (`orq-lite init --precommit`, or a dedicated subcommand)
that writes a `.pre-commit-config` matching the detected type and sets the lint
command in `team.json`. Route the rule checks through the lint gate that already
runs inside the fix loop (`FixConfig.LintGate` in `internal/loops/fix.go`): on a
violation the gate's feedback is injected as `{{LINT_FEEDBACK}}` and the loop
returns to the coder, instead of a raw git hook aborting the agent's commit.

Acceptance criteria:
- For a Go repo, the Go rule set is written and the lint gate is configured.
- For a Python repo, the Python rule set is written.
- A violation triggers feedback to the coder inside the fix loop (not a raw hook
  abort).

## review command: PR code review with the critic role

Add an `orq-lite review` command that, given a PR (a number resolved through
`gh`, or an explicit `--base`/`--head` ref pair), assembles the diff with
`internal/gitx` and invokes the critic role through `invoke.Role` with a
review-oriented prompt `prompts/review.md` that receives the diff as `{{DIFF}}`.
The role returns approved/rejected plus observations.

Post the verdict and observations as a PR review via `gh pr review` (reusing the
existing `gh` wrapper pattern from `factorycmd.go`) and archive the result under
`.orquestalite/results/`.

Acceptance criteria:
- The diff is assembled correctly between two refs.
- The review result is parsed correctly.
- Publication via `gh` posts the observations (with `gh` mocked).

## intake role and issue to run flow

Add an `intake` role to `team.json` (agent chain + `prompts/intake.md` +
`result_path`) that takes a GitHub issue body and returns `actionable` (bool)
plus either `missing_info` or a derived plan.

Add an `orq-lite intake --issue <file>` command: if the issue is not actionable,
write `missing_info` so it can be commented back on the issue; if it is
actionable, dump the derived plan to `plan.md`, run the parser to produce
`tasks.json`, and trigger `orq-lite run` for one or more tasks. All state lives
in files under `.orquestalite/`, reusing the existing `Plan` and `run` machinery.

Acceptance criteria:
- An actionable issue produces `tasks.json` and reaches the run loop.
- An incomplete issue emits `missing_info`.
- The intake contract parses correctly.

## watch command: per-project watcher daemon for issues and PRs

Add an `orq-lite watch <project>` command that runs a long-lived loop for a
single project (one daemon per active project, where the agents are already
authenticated). On startup it reads from the registry / management plane which
watch types are enabled for the project (PRs and/or issues) and polls only those.

Every interval (default 60s) it queries GitHub through the already-authenticated
`gh` (no tokens in config): new/updated issues since the last cursor trigger
intake; new/updated PRs since the last cursor trigger review. Persist the cursor
(`last_seen` per type) and the set of processed items in
`.orquestalite/watch.json` so an item is never processed twice. By default skip
PRs that orq-lite itself opened, with a `--review-own-prs` flag to review them.
Depends on the review and intake features.

Acceptance criteria:
- The cursor advances and items are not reprocessed.
- With only issues enabled, PRs are not polled.
- A new issue triggers intake.
- A new PR triggers review.
- An already-processed item is skipped.
