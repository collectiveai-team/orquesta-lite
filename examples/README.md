# Examples

Reference configurations for the **configuration-driven flow engine** (`orq-lite flow run`).
Each example is a `team.json` (the agent/role definition) plus the work it operates on — a
`features.md` for the build flows, or a `pr_number` / `issue_number` for the review flows — and,
where it defines a non-shipped flow, its own `flows.json` and role prompts.

These are reference configs, not standalone projects. To run one, drop its files into a
project working directory next to the repo's `prompts/` (the helper scripts referenced
below do exactly that into a throwaway dir). Every example uses a **haiku-only** team so a
full run is cheap.

| Example | Flow | Keyed by | Highlights |
|---------|------|----------|------------|
| [`go-hello-api/`](./go-hello-api/) | `factory_fast` (shipped in root `flows.json`) | `features.md` | Per-feature batch build: parser → coder → tester → critic → commit → review |
| [`fastapi-governed/`](./fastapi-governed/) | `factory_governed` (bundled) | `features.md` | Adds a governance loop: iterate until **architect + QA + PM** approve, each filing tasks as feedback |
| [`pr-review/`](./pr-review/) | `pr_review` (bundled) | `pr_number` | Reviews a PR: **critic + security** lenses + test/lint gates → a `review_lead` posts one verdict |
| [`issue-fix/`](./issue-fix/) | `issue_fix` (bundled) | `issue_number` | Evidence-gated triage → **reproduce with a failing test → fix until green → PR** (branch is an empty-vs-single-element queue) |
| [`ralph-loop/`](./ralph-loop/) | `ralph_loop` (bundled) | `plan.md` checkboxes | Minimal ralph loop: **same coder prompt per task until the plan is done**, then an adversarial reviewer appends findings as new tasks and the loop reruns |

## Running an example

These examples spawn autonomous agents that read code, run commands, and (for the build and
fix flows) write code — the team sets `dangerously_skip_permissions: true` — so launch them
yourself. The general shape:

```sh
orq-lite flow run <flow-name> features_path=features.md base_branch=main --log-format verbose
```

`orq-lite` resolves `team.json` and `flows.json` from the current directory and prompt
paths relative to it. `git push` / `gh pr create` steps are tolerant (`on_failure: continue`),
so they no-op cleanly without a remote or `gh` auth.

See each example's README for stack prerequisites and the exact command.
