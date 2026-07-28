# Governed Refactor Pass Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a post-governance refactor pass to the official governed pack (`examples/governed-pack/`) — a new `refactorer` role that finds structural cleanup opportunities across the whole integrated diff, an `integrator`-applied repair loop with its own retry budget, and an explicit checkpoint commit marking the governance-approved state before the refactor pass touches anything.

**Architecture:** `factory-governed@1` gains two new steps after `integrated_review` and before `publish_pr`: `checkpoint_commit` (a plain `git commit` of the approved state) and `refactor_review` (a new subflow: `refactorer` finds → `refactor_repair` loop of a new `refactor-cycle@1` subflow applies via the existing `integrator` role and re-audits fresh → `refactor_gate` fails the whole run closed if it never converges). `refactor-cycle@1` is a structural clone of the existing `governance-cycle@1` subflow. No existing subflow's control flow changes; `integrator.md` gains one new optional context line (`REFACTOR_REVIEW`) that existing call sites must supply as `"n/a"` so the raw placeholder never leaks into a prompt un-substituted.

**Tech Stack:** `orq.dev/v2` Flow/Subflow JSON (this repo's own DSL, not a general-purpose language), Markdown agent prompts with `{{VAR}}` string interpolation, Python 3 (`regen-digests.py`) for the pack manifest.

## Global Constraints

- **No flow/pack version bump.** Modify `flows/factory-governed@1.json`, `subflows/integrated-review@1.json`, and `subflows/governance-cycle@1.json` **in place** — do not rename to `@2`. This matches how this same pack's adversary-wiring fix was previously shipped (in-place edit, no version bump) and avoids a doc-reference sweep across `README.md`, `guide.md`, `CHANGELOG.md`, and every `.orquestalite/packs/development/1/...` path in `team.json` that a real version bump would force. `pack.json`'s top-level `"version"` stays `"1"` too.
- **Prompt interpolation has no conditionals.** `internal/prompts/prompts.go`'s `Interpolate` is a dumb `strings.ReplaceAll` over whatever vars map is supplied — a `{{VAR}}` placeholder with no matching key in the context is left **literally in the prompt text**, unresolved. Any task that adds a new `{{VAR}}` to a **shared** prompt (only `integrator.md` in this plan) must audit every existing call site of that prompt and supply the new key everywhere, even as a literal `"n/a"`-style string when it doesn't apply. Extra, unused context keys are harmless (confirmed: `governance-cycle@1.json`'s `repair` step already passes an unused `GOV_REVIEW` key that `integrator.md` never references).
- **Context values accept plain string literals, not just `{"$ref": ...}`.** Confirmed against `factory-governed@1.json`'s existing `plan_tickets` step (`"CURRENT_STATE": "{}"`, `"APPEND": "false"` are bare literals in a `vars` block validated successfully by `orq-lite flow validate`). Use this for the `"REFACTOR_REVIEW": "n/a — ..."` literals below.
- **Validate everything with `orq-lite flow validate <path>`** — it works directly on a file path without installing the pack (confirmed: `orq-lite flow validate examples/governed-pack/pack/subflows/governance-cycle@1.json` → `valid governance-cycle@1 <hash>` today, before any change in this plan). Build the binary once at the start:

  ```bash
  go build -o /tmp/orq-lite-plan ./cmd/orq-lite
  ```

  Use `/tmp/orq-lite-plan` for every validation step in this plan.
- **Reuse `schema:review-result@1` (`{approved, summary, findings}`) and `schema:iteration-result@1` (`{continue, summary, remaining}`) as-is.** No new schema files.
- Every new/modified JSON file must remain valid JSON and pass `orq-lite flow validate` before its task is considered done.

---

### Task 1: Add the `refactorer` prompt and wire the role into `team.json`

**Files:**
- Create: `examples/governed-pack/pack/prompts/refactorer.md`
- Modify: `examples/governed-pack/team.json`

**Interfaces:**
- Produces: role name `refactorer`, output contract `schema:review-result@1` (`{"approved": bool, "summary": string, "findings": [string]}`) written to `.orquestalite/results/refactorer.json`. Later tasks (2, 3) invoke this role by name and depend on this exact output shape.

- [ ] **Step 1: Write `prompts/refactorer.md`**

```markdown
Read {{FEATURES_PATH}}, the complete repository, all tests, and the hard
conventions. Do not modify files — you only report findings.

You are reviewing code that already passed governance: implemented, tested,
and approved. Your only job is structural cleanup — never behavior changes,
never new functionality, never a public API change unless it is strictly
cosmetic (e.g. a misleading name).

Look across the whole integrated diff for this feature, not one ticket in
isolation. Flag opportunities such as: duplicated logic that should be
extracted, misleading or inconsistent names, incidental complexity that
obscures the actual contract, dead code, or structure that will make the
next feature harder to add.

A finding only counts if applying it would still pass `uv run ruff check .`
and `uv run pytest -q` with the exact same test results — no test may need
to change for a refactor finding to be valid. If verifying a suggestion
would require rewriting a test, it is not a refactor finding; drop it.

Before finishing, write JSON only to `.orquestalite/results/refactorer.json`:

```json
{"approved":true,"summary":"what you found, if anything","findings":["concrete, actionable refactor opportunity"]}
```

Set approved false only when you found at least one real finding worth
applying. Set it true when the code is already clean, or when you have
nothing concrete enough to act on — an empty codebase-tidy pass is a
legitimate outcome, not a failure.
```

- [ ] **Step 2: Add the role to `examples/governed-pack/team.json`**

Add this entry to the `"roles"` object, next to the existing `"gov_reviewer"` entry (matches its `timeout_seconds` — same judgment-tier budget):

```json
    "refactorer": {"agents": ["haiku"], "prompt": ".orquestalite/packs/development/1/prompts/refactorer.md", "result_path": ".orquestalite/results/refactorer.json", "timeout_seconds": 1500},
```

- [ ] **Step 3: Verify JSON validity**

Run: `python3 -c "import json; json.load(open('examples/governed-pack/team.json'))" && echo OK`
Expected: `OK`

- [ ] **Step 4: Commit**

```bash
git add examples/governed-pack/pack/prompts/refactorer.md examples/governed-pack/team.json
git commit -m "feat(governed-pack): add refactorer role"
```

---

### Task 2: Add the `refactor-cycle@1` subflow (clone of `governance-cycle@1`)

**Files:**
- Create: `examples/governed-pack/pack/subflows/refactor-cycle@1.json`

**Interfaces:**
- Consumes: role `refactorer` (Task 1), role `integrator` (existing, extended in Task 5), `schema:review-result@1`, `schema:iteration-result@1` (existing schemas).
- Produces: subflow `refactor-cycle@1` with inputs `{features_path, verdict, qa_review, critic_review, adversary_review}` (all but `features_path` typed `schema:review-result@1`) and outputs `{approved: bool, verdict: object}` — Task 3's `refactor_review` subflow calls this in a `while` loop keyed on `approved`.

- [ ] **Step 1: Write `subflows/refactor-cycle@1.json`**

```json
{
 "apiVersion": "orq.dev/v2",
 "kind": "Subflow",
 "metadata": {
  "name": "refactor-cycle",
  "version": "1"
 },
 "inputs": {
  "features_path": {"schema": "schema:path@1"},
  "verdict": {"schema": "schema:review-result@1"},
  "qa_review": {"schema": "schema:review-result@1"},
  "critic_review": {"schema": "schema:review-result@1"},
  "adversary_review": {"schema": "schema:review-result@1"}
 },
 "steps": [
  {
   "id": "repair",
   "uses": "activity:agent.invoke@1",
   "with": {
    "role": "integrator",
    "outputSchema": "schema:iteration-result@1",
    "context": {
     "FEATURES_PATH": {"$ref": "inputs.features_path"},
     "FEEDBACK": {"$ref": "inputs.verdict"},
     "REFACTOR_REVIEW": {"$ref": "inputs.verdict"},
     "QA_REVIEW": {"$ref": "inputs.qa_review"},
     "CRITIC_REVIEW": {"$ref": "inputs.critic_review"},
     "ADVERSARY_REVIEW": {"$ref": "inputs.adversary_review"}
    }
   }
  },
  {
   "id": "lint",
   "uses": "activity:gate.run@1",
   "with": {"argv": ["uv", "run", "ruff", "check", "."]}
  },
  {
   "id": "tests",
   "uses": "activity:gate.run@1",
   "with": {"argv": ["uv", "run", "pytest", "-q"]}
  },
  {
   "id": "refactorer",
   "uses": "activity:agent.invoke@1",
   "with": {
    "role": "refactorer",
    "outputSchema": "schema:review-result@1",
    "vars": {"FEATURES_PATH": {"$ref": "inputs.features_path"}}
   }
  }
 ],
 "outputs": {
  "approved": {"$ref": "steps.refactorer.output.approved"},
  "verdict": {"$ref": "steps.refactorer.output"}
 }
}
```

- [ ] **Step 2: Validate**

Run: `/tmp/orq-lite-plan flow validate examples/governed-pack/pack/subflows/refactor-cycle@1.json`
Expected: `valid refactor-cycle@1 <hash>` (some hex digest — any value is fine, absence of an `error:` line is what matters)

- [ ] **Step 3: Commit**

```bash
git add examples/governed-pack/pack/subflows/refactor-cycle@1.json
git commit -m "feat(governed-pack): add refactor-cycle@1 subflow"
```

---

### Task 3: Add the `refactor-review@1` subflow

**Files:**
- Create: `examples/governed-pack/pack/subflows/refactor-review@1.json`

**Interfaces:**
- Consumes: `subflow:refactor-cycle@1` (Task 2), role `refactorer` (Task 1).
- Produces: subflow `refactor-review@1` with inputs `{features_path, qa_review, critic_review, adversary_review}` and outputs `{refactorer: object, refactor_repair: object}` — Task 4's top-level flow calls this and threads `steps.integrated_review.output.{qa,critic,adversary}` into it.

- [ ] **Step 1: Write `subflows/refactor-review@1.json`**

```json
{
 "apiVersion": "orq.dev/v2",
 "kind": "Subflow",
 "metadata": {
  "name": "refactor-review",
  "version": "1"
 },
 "inputs": {
  "features_path": {"schema": "schema:path@1"},
  "qa_review": {"schema": "schema:review-result@1"},
  "critic_review": {"schema": "schema:review-result@1"},
  "adversary_review": {"schema": "schema:review-result@1"}
 },
 "steps": [
  {
   "id": "refactorer",
   "uses": "activity:agent.invoke@1",
   "with": {
    "role": "refactorer",
    "outputSchema": "schema:review-result@1",
    "vars": {"FEATURES_PATH": {"$ref": "inputs.features_path"}},
    "fallbackOutput": {
     "approved": true,
     "summary": "refactorer unavailable; no refactor findings to apply",
     "findings": []
    }
   }
  },
  {
   "id": "refactor_repair",
   "uses": "subflow:refactor-cycle@1",
   "while": {
    "condition": "item.approved != true",
    "maxIterations": 2,
    "initial": {
     "approved": {"$ref": "steps.refactorer.output.approved"},
     "verdict": {"$ref": "steps.refactorer.output"}
    }
   },
   "with": {
    "features_path": {"$ref": "inputs.features_path"},
    "verdict": {"$ref": "item.verdict"},
    "qa_review": {"$ref": "inputs.qa_review"},
    "critic_review": {"$ref": "inputs.critic_review"},
    "adversary_review": {"$ref": "inputs.adversary_review"}
   }
  },
  {
   "id": "refactor_gate",
   "uses": "activity:gate.run@1",
   "with": {
    "argv": ["uv", "run", "python", "-c",
     "import json,sys; data=json.load(open(sys.argv[1])); raise SystemExit(0 if data.get('approved') is True else 1)",
     ".orquestalite/results/refactorer.json"]
   }
  }
 ],
 "outputs": {
  "refactorer": {"$ref": "steps.refactorer.output"},
  "refactor_repair": {"$ref": "steps.refactor_repair.output"}
 }
}
```

Note: `refactor_gate` reads `.orquestalite/results/refactorer.json` **after** `refactor_repair` has run — that file gets overwritten by the fresh re-audit invocation inside `refactor-cycle@1`'s own `refactorer` step on every loop iteration, so this always reflects the latest verdict. This is the same pattern `governance_gate` already relies on in `integrated-review@1.json` (reads `gov_reviewer.json` after `governance_repair`, whose inner `governance` step keeps overwriting it fresh).

- [ ] **Step 2: Validate**

Run: `/tmp/orq-lite-plan flow validate examples/governed-pack/pack/subflows/refactor-review@1.json`
Expected: `valid refactor-review@1 <hash>`

- [ ] **Step 3: Commit**

```bash
git add examples/governed-pack/pack/subflows/refactor-review@1.json
git commit -m "feat(governed-pack): add refactor-review@1 subflow"
```

---

### Task 4: Wire `checkpoint_commit` and `refactor_review` into `factory-governed@1`

**Files:**
- Modify: `examples/governed-pack/pack/flows/factory-governed@1.json`

**Interfaces:**
- Consumes: `subflow:refactor-review@1` (Task 3), `steps.integrated_review.output.{qa,critic,adversary}` (already exposed today by `integrated-review@1.json`'s existing `outputs` block — confirmed, no change needed there).
- Produces: flow output key `refactor` (`$ref: steps.refactor_review.output`) — nothing downstream in this plan consumes it yet, but it must exist for future tooling (e.g. `orq-lite flow status`) to surface refactor results.

- [ ] **Step 1: Insert two new steps** between the existing `integrated_review` step and the existing `publish_pr` step. Both are gated by the same `"if": "inputs.fast != true"` as `integrated_review` itself — the `fast_batch` path has no governance verdict to checkpoint or refactor against, so both new steps must be skipped when `fast=true`, exactly like `integrated_review` already is.

The full modified file (replace `examples/governed-pack/pack/flows/factory-governed@1.json` entirely with this):

```json
{
 "apiVersion": "orq.dev/v2",
 "kind": "Flow",
 "metadata": {
  "name": "factory-governed",
  "version": "1"
 },
 "inputs": {
  "features_path": {
   "schema": "schema:path@1"
  },
  "fast": {"schema": "schema:flag@1", "default": false},
  "create_pr": {"schema": "schema:flag@1", "default": false}
 },
 "steps": [
  {
   "id": "plan_tickets",
   "uses": "activity:agent.invoke@1",
   "with": {
    "role": "ticket_planner",
    "outputSchema": "schema:workflow-state@1",
    "vars": {
     "MODE": "initial",
     "FEATURES_PATH": {
      "$ref": "inputs.features_path"
     },
     "CURRENT_STATE": "{}",
     "IMPLEMENTATION": "{}",
     "VERIFICATION": "{}",
     "APPEND": "false",
     "TRIAGE": ""
    }
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
   "id": "develop_tickets",
   "uses": "subflow:develop-ticket@1",
   "if": "inputs.fast != true",
   "while": {
    "condition": "item.state.status == \"active\"",
    "maxIterations": 20,
    "initial": {
     "state": {
      "$ref": "steps.plan_tickets.output"
     }
    }
   },
   "with": {
    "features_path": {
     "$ref": "inputs.features_path"
    },
    "state": {
     "$ref": "item.state"
    }
   }
  },
  {
   "id": "ticket_plan_complete",
   "uses": "activity:gate.run@1",
   "if": "inputs.fast != true",
   "with": {
    "argv": [
     "uv",
     "run",
     "python",
     "-c",
     "import json,sys; data=json.load(open(sys.argv[1])); raise SystemExit(0 if data.get('status') == 'complete' and not data.get('pending') and data.get('next_ticket') is None else 1)",
     ".orquestalite/results/ticket_planner.json"
    ]
   }
  },
  {
   "id": "integrated_review",
   "uses": "subflow:integrated-review@1",
   "if": "inputs.fast != true",
   "with": {
    "features_path": {
     "$ref": "inputs.features_path"
    }
   }
  },
  {
   "id": "checkpoint_commit",
   "uses": "activity:command.run@1",
   "if": "inputs.fast != true",
   "with": {
    "argv": ["sh", "-c", "git add -A && (git diff --cached --quiet || git commit -m 'checkpoint: governance approved, pre-refactor')"]
   }
  },
  {
   "id": "refactor_review",
   "uses": "subflow:refactor-review@1",
   "if": "inputs.fast != true",
   "with": {
    "features_path": {
     "$ref": "inputs.features_path"
    },
    "qa_review": {
     "$ref": "steps.integrated_review.output.qa"
    },
    "critic_review": {
     "$ref": "steps.integrated_review.output.critic"
    },
    "adversary_review": {
     "$ref": "steps.integrated_review.output.adversary"
    }
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
  "ticket_iterations": {"$ref": "steps.develop_tickets.output"},
  "governance": {"$ref": "steps.integrated_review.output"},
  "batch": {"$ref": "steps.fast_batch.output"},
  "refactor": {"$ref": "steps.refactor_review.output"}
 }
}
```

- [ ] **Step 2: Validate**

Run: `/tmp/orq-lite-plan flow validate examples/governed-pack/pack/flows/factory-governed@1.json`
Expected: `valid factory-governed@1 <hash>` (the hash will differ from before the edit — that's expected, it's a content digest)

- [ ] **Step 3: Commit**

```bash
git add examples/governed-pack/pack/flows/factory-governed@1.json
git commit -m "feat(governed-pack): wire checkpoint_commit + refactor_review into factory-governed"
```

---

### Task 5: Extend `integrator.md` for refactor-mode repairs, and keep the two existing callers placeholder-safe

**Files:**
- Modify: `examples/governed-pack/pack/prompts/integrator.md`
- Modify: `examples/governed-pack/pack/subflows/integrated-review@1.json`
- Modify: `examples/governed-pack/pack/subflows/governance-cycle@1.json`

**Interfaces:**
- Consumes: none new.
- Produces: `integrator.md` now has a `{{REFACTOR_REVIEW}}` placeholder. Every existing invocation of role `integrator` (there are three total: `integration_repair` in `integrated-review@1.json`, `repair` in `governance-cycle@1.json`, and the new `repair` in `refactor-cycle@1.json` from Task 2) must supply that key or the literal text `{{REFACTOR_REVIEW}}` leaks into the prompt sent to the agent.

- [ ] **Step 1: Add `"REFACTOR_REVIEW": "n/a — this is a bug-fix repair pass, not a refactor pass"` to `integrated-review@1.json`'s `integration_repair` step context**

In `examples/governed-pack/pack/subflows/integrated-review@1.json`, find the `integration_repair` step's `context` object:

```json
    "context": {
     "FEATURES_PATH": {
      "$ref": "inputs.features_path"
     },
     "FEEDBACK": {
      "$ref": "item"
     },
     "ITERATION": {
      "$ref": "index"
     },
     "QA_REVIEW": {
      "$ref": "steps.qa.output"
     },
     "CRITIC_REVIEW": {
      "$ref": "steps.critic.output"
     },
     "ADVERSARY_REVIEW": {
      "$ref": "steps.adversary.output"
     }
    }
```

Add one line so it reads:

```json
    "context": {
     "FEATURES_PATH": {
      "$ref": "inputs.features_path"
     },
     "FEEDBACK": {
      "$ref": "item"
     },
     "ITERATION": {
      "$ref": "index"
     },
     "QA_REVIEW": {
      "$ref": "steps.qa.output"
     },
     "CRITIC_REVIEW": {
      "$ref": "steps.critic.output"
     },
     "ADVERSARY_REVIEW": {
      "$ref": "steps.adversary.output"
     },
     "REFACTOR_REVIEW": "n/a — this is a bug-fix repair pass, not a refactor pass"
    }
```

- [ ] **Step 2: Add the same line to `governance-cycle@1.json`'s `repair` step context**

In `examples/governed-pack/pack/subflows/governance-cycle@1.json`, the `repair` step's `context` currently ends with `"ADVERSARY_REVIEW": {"$ref": "inputs.adversary_review"}`. Add after it:

```json
     "REFACTOR_REVIEW": "n/a — this is a bug-fix repair pass, not a refactor pass"
```

so the full `repair` step context block reads:

```json
    "context": {
     "FEATURES_PATH": {
      "$ref": "inputs.features_path"
     },
     "FEEDBACK": {
      "$ref": "inputs.verdict"
     },
     "GOV_REVIEW": {
      "$ref": "inputs.verdict"
     },
     "QA_REVIEW": {
      "$ref": "inputs.qa_review"
     },
     "CRITIC_REVIEW": {
      "$ref": "inputs.critic_review"
     },
     "ADVERSARY_REVIEW": {
      "$ref": "inputs.adversary_review"
     },
     "REFACTOR_REVIEW": "n/a — this is a bug-fix repair pass, not a refactor pass"
    }
```

- [ ] **Step 3: Rewrite `prompts/integrator.md`**

Replace the full file with:

```markdown
You repair only the integrated findings produced after all planned tickets.
Work directly in the repository. Do not commit.

Canonical contract: {{FEATURES_PATH}}
QA review: {{QA_REVIEW}}
Critic review: {{CRITIC_REVIEW}}
Adversarial falsification review: {{ADVERSARY_REVIEW}}
Refactor review: {{REFACTOR_REVIEW}}
Feedback driving this repair pass (previous repair state, or a governance
rejection verdict if this pass was triggered by a governance repair cycle):
{{FEEDBACK}}

Reproduce and address every actionable finding from ALL of QA, critic, and
adversary — not only whichever is easiest or largest. An adversarial finding
is a reproduced defect with a concrete repro, not a suspicion; it does not
outrank the others by default, but it must never be silently dropped in
favor of a different finding just because there was only time for one. If
you cannot fix everything in this pass, say so explicitly in `remaining` and
name which specific findings are still open — do not report `continue:false`
while any QA, critic, or adversary finding remains unaddressed.

If the refactor review above says `n/a`, ignore it — this is a bug-fix pass.
If instead QA/critic/adversary above say `n/a`, this is a **refactor repair
pass**, not a bug-fix pass: address only the findings listed in the refactor
review, never change observable behavior, never add functionality, never
touch the public API beyond a strictly cosmetic rename, and never modify a
test's expectations to make a finding "pass" — if a finding cannot be applied
without changing what a test asserts, leave it unaddressed and name it in
`remaining` instead. Set `continue:false` for a refactor pass once every
refactor finding is applied or explicitly deferred and both gates still pass
with identical results.

Strengthen tests so each fix is observable — if a finding came with its own
reproduction (a script, a test, explicit steps), turn that reproduction into
a permanent regression test under `tests/` if one does not already exist, so
the gates themselves catch a future regression. Run `uv run ruff check .` and
`uv run pytest -q`.

Before finishing, write JSON only to `.orquestalite/results/integrator.json`:

```json
{"continue":false,"summary":"what changed and exact gate status","remaining":[]}
```

Set continue false only when all QA, critic, AND adversary findings are
resolved and both gates pass (bug-fix pass), or when every refactor finding
is applied/deferred and both gates pass (refactor pass). Otherwise keep it
true and list concrete remaining findings, including which ones are
adversary-sourced or refactor-sourced.
```

- [ ] **Step 4: Validate both modified subflows**

Run:
```bash
/tmp/orq-lite-plan flow validate examples/governed-pack/pack/subflows/integrated-review@1.json
/tmp/orq-lite-plan flow validate examples/governed-pack/pack/subflows/governance-cycle@1.json
```
Expected: both print `valid <name>@1 <hash>` with no `error:` line.

- [ ] **Step 5: Grep-verify no dangling `{{REFACTOR_REVIEW}}` context gaps remain**

Run: `grep -L "REFACTOR_REVIEW" examples/governed-pack/pack/subflows/integrated-review@1.json examples/governed-pack/pack/subflows/governance-cycle@1.json examples/governed-pack/pack/subflows/refactor-cycle@1.json`
Expected: **no output** (empty — `-L` prints files that do NOT contain the string; empty output means all three now contain it, i.e. every existing/new caller of `integrator` supplies the key)

- [ ] **Step 6: Commit**

```bash
git add examples/governed-pack/pack/prompts/integrator.md examples/governed-pack/pack/subflows/integrated-review@1.json examples/governed-pack/pack/subflows/governance-cycle@1.json
git commit -m "feat(governed-pack): teach integrator to apply refactor findings"
```

---

### Task 6: Regenerate `pack.json` digests

**Files:**
- Modify: `examples/governed-pack/pack/pack.json` (regenerated, not hand-edited)

**Interfaces:**
- Consumes: every file under `examples/governed-pack/pack/` (Tasks 1–5's new and modified files).
- Produces: an updated `"files"` map in `pack.json` with correct SHA-256 digests for the 3 new files and the 4 modified files.

- [ ] **Step 1: Run the existing digest script**

Run: `python3 examples/governed-pack/regen-digests.py`
Expected output: `pack.json: N files digested` where N is one more than before (there were 25 entries before this plan; Tasks 2 and 3 add `subflows/refactor-cycle@1.json` and `subflows/refactor-review@1.json`, Task 1 adds `prompts/refactorer.md` — 3 new files, so N should be 28).

- [ ] **Step 2: Review the diff before committing — this script legitimizes every file it finds under `pack/`, including accidental ones**

Run: `git diff examples/governed-pack/pack/pack.json`
Expected: entries changed only for `flows/factory-governed@1.json`, `subflows/integrated-review@1.json`, `subflows/governance-cycle@1.json` (modified — new hashes), plus 3 new entries added (`prompts/refactorer.md`, `subflows/refactor-cycle@1.json`, `subflows/refactor-review@1.json`). No unrelated entries should appear or disappear — if any do, something was accidentally left in or removed from `pack/`, stop and investigate before proceeding.

- [ ] **Step 3: Commit**

```bash
git add examples/governed-pack/pack/pack.json
git commit -m "chore(governed-pack): regenerate pack.json digests for refactor pass"
```

---

### Task 7: Update `examples/governed-pack/README.md`

**Files:**
- Modify: `examples/governed-pack/README.md`

**Interfaces:** None (documentation only).

- [ ] **Step 1: Update the architecture diagram**

Find this block:

```
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
```

Replace with:

```
```
plan_tickets (budget-sized)
└─ develop_tickets  [per ticket: coder → ticket_qa → replan]
integrated_review:
   lint → tests → qa → adversary → critic
   → integration_repair   [loop: reconcile findings]
   → gates → governance
   → governance_repair     [loop ×2: repair → gates → FRESH re-audit]
   → governance_gate       (fail-closed)
checkpoint_commit   [commits the governance-approved state before refactor touches anything]
refactor_review:
   refactorer
   → refactor_repair       [loop ×2: repair → gates → FRESH re-audit, own budget]
   → refactor_gate         (fail-closed — same weight as governance_gate)
```
```

- [ ] **Step 2: Add a fourth numbered lesson after the existing three**

Find the end of the numbered list (after "...blocking findings." for item 3, before "## Flows"). Add:

```markdown
4. **A refactor pass with its own retry budget.** After governance approves,
   `checkpoint_commit` records that exact state in git, then `refactor_review`
   looks for structural cleanup across the whole integrated diff —
   duplication, naming, incidental complexity — and applies it through the
   same `integrator` role. Its repair loop never shares iterations with
   `integration_repair`/`governance_repair`, so a refactor finding can never
   crowd out a real bug finding's retry budget. A refactor pass that can't
   converge fails the run closed too — but the checkpoint commit means the
   governed, approved code is always safe in git history before that risk
   exists.
```

- [ ] **Step 3: Update the "Models" production note**

Find:

```markdown
- **Models.** Swap the haiku team for real reviewers: a strong coder
  (e.g. Sonnet) and **Opus on the test/gate roles** (`ticket_qa`, `qa`,
  `adversary`, `critic`, `gov_reviewer`). The review roles are where the bugs
  are caught, and in the benchmark they were ~78% of a governed run's cost —
  that spend is the point, not the overhead.
```

Replace with:

```markdown
- **Models.** Swap the haiku team for real reviewers: a strong coder
  (e.g. Sonnet) and **Opus on the test/gate roles** (`ticket_qa`, `qa`,
  `adversary`, `critic`, `gov_reviewer`, `refactorer`). The review roles are
  where the bugs are caught, and in the benchmark they were ~78% of a
  governed run's cost — that spend is the point, not the overhead.
```

- [ ] **Step 4: Commit**

```bash
git add examples/governed-pack/README.md
git commit -m "docs(governed-pack): document the refactor pass"
```

---

### Task 8: End-to-end structural check on a scratch project

**Files:** none in the repo — this task operates entirely in a scratch directory outside version control.

**Interfaces:** none new; this task only verifies Tasks 1–7 compose correctly through `orq-lite doctor` and an installed-pack `flow validate` (static checks — no real agent invocation, no API cost).

- [ ] **Step 1: Scaffold a throwaway project**

```bash
rm -rf /tmp/refactor-pass-check && mkdir -p /tmp/refactor-pass-check && cd /tmp/refactor-pass-check
git init -q
echo "# scratch" > README.md
git add -A && git commit -q -m "init"
/tmp/orq-lite-plan init
```
Expected: exits 0, scaffolds `.orquestalite/` and `.gitignore`.

- [ ] **Step 2: Install the modified pack and drop the team over it**

```bash
/tmp/orq-lite-plan pack install /Users/lionelchamorro/Projects/personal/orquesta-lite/examples/governed-pack/pack
cp /Users/lionelchamorro/Projects/personal/orquesta-lite/examples/governed-pack/{team.json,features.md,CONVENTIONS.md} .
```
Expected: `pack install` reports the pack installed at version `1` with no digest mismatch errors (this is the real test that Task 6's digests are correct).

- [ ] **Step 3: Doctor check**

Run: `/tmp/orq-lite-plan doctor`
Expected: no `unknown role` / `missing prompt` errors for `refactorer` (a `git tree has uncommitted changes` warning is fine here since nothing has run yet — ignore it). All `[PASS]` lines for `team.json`, `prompts`, `provider:claude`/`credentials:claude` should still appear same as before this plan (this scratch project doesn't need real credentials for a doctor-only check to validate role/prompt resolution — if credentials are absent, doctor reports that separately from role resolution, which is what this step cares about).

- [ ] **Step 4: Validate the installed flow (not just the raw path)**

Run: `/tmp/orq-lite-plan flow validate development/factory-governed@1`
Expected: `valid factory-governed@1 <hash>` — same hash as Task 4 Step 2 printed, confirming the installed copy matches the source exactly.

- [ ] **Step 5: Clean up**

```bash
rm -rf /tmp/refactor-pass-check
```

(No commit — this task touches no repo files.)

---

## Self-Review

**1. Spec coverage:**
- §"Cuándo" (end, over whole diff) → Task 4 places `refactor_review` after `integrated_review`, not inside `develop-ticket@1`. ✓
- §"Autoridad" (finder + integrator applies) → Task 1 (`refactorer` finder), Task 5 (`integrator` applies). ✓
- §"Aislamiento" (own retry budget, not `integration_repair`/`governance_repair`) → Task 2/3's `refactor_repair` loop is entirely separate; Task 5 confirms `integration_repair`/`governance-cycle@1` are untouched in control flow (only a placeholder-safety literal added). ✓
- §"Checkpoint commit" → Task 4 Step 1 adds `checkpoint_commit` before `refactor_review`. ✓
- §"Peso de la falla" (fail-closed, same weight) → Task 3's `refactor_gate` mirrors `governance_gate`'s exact fail-closed pattern. ✓
- §"Siempre activo" (no flag) → Task 4's steps use `if: inputs.fast != true` only (same gating as `integrated_review` itself), no new flag introduced. ✓
- §Presupuestos (`maxIterations: 2`) → Task 3's `refactor_repair` while loop uses `maxIterations: 2`. ✓
- §Verificación (`flow validate`, digest protocol) → Tasks 2–4 validate via CLI; Task 6 regenerates digests. ✓ A live governed run with real agents (spec's other verification bullet) is intentionally NOT a plan task — it costs real API spend/hours and belongs to the user's own follow-up, not a mechanical implementation step.

**2. Placeholder scan:** No TBD/TODO; every step has literal file content, not descriptions of content.

**3. Type/interface consistency:** `refactorer`'s output is `schema:review-result@1` (`approved`/`summary`/`findings`) everywhere it's referenced (Tasks 1, 2, 3). `integrator`'s output stays `schema:iteration-result@1` (`continue`/`summary`/`remaining`) — unchanged, matches its 3 call sites. Context key names (`REFACTOR_REVIEW`, `FEATURES_PATH`, `QA_REVIEW`, etc.) match between the JSON `context` blocks (Tasks 2, 5) and the `{{VAR}}` placeholders in `integrator.md` (Task 5) exactly.

**Refinement beyond the original design doc** (found while reading the actual reused files closely, not just the spec's abbreviated JSON sketches): the spec's `refactor-cycle@1` sketch only threaded `FEEDBACK`/`REFACTOR_REVIEW` into `integrator`, omitting `QA_REVIEW`/`CRITIC_REVIEW`/`ADVERSARY_REVIEW`. Since `integrator.md` already hardcodes those three placeholders unconditionally, omitting them would have leaked literal `{{QA_REVIEW}}` etc. into every refactor-repair prompt. Fixed by threading the real qa/critic/adversary verdicts through from `integrated_review`'s output (Task 4's `refactor_review` call, Task 3's `refactor-review@1` inputs, Task 2's `refactor-cycle@1` inputs) instead of inventing meaningless placeholder text — more informative for the integrator and zero extra invocation cost, since those values are already computed by the time `refactor_review` runs.

---

Plan complete and saved to `docs/superpowers/plans/2026-07-28-governed-refactor-pass.md`. Two execution options:

**1. Subagent-Driven (recommended)** - I dispatch a fresh subagent per task, review between tasks, fast iteration

**2. Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints

**Which approach?**
