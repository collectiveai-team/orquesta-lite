# Example: `fastapi-governed`

A 3-feature FastAPI service built by a **`factory_governed`** flow (bundled here in
[`flows.json`](./flows.json)) that adds a **governance approval loop** on top of the
`factory_fast` style.

## What it builds

`features.md` defines three features:

1. **Health and Application Skeleton** — `create_app()`, `GET /`, `GET /health`.
2. **In-Memory Items CRUD** — Pydantic v2 models, repository/service layers, `POST/GET/GET{id}/DELETE /items`.
3. **Item Search and Pagination** — `q`/`limit`/`offset` query params with `422` validation.

## Flow: `factory_governed`

**Phase 1 — per-feature (fast style), one integration branch:**
`parser → coder → ruff → tester → critic → commit`, looped over the three features.

**Phase 2 — governance loop** (`retry_until {architect_res.pass} && {qa_res.pass} && {pm_res.pass}`, max 3 rounds):
`coder (addresses prior round's tasks) → ruff → pytest → commit → architect → qa → pm → eval`.

Each governance role writes `{"status": "approved" | "changes_requested", "new_tasks": [...]}`.
`status:"approved"` maps to a uniform `pass`, so the loop gates on all three. When any
returns `changes_requested`, its `new_tasks` are threaded into the **next round's coder**
via `{architect_res.new_tasks}` etc. (the engine blanks these tokens on round 1). `pytest`
is the deterministic gate, surfaced to architect/QA as `TESTS_PASS` so they can't approve
over a red suite. Three deliberately non-overlapping lenses: architect = structure,
QA = test depth, PM = scope.

```mermaid
flowchart TD
  A["action: extract features → features_queue"] --> IB["cmd: git checkout -b work branch"]
  IB --> LOOP
  subgraph LOOP["phase 1 · loop: per feature (fast build)"]
    direction TB
    P["agent: parser → tasks"] --> FB["action: format batch prompt"]
    FB --> C1["agent: coder → coder_res"]
    C1 --> R1["cmd: ruff check — continue"]
    R1 --> T1["agent: tester"]
    T1 --> CR1["agent: critic"]
    CR1 --> CM1["cmd: git add + commit"]
  end
  LOOP --> GOV
  subgraph GOV["phase 2 · retry_until — architect and qa and pm approve (max 3)"]
    direction TB
    C2["agent: coder ← prior-round new_tasks"] --> R2["cmd: ruff — continue"]
    R2 --> PT["cmd: pytest → pytest_res — continue"]
    PT --> CM2["cmd: git add + commit"]
    CM2 --> AR["agent: architect — structure"]
    AR --> QA["agent: qa — test depth"]
    QA --> PM["agent: pm — scope"]
    PM --> CH{"all three approved?"}
    CH -->|no| C2
    CH -->|yes| DONE(("approved"))
  end
  GOV --> PUSH["cmd: git push — continue"]
  PUSH --> PRC["cmd: gh pr create — continue"]
```

## Files

| File | Purpose |
|------|---------|
| `team.json` | Haiku agents; roles `parser, coder, tester, critic, reviewer` (reviewer is required by config but unused by this flow) plus the custom `architect, qa, pm`. Test gate `uv run pytest -q`, lint `uv run ruff check .`. |
| `features.md` | The three FastAPI features. |
| `flows.json` | The `factory_governed` flow. |
| `prompts/architect.md`, `qa.md`, `pm.md` | The custom governance role prompts. The standard `parser/coder/tester/critic/reviewer` prompts come from the repo's [`prompts/`](../../prompts/). |

> **Custom roles:** `architect`/`qa`/`pm` are not part of the engine's built-in role
> vocabulary — they work because `config.Resolve` surfaces any declared role for
> configuration-driven flows. That is the only code change this example required.

## Prerequisites

- `uv` (`brew install uv` or `curl -LsSf https://astral.sh/uv/install.sh | sh`) — the team
  uses `uv run pytest` / `uv run ruff`
- `claude` CLI authenticated

## Run it

Copy this config next to the repo's `prompts/` (merging the three custom prompts here into
that `prompts/` dir) in a fresh `uv` project with `fastapi`, `pytest`, `httpx`, `ruff`
installed, then:

```sh
orq-lite flow run factory_governed features_path=features.md base_branch=main --log-format verbose
```

## Caveats

- **No conditional/break in the engine:** the governance coder runs once even on round 1
  with empty feedback (a consistency pass).
- **`retry_until` aborts the flow if it never converges** within `max_retries` (3) — the
  intended "don't ship unapproved" behavior, so an unconverged run fails before the PR.
- **Feedback is text, not a durable backlog** — reviewer tasks are threaded into the next
  coder prompt, not persisted to `tasks.json`.
- Approval is ultimately the agents' (haiku) judgment, with `pytest` as the only hard gate.
