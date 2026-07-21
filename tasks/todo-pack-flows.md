# Governed Pack: 5 Missing Flows + Watch Fail-Fast Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the 5 flows the cutover gate requires (`plan-tickets`, `task-list`, `factory-fast`, `issue-fix`, `pr-review`) in `examples/governed-pack/pack/`, make the v2 CLI aliases and `watch` defaults actually resolvable, add a fail-fast flow validation to `watch --engine=v2`, and turn the cutover `development-pack` gate green.

**Architecture:** All new flows are authored in the governed pack's ticketed idiom, reusing the existing roles/subflows/schemas wherever possible (`ticket_planner`, `develop-ticket@1`, `workflow-state@1`, `review-result@1`). A new shared `fast-batch@1` subflow implements the batch path used by `factory-fast@1`, `task-list@1 (fast=true)`, and `factory-governed@1 (fast=true)`. Three new roles (`intake`, `pr_reviewer`, `batch_coder`) with pack prompts. A committed `regen-digests.py` keeps `pack.json` coherent after every pack edit.

**Tech Stack:** orq.dev/v2 flow JSON (no Go changes except watchcmd fail-fast + one regression test), Python 3 stdlib for the digest script.

## Global Constraints

- No new Go module dependencies. Every commit passes `go build ./... && go vet ./... && go test ./...`.
- Commit messages: conventional commits, each ending with the line `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.
- Branch: current branch `feat/bootstrap-and-governed-pack`.
- `benchmark/round2/`, `benchmark/round3/`, `benchmark/results/` are frozen — never edit. `benchmark/cutover-evidence.json` IS editable (Task 8 updates it).
- After ANY edit inside `examples/governed-pack/pack/`, run `python3 examples/governed-pack/regen-digests.py` (created in Task 1) so `pack.json` digests stay coherent — `flow validate` fails on a stale manifest.
- Flow input names MUST match what `aliases.go` passes exactly: `plan_path`+`append` (plan-tickets), `fast` (task-list), `features_path`+`fast`+`create_pr` (factory-governed), `issue_path`+`run` (issue-fix), `pr`+`base`+`head`+`publish` (pr-review). The runtime rejects unknown input keys.
- Every `agent.invoke` step MUST have a literal `"outputSchema": "schema:<name>@<version>"` string in `with` (a `$ref` there is a compile error).
- `vars` values must resolve to strings; pass booleans/objects via `context` (they are JSON-marshaled into the prompt).
- Gates use the pack's Python conventions: `["uv", "run", "ruff", "check", "."]` and `["uv", "run", "pytest", "-q"]`.
- Verified by spike (all compile): `if` conditions on `inputs.*`, `&&` in conditions, `if`+`while` on the same step, `{"$ref": ...}` elements inside `argv` arrays, `{"type":"boolean"}` / `{"type":"string"}` input schemas, `steps.<id>.output.stdout` property refs on `command.run` output.
- Before writing any new `agent.invoke` step that uses `ticket_planner` or reuses pack idioms (`fallbackOutput`, var names), FIRST read the corresponding existing step in `examples/governed-pack/pack/flows/factory-governed@1.json` / `subflows/integrated-review@1.json` and mirror its exact `vars`/`context` key names (they must match the `{{PLACEHOLDERS}}` in the prompt files). The JSON in this plan is the target shape; reconcile var names against the real prompts before committing.
- Validate each flow with: `go run ./cmd/orq-lite flow validate examples/governed-pack/pack/flows/<name>@1.json` (run the digest regen first). Expected output: `valid <name>@1 <digest>`.

---

### Task 1: Digest regen script + new schemas

**Files:**
- Create: `examples/governed-pack/regen-digests.py`
- Create: `examples/governed-pack/pack/schemas/flag@1.json`
- Create: `examples/governed-pack/pack/schemas/text@1.json`
- Create: `examples/governed-pack/pack/schemas/intake-result@1.json`
- Modify: `examples/governed-pack/pack/pack.json` (via the script)

**Interfaces:**
- Produces: `schema:flag@1` (boolean), `schema:text@1` (any string, empty allowed — unlike `path@1` which requires minLength 1), `schema:intake-result@1` (intake triage output). All later tasks consume these. The script is the canonical way to refresh `pack.json`.

- [ ] **Step 1: Write the regen script**

Create `examples/governed-pack/regen-digests.py`:

```python
#!/usr/bin/env python3
"""Regenerate the SHA-256 file digests in pack/pack.json.

Run from anywhere: python3 examples/governed-pack/regen-digests.py
Rewrites pack.json's "files" map from the actual pack/ directory contents
(every file except pack.json itself), preserving apiVersion/name/version.
"""
import hashlib
import json
import pathlib

pack_dir = pathlib.Path(__file__).resolve().parent / "pack"
manifest_path = pack_dir / "pack.json"
manifest = json.loads(manifest_path.read_text())

files = {}
for path in sorted(pack_dir.rglob("*")):
    if path.is_dir() or path == manifest_path:
        continue
    relative = path.relative_to(pack_dir).as_posix()
    files[relative] = hashlib.sha256(path.read_bytes()).hexdigest()

manifest["files"] = files
manifest_path.write_text(json.dumps(manifest, indent=2, sort_keys=False) + "\n")
print(f"pack.json: {len(files)} files digested")
```

- [ ] **Step 2: Write the three schemas**

`examples/governed-pack/pack/schemas/flag@1.json`:
```json
{"type": "boolean"}
```

`examples/governed-pack/pack/schemas/text@1.json`:
```json
{"type": "string"}
```

`examples/governed-pack/pack/schemas/intake-result@1.json`:
```json
{
  "type": "object",
  "properties": {
    "actionable": {"type": "boolean"},
    "summary": {"type": "string"},
    "plan": {"type": "string"},
    "missing_info": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["actionable", "summary", "plan", "missing_info"],
  "additionalProperties": false
}
```

- [ ] **Step 3: Regen digests and verify the pack still verifies + existing flows still compile**

```bash
python3 examples/governed-pack/regen-digests.py
go run ./cmd/orq-lite flow validate examples/governed-pack/pack/flows/factory-governed@1.json
go run ./cmd/orq-lite flow validate examples/governed-pack/pack/flows/review-existing@1.json
```
Expected: `pack.json: N files digested` (N = previous count + 3), then `valid factory-governed@1 …` and `valid review-existing@1 …`. Also sanity-check the script is byte-stable: run it twice; `git diff` after the second run must show no new changes.

- [ ] **Step 4: Commit**

```bash
git add examples/governed-pack/regen-digests.py examples/governed-pack/pack/schemas/flag@1.json examples/governed-pack/pack/schemas/text@1.json examples/governed-pack/pack/schemas/intake-result@1.json examples/governed-pack/pack/pack.json
git commit -m "feat(examples): pack digest regen script + flag/text/intake-result schemas

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: `plan-tickets@1` flow + ticket-planner prompt notes

**Files:**
- Create: `examples/governed-pack/pack/flows/plan-tickets@1.json`
- Modify: `examples/governed-pack/pack/prompts/ticket-planner.md` (two short additions)
- Modify: `examples/governed-pack/pack/pack.json` (via script)

**Interfaces:**
- Consumes: `schema:path@1`, `schema:flag@1`, `schema:workflow-state@1`, role `ticket_planner`.
- Produces: flow `development/plan-tickets@1` accepting `plan_path` (string) + `append` (bool) — the exact inputs `orq-lite plan <file> --engine=v2` passes. Output: `state` = the workflow-state.

- [ ] **Step 1: Read the existing planner step**

Open `examples/governed-pack/pack/flows/factory-governed@1.json` and copy its `plan_tickets` step's exact `vars`/`context` key set. The flow below is the target; keep whatever placeholder keys the real prompt uses (`{{MODE}}`, `{{FEATURES_PATH}}`, `{{CURRENT_STATE}}`, `{{IMPLEMENTATION}}`, `{{VERIFICATION}}`).

- [ ] **Step 2: Write the flow**

`examples/governed-pack/pack/flows/plan-tickets@1.json`:

```json
{
  "apiVersion": "orq.dev/v2",
  "kind": "Flow",
  "metadata": {"name": "plan-tickets", "version": "1"},
  "inputs": {
    "plan_path": {"schema": "schema:path@1", "default": "features.md"},
    "append": {"schema": "schema:flag@1", "default": false}
  },
  "steps": [
    {
      "id": "plan_tickets",
      "uses": "activity:agent.invoke@1",
      "with": {
        "role": "ticket_planner",
        "vars": {
          "MODE": "initial",
          "FEATURES_PATH": {"$ref": "inputs.plan_path"}
        },
        "context": {
          "APPEND": {"$ref": "inputs.append"},
          "CURRENT_STATE": "",
          "IMPLEMENTATION": "",
          "VERIFICATION": ""
        },
        "outputSchema": "schema:workflow-state@1"
      }
    }
  ],
  "outputs": {
    "state": {"$ref": "steps.plan_tickets.output"}
  }
}
```

- [ ] **Step 3: Prompt additions**

In `examples/governed-pack/pack/prompts/ticket-planner.md`, in the section describing `initial` mode (locate by reading the file), append this paragraph:

```markdown
If the context variable `APPEND` is `true`, do not discard existing planning:
read the previous state from `.orquestalite/results/ticket_planner.json` if it
exists, keep its completed/pending tickets, and append new tickets derived
from the contract after them (bump `revision`).
```

And at the end of the input-variables section:

```markdown
If a `TRIAGE` context variable is provided (issue-fix flow), its `plan` field
is the contract to decompose — treat it as the authoritative scope and use the
file at FEATURES_PATH only as supporting context.
```

- [ ] **Step 4: Regen, validate, commit**

```bash
python3 examples/governed-pack/regen-digests.py
go run ./cmd/orq-lite flow validate examples/governed-pack/pack/flows/plan-tickets@1.json
```
Expected: `valid plan-tickets@1 …`

```bash
git add examples/governed-pack/pack
git commit -m "feat(examples): plan-tickets@1 flow (v2 alias for orq-lite plan)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: `fast-batch@1` subflow + `batch_coder` role + `factory-fast@1` flow

**Files:**
- Create: `examples/governed-pack/pack/subflows/fast-batch@1.json`
- Create: `examples/governed-pack/pack/prompts/batch-coder.md`
- Create: `examples/governed-pack/pack/flows/factory-fast@1.json`
- Modify: `examples/governed-pack/team.json` (add `batch_coder` role)
- Modify: `examples/governed-pack/pack/pack.json` (via script)

**Interfaces:**
- Consumes: roles `qa`, `integrator` (existing), schemas `ticket-implementation@1`, `review-result@1`, `iteration-result@1`, `workflow-state@1`.
- Produces: `subflow:fast-batch@1` with inputs `features_path` (path) + `state` (workflow-state) — consumed by Tasks 4 and 5; flow `development/factory-fast@1`; role `batch_coder`.

- [ ] **Step 1: Write the batch-coder prompt**

Read `examples/governed-pack/pack/prompts/coder.md` first and mirror its structure/tone (result-file contract, gate commands, fail conditions). Create `examples/governed-pack/pack/prompts/batch-coder.md`:

```markdown
# Batch Coder

You implement the ENTIRE remaining ticket backlog in one coherent pass. This
is the fast path: one implementation batch instead of one ticket at a time.

## Inputs

- `WORKFLOW_STATE`: the full ticket plan JSON (workflow-state@1). Implement
  `next_ticket` AND every ticket in `pending`, in dependency order.
- `FEATURES_PATH`: the contract file. Acceptance criteria in the tickets are
  authoritative; the contract resolves ambiguity.
- `CONVENTIONS`: {{CONVENTIONS}}

## Rules

1. Work ticket by ticket internally, but produce one coherent change set.
2. Run the gates yourself before reporting: `uv run ruff check .` and
   `uv run pytest -q`. Fix failures before writing your result.
3. Write deterministic tests for every acceptance criterion — a test must
   fail if the behavior it covers regresses.
4. Never weaken or delete an existing test to make the suite pass.

## Result

Write JSON to `.orquestalite/results/coder.json` matching
ticket-implementation@1:

- `ticket_id`: "_batch"
- `complete`: true only if EVERY ticket's acceptance criteria are implemented
  and both gates exit 0.
- `summary`: what was built, ticket by ticket.
- `files_changed`: every file you touched.
- `gates`: each gate command with its exit code.
- `remaining`: tickets or criteria you could not finish (forces review to
  reject — never claim complete with a non-empty remaining).

WORKFLOW_STATE:
{{WORKFLOW_STATE}}

FEATURES_PATH: {{FEATURES_PATH}}
```

- [ ] **Step 2: Add the role to team.json**

In `examples/governed-pack/team.json`, add to `roles` (same shape as `coder`, result_path `.orquestalite/results/coder.json` is fine to share since only one runs at a time — but use its own to keep artifacts distinct):

```json
"batch_coder": {"agents": ["haiku"], "prompt": ".orquestalite/packs/development/1/prompts/batch-coder.md", "result_path": ".orquestalite/results/batch_coder.json", "timeout_seconds": 2400}
```

- [ ] **Step 3: Write the subflow**

Read `examples/governed-pack/pack/subflows/integrated-review@1.json` first and mirror its `qa` step's `fallbackOutput` idiom exactly. Create `examples/governed-pack/pack/subflows/fast-batch@1.json`:

```json
{
  "apiVersion": "orq.dev/v2",
  "kind": "Subflow",
  "metadata": {"name": "fast-batch", "version": "1"},
  "inputs": {
    "features_path": {"schema": "schema:path@1"},
    "state": {"schema": "schema:workflow-state@1"}
  },
  "steps": [
    {
      "id": "implement_batch",
      "uses": "activity:agent.invoke@1",
      "with": {
        "role": "batch_coder",
        "vars": {"FEATURES_PATH": {"$ref": "inputs.features_path"}},
        "context": {"WORKFLOW_STATE": {"$ref": "inputs.state"}},
        "outputSchema": "schema:ticket-implementation@1"
      }
    },
    {
      "id": "batch_lint",
      "uses": "activity:gate.run@1",
      "with": {"argv": ["uv", "run", "ruff", "check", "."]}
    },
    {
      "id": "batch_tests",
      "uses": "activity:gate.run@1",
      "with": {"argv": ["uv", "run", "pytest", "-q"]}
    },
    {
      "id": "batch_qa",
      "uses": "activity:agent.invoke@1",
      "with": {
        "role": "qa",
        "vars": {"FEATURES_PATH": {"$ref": "inputs.features_path"}},
        "context": {"IMPLEMENTATION": {"$ref": "steps.implement_batch.output"}},
        "outputSchema": "schema:review-result@1",
        "fallbackOutput": {"approved": false, "summary": "qa agent failed; fail closed", "findings": ["qa agent did not produce a verdict"]}
      }
    },
    {
      "id": "batch_repair",
      "uses": "activity:agent.invoke@1",
      "if": "steps.batch_qa.output.approved != true",
      "while": {
        "condition": "item.continue == true",
        "maxIterations": 3,
        "initial": {"continue": true, "summary": "resolve batch QA findings", "remaining": ["review batch QA findings"]}
      },
      "with": {
        "role": "integrator",
        "vars": {"FEATURES_PATH": {"$ref": "inputs.features_path"}},
        "context": {"QA": {"$ref": "steps.batch_qa.output"}, "PROGRESS": {"$ref": "item"}},
        "outputSchema": "schema:iteration-result@1",
        "fallbackOutput": {"continue": false, "summary": "integrator agent failed; stopping repair loop", "remaining": ["integrator did not report"]}
      }
    },
    {
      "id": "final_lint",
      "uses": "activity:gate.run@1",
      "with": {"argv": ["uv", "run", "ruff", "check", "."]}
    },
    {
      "id": "final_tests",
      "uses": "activity:gate.run@1",
      "with": {"argv": ["uv", "run", "pytest", "-q"]}
    }
  ],
  "outputs": {
    "implementation": {"$ref": "steps.implement_batch.output"},
    "qa": {"$ref": "steps.batch_qa.output"}
  }
}
```

Reconcile the `qa` and `integrator` context key names against `integrated-review@1.json`'s steps for those roles — the prompt placeholders must match; if the real prompts use different context keys (e.g. `{{QA_RESULT}}`), use those.

- [ ] **Step 4: Write the factory-fast flow**

Create `examples/governed-pack/pack/flows/factory-fast@1.json` (the `plan_tickets` step is the same as in Task 2, minus append):

```json
{
  "apiVersion": "orq.dev/v2",
  "kind": "Flow",
  "metadata": {"name": "factory-fast", "version": "1"},
  "inputs": {
    "features_path": {"schema": "schema:path@1", "default": "features.md"},
    "create_pr": {"schema": "schema:flag@1", "default": false}
  },
  "steps": [
    {
      "id": "plan_tickets",
      "uses": "activity:agent.invoke@1",
      "with": {
        "role": "ticket_planner",
        "vars": {"MODE": "initial", "FEATURES_PATH": {"$ref": "inputs.features_path"}},
        "context": {"CURRENT_STATE": "", "IMPLEMENTATION": "", "VERIFICATION": ""},
        "outputSchema": "schema:workflow-state@1"
      }
    },
    {
      "id": "fast_batch",
      "uses": "subflow:fast-batch@1",
      "with": {
        "features_path": {"$ref": "inputs.features_path"},
        "state": {"$ref": "steps.plan_tickets.output"}
      }
    },
    {
      "id": "publish_pr",
      "uses": "activity:command.run@1",
      "if": "inputs.create_pr == true",
      "with": {"argv": ["gh", "pr", "create", "--fill"]}
    }
  ],
  "outputs": {
    "plan": {"$ref": "steps.plan_tickets.output"},
    "batch": {"$ref": "steps.fast_batch.output"}
  }
}
```

- [ ] **Step 5: Regen, validate, commit**

```bash
python3 examples/governed-pack/regen-digests.py
go run ./cmd/orq-lite flow validate examples/governed-pack/pack/flows/factory-fast@1.json
python3 -c "import json; json.load(open('examples/governed-pack/team.json'))"
```
Expected: `valid factory-fast@1 …`; JSON parses.

```bash
git add examples/governed-pack
git commit -m "feat(examples): fast-batch@1 subflow, batch_coder role, factory-fast@1 flow

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: `task-list@1` flow

**Files:**
- Create: `examples/governed-pack/pack/flows/task-list@1.json`
- Modify: `examples/governed-pack/pack/pack.json` (via script)

**Interfaces:**
- Consumes: `subflow:develop-ticket@1` (existing — read its `with` contract in `factory-governed@1.json`'s `develop_tickets` step and mirror it exactly), `subflow:fast-batch@1` (Task 3).
- Produces: flow `development/task-list@1` with inputs `fast` (bool) + `features_path` (path, default features.md) — `orq-lite run --engine=v2` passes `fast` only; `features_path` uses its default.

- [ ] **Step 1: Write the flow**

Copy the `plan_tickets` and `develop_tickets` steps from `factory-governed@1.json` verbatim, then add the `if` conditions and `fast_batch` step. Target:

```json
{
  "apiVersion": "orq.dev/v2",
  "kind": "Flow",
  "metadata": {"name": "task-list", "version": "1"},
  "inputs": {
    "features_path": {"schema": "schema:path@1", "default": "features.md"},
    "fast": {"schema": "schema:flag@1", "default": false}
  },
  "steps": [
    {
      "id": "plan_tickets",
      "uses": "activity:agent.invoke@1",
      "with": {
        "role": "ticket_planner",
        "vars": {"MODE": "initial", "FEATURES_PATH": {"$ref": "inputs.features_path"}},
        "context": {"CURRENT_STATE": "", "IMPLEMENTATION": "", "VERIFICATION": ""},
        "outputSchema": "schema:workflow-state@1"
      }
    },
    {
      "id": "develop_tickets",
      "uses": "subflow:develop-ticket@1",
      "if": "inputs.fast != true",
      "while": {
        "condition": "item.state.status == \"active\"",
        "maxIterations": 20,
        "initial": {"state": {"$ref": "steps.plan_tickets.output"}}
      },
      "with": {
        "features_path": {"$ref": "inputs.features_path"},
        "state": {"$ref": "item.state"}
      }
    },
    {
      "id": "fast_batch",
      "uses": "subflow:fast-batch@1",
      "if": "inputs.fast == true",
      "with": {
        "features_path": {"$ref": "inputs.features_path"},
        "state": {"$ref": "steps.plan_tickets.output"}
      }
    },
    {
      "id": "final_lint",
      "uses": "activity:gate.run@1",
      "with": {"argv": ["uv", "run", "ruff", "check", "."]}
    },
    {
      "id": "final_tests",
      "uses": "activity:gate.run@1",
      "with": {"argv": ["uv", "run", "pytest", "-q"]}
    }
  ],
  "outputs": {
    "plan": {"$ref": "steps.plan_tickets.output"},
    "developed": {"$ref": "steps.develop_tickets.output"}
  }
}
```

If `factory-governed@1.json`'s `develop_tickets` `while`/`with` differs from the above (key names, maxIterations), mirror the existing file — it is the source of truth.

- [ ] **Step 2: Regen, validate, commit**

```bash
python3 examples/governed-pack/regen-digests.py
go run ./cmd/orq-lite flow validate examples/governed-pack/pack/flows/task-list@1.json
```
Expected: `valid task-list@1 …`

```bash
git add examples/governed-pack/pack
git commit -m "feat(examples): task-list@1 flow (v2 alias for orq-lite run)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: `factory-governed@1` gains `fast` + `create_pr`

**Files:**
- Modify: `examples/governed-pack/pack/flows/factory-governed@1.json`
- Modify: `examples/governed-pack/pack/pack.json` (via script)

**Interfaces:**
- Consumes: `subflow:fast-batch@1` (Task 3).
- Produces: `development/factory-governed@1` accepting `features_path` + `fast` + `create_pr` — the exact inputs `orq-lite factory --engine=v2` passes (today the extra two are rejected as unknown inputs).

- [ ] **Step 1: Edit the flow**

In `examples/governed-pack/pack/flows/factory-governed@1.json`:

1. Add to `inputs`:
```json
"fast": {"schema": "schema:flag@1", "default": false},
"create_pr": {"schema": "schema:flag@1", "default": false}
```
2. Add `"if": "inputs.fast != true"` to the `develop_tickets` step, the `ticket_plan_complete` step, and the `integrated_review` step (the ticketed path is skipped entirely in fast mode).
3. Insert AFTER `plan_tickets` (before `develop_tickets`) the fast path:
```json
{
  "id": "fast_batch",
  "uses": "subflow:fast-batch@1",
  "if": "inputs.fast == true",
  "with": {
    "features_path": {"$ref": "inputs.features_path"},
    "state": {"$ref": "steps.plan_tickets.output"}
  }
}
```
4. Append as the LAST step:
```json
{
  "id": "publish_pr",
  "uses": "activity:command.run@1",
  "if": "inputs.create_pr == true",
  "with": {"argv": ["gh", "pr", "create", "--fill"]}
}
```
5. Leave existing `outputs` untouched.

- [ ] **Step 2: Regen, validate BOTH flows that share subflows, commit**

```bash
python3 examples/governed-pack/regen-digests.py
go run ./cmd/orq-lite flow validate examples/governed-pack/pack/flows/factory-governed@1.json
go run ./cmd/orq-lite flow validate examples/governed-pack/pack/flows/review-existing@1.json
```
Expected: both `valid …`.

```bash
git add examples/governed-pack/pack
git commit -m "feat(examples): factory-governed@1 accepts fast/create_pr alias inputs

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: `issue-fix@1` + `pr-review@1` flows, `intake` + `pr_reviewer` roles

**Files:**
- Create: `examples/governed-pack/pack/flows/issue-fix@1.json`
- Create: `examples/governed-pack/pack/flows/pr-review@1.json`
- Create: `examples/governed-pack/pack/prompts/intake.md`
- Create: `examples/governed-pack/pack/prompts/pr-reviewer.md`
- Modify: `examples/governed-pack/team.json` (add `intake`, `pr_reviewer` roles)
- Modify: `examples/governed-pack/pack/pack.json` (via script)

**Interfaces:**
- Consumes: `schema:intake-result@1` (Task 1), `schema:text@1`, `schema:flag@1`, `subflow:develop-ticket@1`, role `ticket_planner`.
- Produces: `development/issue-fix@1` (inputs `issue_path`, `run`) and `development/pr-review@1` (inputs `pr`, `base`, `head`, `publish`) — matching the intake/review aliases and the `watch` defaults.

- [ ] **Step 1: intake prompt**

Read `internal/commands/assets/prompts/intake.md` for the legacy contract, then create `examples/governed-pack/pack/prompts/intake.md`:

```markdown
# Intake Triage

You triage one incoming issue and decide whether it is actionable as-is.

## Input

`ISSUE` is the raw issue body (title + description as filed):

{{ISSUE}}

House conventions: {{CONVENTIONS}}

## Your job

1. Read the issue and the repository. Reproduce the problem if it is a bug
   report and reproduction is cheap (run the failing command/test).
2. Decide: is there enough information to act without asking the reporter?
3. If actionable: write a precise implementation plan in markdown — scope,
   acceptance criteria, files likely touched. The plan is the full contract a
   planner will decompose; do not leave decisions open.
4. If not actionable: list exactly what information is missing.

## Result

Write JSON to `.orquestalite/results/intake.json` matching intake-result@1:

- `actionable`: boolean.
- `summary`: one-paragraph triage verdict.
- `plan`: the implementation plan markdown ("" when not actionable).
- `missing_info`: questions for the reporter ([] when actionable).
```

- [ ] **Step 2: pr-reviewer prompt**

Read `internal/commands/assets/prompts/critic.md` and `internal/commands/reviewcmd.go`'s `reviewBody` for the legacy contract, then create `examples/governed-pack/pack/prompts/pr-reviewer.md`:

```markdown
# PR Reviewer

You review one pull request end-to-end and optionally publish the verdict.

## Inputs (context)

- `PR`: PR number/URL ("" if reviewing a raw ref range instead).
- `BASE`, `HEAD`: git refs ("" means resolve them from the PR).
- `PUBLISH`: when true AND PR is set, post the review to GitHub yourself.

PR: {{PR}}
BASE: {{BASE}}
HEAD: {{HEAD}}
PUBLISH: {{PUBLISH}}

## Procedure

1. Resolve the diff range: if BASE/HEAD are empty and PR is set, run
   `gh pr view <PR> --json baseRefName,headRefName`. Then read the full diff
   (`git diff <base>...<head>`; fetch refs first if needed).
2. Review for correctness bugs, contract violations, missing/weakened tests,
   and convention drift ({{CONVENTIONS}}). Cite file:line for every finding.
3. Severity discipline: a finding must describe a concrete failure scenario.
   Style nits go last and never block on their own.
4. If PUBLISH is true and PR is set: post via
   `gh pr review <PR> --approve --body <verdict>` when approving, or
   `gh pr review <PR> --request-changes --body <verdict>` when not. The body
   must list every finding.

## Result

Write JSON to `.orquestalite/results/pr_reviewer.json` matching
review-result@1:

- `approved`: true only if nothing blocking was found.
- `summary`: verdict paragraph, including the diff range reviewed and whether
  the review was published.
- `findings`: one string per finding, "file:line — issue — why it matters".
```

- [ ] **Step 3: team.json roles**

Add to `roles` in `examples/governed-pack/team.json`:

```json
"intake": {"agents": ["haiku"], "prompt": ".orquestalite/packs/development/1/prompts/intake.md", "result_path": ".orquestalite/results/intake.json", "timeout_seconds": 900},
"pr_reviewer": {"agents": ["haiku"], "prompt": ".orquestalite/packs/development/1/prompts/pr-reviewer.md", "result_path": ".orquestalite/results/pr_reviewer.json", "timeout_seconds": 1200}
```

- [ ] **Step 4: issue-fix flow**

`examples/governed-pack/pack/flows/issue-fix@1.json`:

```json
{
  "apiVersion": "orq.dev/v2",
  "kind": "Flow",
  "metadata": {"name": "issue-fix", "version": "1"},
  "inputs": {
    "issue_path": {"schema": "schema:path@1"},
    "run": {"schema": "schema:flag@1", "default": true}
  },
  "steps": [
    {
      "id": "read_issue",
      "uses": "activity:command.run@1",
      "with": {"argv": ["cat", {"$ref": "inputs.issue_path"}]}
    },
    {
      "id": "triage",
      "uses": "activity:agent.invoke@1",
      "with": {
        "role": "intake",
        "context": {"ISSUE": {"$ref": "steps.read_issue.output.stdout"}},
        "outputSchema": "schema:intake-result@1",
        "fallbackOutput": {"actionable": false, "summary": "intake agent failed; fail closed", "plan": "", "missing_info": ["intake agent did not produce a verdict"]}
      }
    },
    {
      "id": "plan_fix",
      "uses": "activity:agent.invoke@1",
      "if": "steps.triage.output.actionable == true && inputs.run == true",
      "with": {
        "role": "ticket_planner",
        "vars": {"MODE": "initial", "FEATURES_PATH": {"$ref": "inputs.issue_path"}},
        "context": {"TRIAGE": {"$ref": "steps.triage.output"}, "CURRENT_STATE": "", "IMPLEMENTATION": "", "VERIFICATION": ""},
        "outputSchema": "schema:workflow-state@1"
      }
    },
    {
      "id": "develop_tickets",
      "uses": "subflow:develop-ticket@1",
      "if": "steps.triage.output.actionable == true && inputs.run == true",
      "while": {
        "condition": "item.state.status == \"active\"",
        "maxIterations": 10,
        "initial": {"state": {"$ref": "steps.plan_fix.output"}}
      },
      "with": {
        "features_path": {"$ref": "inputs.issue_path"},
        "state": {"$ref": "item.state"}
      }
    },
    {
      "id": "final_lint",
      "uses": "activity:gate.run@1",
      "if": "steps.triage.output.actionable == true && inputs.run == true",
      "with": {"argv": ["uv", "run", "ruff", "check", "."]}
    },
    {
      "id": "final_tests",
      "uses": "activity:gate.run@1",
      "if": "steps.triage.output.actionable == true && inputs.run == true",
      "with": {"argv": ["uv", "run", "pytest", "-q"]}
    }
  ],
  "outputs": {
    "triage": {"$ref": "steps.triage.output"},
    "state": {"$ref": "steps.develop_tickets.output"}
  }
}
```

Mirror the `develop_tickets` `with` contract from `factory-governed@1.json` if it differs.

- [ ] **Step 5: pr-review flow**

`examples/governed-pack/pack/flows/pr-review@1.json`:

```json
{
  "apiVersion": "orq.dev/v2",
  "kind": "Flow",
  "metadata": {"name": "pr-review", "version": "1"},
  "inputs": {
    "pr": {"schema": "schema:text@1", "default": ""},
    "base": {"schema": "schema:text@1", "default": ""},
    "head": {"schema": "schema:text@1", "default": ""},
    "publish": {"schema": "schema:flag@1", "default": false}
  },
  "steps": [
    {
      "id": "review",
      "uses": "activity:agent.invoke@1",
      "with": {
        "role": "pr_reviewer",
        "context": {
          "PR": {"$ref": "inputs.pr"},
          "BASE": {"$ref": "inputs.base"},
          "HEAD": {"$ref": "inputs.head"},
          "PUBLISH": {"$ref": "inputs.publish"}
        },
        "outputSchema": "schema:review-result@1",
        "fallbackOutput": {"approved": false, "summary": "pr reviewer agent failed; fail closed", "findings": ["reviewer agent did not produce a verdict"]}
      }
    }
  ],
  "outputs": {
    "verdict": {"$ref": "steps.review.output"}
  }
}
```

- [ ] **Step 6: Regen, validate, commit**

```bash
python3 examples/governed-pack/regen-digests.py
go run ./cmd/orq-lite flow validate examples/governed-pack/pack/flows/issue-fix@1.json
go run ./cmd/orq-lite flow validate examples/governed-pack/pack/flows/pr-review@1.json
python3 -c "import json; json.load(open('examples/governed-pack/team.json'))"
```
Expected: both `valid …`; JSON parses.

```bash
git add examples/governed-pack
git commit -m "feat(examples): issue-fix@1 + pr-review@1 flows with intake/pr_reviewer roles

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: watch fail-fast validation

**Files:**
- Modify: `internal/commands/watchcmd.go` (v2 branch, after `cfg.FlowRefs`/`cfg.Trigger` are set, before `watch.Run`)
- Test: `internal/commands/watchcmd_test.go` (append; check existing test harness first and follow its patterns)

**Interfaces:**
- Consumes: `compileWorkflowTarget(projectDir, target)` (`internal/commands/workflowcmd.go:44`).
- Produces: `orq-lite watch --engine=v2` errors at STARTUP (not on first event) when a configured flow ref does not resolve/compile, with an error naming the ref.

- [ ] **Step 1: Write the failing test**

Read `internal/commands/watchcmd_test.go` first to reuse its `WatchOptions` construction. Append (adjust option/field names to the real ones — the assertion contract is what matters):

```go
func TestWatch_V2FailsFastOnMissingFlow(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := Watch(ctx, WatchOptions{
		ProjectDir: dir,
		Engine:     "v2",
		Issues:     true,
		Out:        io.Discard,
	})
	if err == nil {
		t.Fatal("expected startup error for missing v2 flow")
	}
	if !strings.Contains(err.Error(), "development/issue-fix@1") {
		t.Fatalf("error should name the flow ref, got: %v", err)
	}
}
```

If `WatchOptions` has no `Issues` field (enable-set derived elsewhere), mirror how existing tests enable issue watching. The essential assertions: startup error (no polling begins), message contains the flow ref.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/commands/ -run TestWatch_V2FailsFast -v`
Expected: FAIL (today Watch starts successfully and only errors per-event).

- [ ] **Step 3: Implement**

In `internal/commands/watchcmd.go`, inside the `opts.Engine == "v2"` branch, after `cfg.FlowRefs` is populated and the enabled item-types are known, add:

```go
	// Fail fast: a watch daemon with an unresolvable flow would only surface
	// the error on the first event, hours later. Compile the configured flows
	// now, exactly as flow run will.
	for itemType, ref := range cfg.FlowRefs {
		if !enabled[itemType] {
			continue
		}
		if _, err := compileWorkflowTarget(opts.ProjectDir, ref); err != nil {
			return fmt.Errorf("watch: flow %s does not compile: %w", ref, err)
		}
	}
```

Adapt `enabled[itemType]` to however watchcmd.go actually tracks which item types are active (read the file; if there is no such map, validate both refs unconditionally — issues and PRs are both watched by default).

- [ ] **Step 4: Run tests**

Run: `go test ./internal/commands/ -run TestWatch -v`
Expected: new test PASSES, all existing watch tests PASS.

- [ ] **Step 5: Full suite + commit**

Run: `go build ./... && go vet ./... && go test ./...`

```bash
git add internal/commands/watchcmd.go internal/commands/watchcmd_test.go
git commit -m "fix(watch): fail fast when a v2 flow ref does not compile at startup

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 8: Integration — regression test, evidence update, docs

**Files:**
- Create: `internal/commands/governedpack_test.go`
- Modify: `benchmark/cutover-evidence.json` (developmentPack.manifestDigest)
- Modify: `examples/governed-pack/README.md` (flows table)
- Modify: `README.md` (one line), `CHANGELOG.md` (Unreleased additions)

**Interfaces:**
- Consumes: everything above.
- Produces: CI-level guarantee that the example pack's 6 required flows compile; cutover `development-pack` gate PASS.

- [ ] **Step 1: Write the regression test**

Create `internal/commands/governedpack_test.go`:

```go
package commands

import (
	"path/filepath"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/flow"
)

// The governed-pack example must always satisfy the cutover gate's required
// development flows: they must exist, verify against pack.json, and compile.
func TestGovernedPackRequiredFlowsCompile(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "examples", "governed-pack", "pack"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flow.LoadPack(root); err != nil {
		t.Fatalf("pack.json digests are stale — run examples/governed-pack/regen-digests.py: %v", err)
	}
	catalog := flow.NewDirectoryCatalog(root, builtinSpecs())
	for _, name := range []string{"factory-fast", "factory-governed", "issue-fix", "plan-tickets", "pr-review", "task-list"} {
		ref := flow.ResourceRef{Kind: "flow", Name: name, Version: "1"}
		doc, _, resolveErr := catalog.ResolveDocument(ref)
		if resolveErr != nil {
			t.Errorf("%s: %v", name, resolveErr)
			continue
		}
		if _, diagnostics := flow.Compile(doc, catalog); diagnostics.HasErrors() {
			t.Errorf("%s does not compile: %+v", name, diagnostics)
		}
	}
}
```

Run: `go test ./internal/commands/ -run TestGovernedPackRequiredFlows -v`
Expected: PASS.

- [ ] **Step 2: Update the evidence manifest digest**

The pack changed, so its manifest digest changed. Re-resolve exactly as before:

```bash
go run ./cmd/orq-lite cutover check --evidence benchmark/cutover-evidence.json --commit "$(git rev-parse HEAD)"
```
The `development-pack` gate fails with `identity mismatch: got development@1 digest=<new-sha>`. Copy the new digest into `benchmark/cutover-evidence.json` → `developmentPack.manifestDigest`, re-run the check. Expected now: `[PASS] development-pack: development@1 digest=… resolves offline with 6 required flows` — and the overall gate still CLOSED (parity/benchmarks/chaos/canary/rollback remain, correctly). Also update `runtimeCommit` to the current HEAD? NO — leave `runtimeCommit` as-is; it binds the older evidence entries. The development-pack gate does not depend on runtimeCommit.

- [ ] **Step 3: Docs**

1. `examples/governed-pack/README.md`: add a "Flows" section (place after the existing flow description, before "Run it") listing the 7 flows in a table: `factory-governed@1` (full governed build; `fast=true` switches to the batch path, `create_pr=true` opens a PR), `review-existing@1` (audit an existing tree), `plan-tickets@1` (planning only — `orq-lite plan` alias), `task-list@1` (`orq-lite run` alias), `factory-fast@1` (one-batch fast path), `issue-fix@1` (triage → plan → develop — `orq-lite intake` alias and `watch --issues` default), `pr-review@1` (agent-driven PR review — `orq-lite review` alias and `watch --prs` default). One line each.
2. `README.md`: in the v2 runtime section right after the example block, add one sentence: "The governed pack ships all six flows the CLI aliases and `watch` defaults reference (`plan-tickets`, `task-list`, `factory-fast`, `factory-governed`, `issue-fix`, `pr-review`)."
3. `CHANGELOG.md` → `## Unreleased` → `### Added`: add bullets for the five new flows + `fast-batch@1` subflow + three new roles, and for the watch fail-fast under `### Changed`.

- [ ] **Step 4: E2E sanity + full suite + commit**

```bash
go build -o /tmp/orq-lite-dev ./cmd/orq-lite
cd "$(mktemp -d)" && git init -q
/tmp/orq-lite-dev pack install ~/Projects/personal/orquesta-lite/examples/governed-pack/pack
for f in plan-tickets task-list factory-fast factory-governed issue-fix pr-review; do /tmp/orq-lite-dev flow validate "development/$f@1" || echo "FAIL $f"; done
cd -
go build ./... && go vet ./... && go test ./...
```
Expected: all six `valid …`, suite green.

```bash
git add internal/commands/governedpack_test.go benchmark/cutover-evidence.json examples/governed-pack/README.md README.md CHANGELOG.md
git commit -m "feat(examples): governed pack ships all 6 required flows; development-pack gate green

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Review

**Status: COMPLETE — 12 commits (6667e2b..d5de330), suite green, final whole-phase review READY TO MERGE after one fix wave.**

- Tasks 1–8 executed via subagent-driven development. Highlights beyond the plan:
  - **Bonus fix (Task 1):** `.gitignore` silently excluded `examples/**/schemas/**` — the pack's 6 pre-existing schemas were never tracked; a fresh clone shipped a broken pack. Fixed + tracked.
  - **Rule discovered (Task 2 review):** `prompts.Interpolate` leaves missing `{{KEY}}` verbatim → `{{APPEND}}`/`{{TRIAGE}}` placeholders added and supplied at every `ticket_planner` call site; placeholder audits enforced on all subsequent flows.
  - **Reconciliations:** the plan's draft JSONs were corrected against the real prompts (qa has no `{{IMPLEMENTATION}}`; integrator uses `QA_REVIEW`/`CRITIC_REVIEW`/`FEEDBACK`/`ITERATION`; planner state vars live in `vars` as `"{}"` strings).
  - **Final-review CRITICAL (fixed in ce97889):** `factory-governed@1` output `governance` referenced `steps.integrated_review.output.governance` — with `fast=true` the skipped step's nil output made ResolveValue fail the run. Outputs now use whole `.output` refs (+ new `batch` output); pack-wide audit found no other sub-property refs through conditional steps.
  - Python/uv toolchain assumption disclosed in the pack README; regen-digests.py warns it legitimizes any file under pack/; CHANGELOG factual corrections (new roles = batch_coder/intake/pr_reviewer; watch entry describes startup compile check).

**Verification:** all 7 flows `flow validate` green; `TestGovernedPackRequiredFlowsCompile` locks the 6 required flows in CI; e2e `pack install` + validate loop green in a fresh project; `cutover check`: `[PASS] development-pack … resolves offline with 6 required flows` (overall gate CLOSED — parity/benchmarks/chaos/canary/rollback remain, as intended); `go build/vet/test` green.

**Deferred (triaged ship-as-is):** task-list `outputs.developed` null in fast mode; watch fail-fast lacks PR-only/legacy test cases; runtime smoke test for `fast=true` output resolution recommended as follow-up; scheduler-level nil-safe sub-property resolution noted as a possible DSL hardening.
