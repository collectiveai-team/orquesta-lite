# Governed Visual/UX Verification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `visual_verifier` role to the official governed pack (`examples/governed-pack/`) — a browser-driven finder, ported from the retired v1 `factory-visual-verify.md` prompt, that runs as a first-class governance finder alongside `qa`/`adversary`/`critic` inside `integrated-review@1`, with a genuine Figma-comparison check folded into the same prompt.

**Architecture:** `subflows/integrated-review@1.json` gains one new step, `visual_verifier`, inserted after `critic` and before `integration_repair` — same shape as the other three finders (`activity:agent.invoke@1`, `schema:review-result@1`, fail-closed `fallbackOutput`). Its output is wired as `VISUAL_REVIEW` into `integration_repair`'s context, `governance`'s context, and `governance-cycle@1`'s `repair` step context — real wiring, not a placeholder, because `prompts/integrator.md` and `prompts/gov-reviewer.md` must genuinely reconcile/confirm visual findings with the same weight as QA/critic/adversary findings. The role's own prompt (`prompts/visual-verifier.md`) self-detects when a project has no UI surface (auto-`approved:true`, no browser opened) and, when a ticket's acceptance criteria reference a `design/<file>` image, folds a Figma-comparison into the same `checks[]`/`findings[]` evidence list — not a separate mechanism.

**Tech Stack:** `orq.dev/v2` Flow/Subflow JSON (pack `development@5` in `examples/governed-pack/pack/`), Markdown agent prompts with `{{VAR}}` string interpolation (`internal/prompts/prompts.go`'s `Interpolate` — a literal `strings.ReplaceAll`, no conditionals), Python 3 (`regen-digests.py`) for the pack manifest.

## Global Constraints

- **Target the CURRENT pack structure, not an older one.** The pack was restructured on 2026-07-30 (commit `0a2c3cc`, "governed pack v4 — policy applies itself, plan-derived loop budget, config-driven gates") — *after* an earlier plan for a different feature (the refactor pass) was written against the pre-restructure pack. That earlier plan is now stale and must not be used as a copy-paste template for exact JSON shapes. This plan's JSON snippets are copied from the pack's **current, real** contents (`pack.json` version `"5"`, flow `factory-governed@2.json`, `gate.assert@1` for governance gates, `config.lint_argv`/`config.test_argv` refs instead of hardcoded argv arrays). Always diff against the actual current file before applying an edit from this plan — if a file has changed further since this plan was written, stop and re-read it rather than blindly applying a stale diff.
- **No flow/pack/subflow version bump.** Edit `subflows/integrated-review@1.json` and `subflows/governance-cycle@1.json` **in place** — do not rename to `@2`. `factory-governed@2.json` calls `subflow:integrated-review@1` by name+version; the subflow's internal content can change freely without the flow needing an edit. `pack.json`'s top-level `"version"` stays `"5"`.
- **`fallbackOutput` for the new step is `{"approved": false, ...}` — fail-closed, matching `qa`/`adversary`/`critic`.** This is the opposite of the pattern a *different*, unrelated spec (a "refactor pass," not part of this plan) used for its own new role, where an unchecked optional-improvement was safe to skip. Here, a visual/UX defect that goes unchecked because the role's agent crashed or produced no valid checkpoint is exactly as risky as an unchecked QA or adversary finding — it must block governance approval, not default to a pass. Do not copy a `fallbackOutput.approved: true` pattern for this role.
- **Prompt interpolation has no conditionals** (`internal/prompts/prompts.go`'s `Interpolate` is `strings.ReplaceAll` — a `{{VAR}}` with no matching key is left **literally, unresolved** in the prompt text sent to the agent). `visual_verifier` is inserted as a genuine first-class finder wired with real context everywhere `QA_REVIEW`/`CRITIC_REVIEW`/`ADVERSARY_REVIEW` already appear (`integration_repair` in `integrated-review@1.json`, `repair` in `governance-cycle@1.json`, and the prompt text of `integrator.md`/`gov-reviewer.md`) — there is no "n/a" literal to seed anywhere, because every one of those call sites gets the real `{"$ref": "steps.visual_verifier.output"}` (or the subflow-input equivalent), not a placeholder string.
- **Reuse `schema:review-result@1` (`{approved, summary, findings}`) as-is.** No new schema files. The v1 prompt's original `{status, checks: [...]}` shape does **not** get ported verbatim — each `checks[]` entry collapses into one `findings[]` string carrying the same evidence (name/action/expected/actual), so the role fits the existing repair/governance machinery without a new mechanism.
- **The source v1 prompt no longer exists in the working tree** (the whole legacy `factory` command was removed by commit `2bd3b0a`, "make durable v2 the only runtime"). Its last content is retrievable via `git show c7f6ff4:internal/commands/assets/prompts/factory-visual-verify.md` — this plan already extracted and adapted that content below; no task needs to run that git command itself.
- **Validate every new/modified JSON file with `orq-lite flow validate <path>`** before considering its task done. Build the binary once at the start:

  ```bash
  go build -o /tmp/orq-lite-plan ./cmd/orq-lite
  ```

  Use `/tmp/orq-lite-plan` for every validation step in this plan.

---

### Task 1: Add the `visual_verifier` prompt and wire the role into `team.json`

**Files:**
- Create: `examples/governed-pack/pack/prompts/visual-verifier.md`
- Modify: `examples/governed-pack/team.json`

**Interfaces:**
- Produces: role name `visual_verifier`, output contract `schema:review-result@1` (`{"approved": bool, "summary": string, "findings": [string]}`) written to `.orquestalite/results/visual_verifier.json`. Task 2 wires this role into `integrated-review@1.json` by name.

- [ ] **Step 1: Write `prompts/visual-verifier.md`**

```markdown
Read the complete contract at {{FEATURES_PATH}} and the prior review
findings below. You verify the feature the way a human would — in a real
browser, not by reading tests. Do not modify source or test files. The only
file you may write is `.orquestalite/results/visual_verifier.json`.

Global QA result: {{QA_REVIEW}}
Adversarial falsification result: {{ADVERSARY_REVIEW}}
Critic result: {{CRITIC_REVIEW}}

## Step 0 — does this project even have a UI to verify?

Before touching a browser, check whether the repository has a real
user-facing UI surface: a frontend build script (e.g. a `package.json` with
a `dev`/`start` script), an `index.html`, or a templated HTML response the
contract describes. A pure API/backend service with no such surface has
nothing for this check to verify.

If there is no UI surface, write this and stop — do not install or invoke
`agent-browser`:

{"approved":true,"summary":"no UI surface found in this project; nothing to verify visually","findings":[]}

## If there is a UI: verify it in a real browser

1. Start the app's dev server in the background; give it a moment to boot.
   Find the start command and URL from the repo (package.json scripts,
   README, the feature contract). Note the base URL.
2. Drive a real browser with the **`agent-browser`** CLI (preferred):
   - `agent-browser open <url>` — navigate to each affected page.
   - `agent-browser snapshot -i --json` — read the interactive elements/refs.
   - `agent-browser click @eN` / `agent-browser type @eN "text"` — exercise
     the flows the feature promises, using refs from the snapshot.
   - `agent-browser screenshot --annotate <path>` — capture visual evidence.
   - Assert the visible text/elements the feature promises exist, and that
     the state changes the feature describes actually happen on screen.
   - Treat any console error or uncaught exception as a failure.
   If `agent-browser` is not installed, fall back in this order: a
   playwright MCP tool, then `npx playwright`, then (last resort) `curl` +
   HTML inspection — and say which one you used in the finding.
3. Run the production build if one exists and confirm it succeeds.
4. Clean up: kill the dev server and any browser session you started.

## Optional check — compare against a Figma reference image

If the current ticket's acceptance criteria in {{FEATURES_PATH}} reference a
design image under `design/<file>` (a path the user exported manually from
Figma), take a screenshot of the corresponding real page and compare it
against that reference image as one more check in the same list below —
same evidence standard as every other check: never approve a visual match
you did not actually look at side by side. If no ticket references a
`design/` image, skip this check entirely — it is optional, not a gap to
report.

## Rules

- Cover each user-facing acceptance criterion with at least one check.
- Every check needs **observed evidence**: the rendered text, a screenshot
  path, a status, or the console state you actually saw. Never pass a check
  unseen.
- If the app cannot start or a page cannot load, that is a failed check —
  report what happened, do not attempt to fix it yourself.
- A prior QA/adversary/critic finding already covering the same UI defect
  does not exempt you from reporting it here too if you observe it directly
  — do not assume it is already handled.

Before finishing, write JSON only to
`.orquestalite/results/visual_verifier.json`. Collapse each check into one
`findings[]` string carrying its own evidence inline (name, action taken,
expected, actual observed):

{"approved":false,"summary":"evidence-based verdict","findings":["capacity page: expected a red 'Over-allocated' badge on row for Ana after agent-browser open /capacity; snapshot -i — badge absent, screenshot tmp/cap.png"]}

Set `approved` true only when every check you performed passed, or when
Step 0 determined there is no UI surface to verify.
```

- [ ] **Step 2: Add the role to `examples/governed-pack/team.json`**

Add this entry to the `"roles"` object, next to the existing `"gov_reviewer"` entry (matches its `timeout_seconds` — same judgment-tier budget, and browser startup/navigation needs headroom):

```json
    "visual_verifier": {"agents": ["haiku"], "prompt": ".orquestalite/packs/development/5/prompts/visual-verifier.md", "result_path": ".orquestalite/results/visual_verifier.json", "timeout_seconds": 1500},
```

- [ ] **Step 3: Verify JSON validity**

Run: `python3 -c "import json; json.load(open('examples/governed-pack/team.json'))" && echo OK`
Expected: `OK`

- [ ] **Step 4: Commit**

```bash
git add examples/governed-pack/pack/prompts/visual-verifier.md examples/governed-pack/team.json
git commit -m "feat(governed-pack): add visual_verifier role"
```

---

### Task 2: Wire `visual_verifier` into `integrated-review@1.json` as a first-class finder

**Files:**
- Modify: `examples/governed-pack/pack/subflows/integrated-review@1.json`

**Interfaces:**
- Consumes: role `visual_verifier` (Task 1).
- Produces: subflow step output `steps.visual_verifier.output` (`schema:review-result@1`), and a new `visual_review` entry in the subflow's `outputs` block for `governance-cycle@1` (Task 3) and `governance` (this task) to consume; `integration_repair` and `governance` both gain a real `VISUAL_REVIEW` context key.

- [ ] **Step 1: Replace `examples/governed-pack/pack/subflows/integrated-review@1.json` in full**

The current file has this structure (steps in order: `lint`, `tests`, `qa`, `adversary`, `critic`, `integration_repair`, `final_lint`, `final_tests`, `governance`, `governance_repair`, `governance_gate`). Replace the whole file with this content — it inserts the new `visual_verifier` step after `critic`, adds `VISUAL_REVIEW` to `integration_repair`'s and `governance`'s context, and adds `visual_verifier`/`visual_review` to `governance_repair`'s `with` block and the subflow's own `outputs`:

```json
{
 "apiVersion": "orq.dev/v2",
 "kind": "Subflow",
 "metadata": {
  "name": "integrated-review",
  "version": "1"
 },
 "inputs": {
  "features_path": {
   "schema": "schema:path@1"
  }
 },
 "steps": [
  {
   "id": "lint",
   "uses": "activity:gate.run@1",
   "with": {
    "argv": {"$ref": "config.lint_argv"}
   }
  },
  {
   "id": "tests",
   "uses": "activity:gate.run@1",
   "with": {
    "argv": {"$ref": "config.test_argv"}
   }
  },
  {
   "id": "qa",
   "uses": "activity:agent.invoke@1",
   "with": {
    "role": "qa",
    "outputSchema": "schema:review-result@1",
    "vars": {
     "FEATURES_PATH": {
      "$ref": "inputs.features_path"
     }
    },
    "fallbackOutput": {
     "approved": false,
     "summary": "global QA unavailable; critic and integration repair must continue fail-closed",
     "findings": [
      "global QA did not produce a valid checkpoint; critic must inspect independently and final governance must not assume approval"
     ]
    }
   }
  },
  {
   "id": "adversary",
   "uses": "activity:agent.invoke@1",
   "with": {
    "role": "adversary",
    "outputSchema": "schema:review-result@1",
    "context": {
     "FEATURES_PATH": {
      "$ref": "inputs.features_path"
     },
     "QA_REVIEW": {
      "$ref": "steps.qa.output"
     }
    },
    "fallbackOutput": {
     "approved": false,
     "summary": "adversary unavailable; critic and governance must not assume falsification ran",
     "findings": [
      "adversarial falsification did not produce a valid checkpoint; downstream reviews must not treat the system as falsification-tested"
     ]
    }
   }
  },
  {
   "id": "critic",
   "uses": "activity:agent.invoke@1",
   "with": {
    "role": "critic",
    "outputSchema": "schema:review-result@1",
    "context": {
     "FEATURES_PATH": {
      "$ref": "inputs.features_path"
     },
     "QA_REVIEW": {
      "$ref": "steps.qa.output"
     },
     "ADVERSARY_REVIEW": {
      "$ref": "steps.adversary.output"
     }
    },
    "fallbackOutput": {
     "approved": false,
     "summary": "critic unavailable; integration repair must continue from global QA evidence",
     "findings": [
      "critic did not produce a valid checkpoint; preserve QA findings and require final governance review"
     ]
    }
   }
  },
  {
   "id": "visual_verifier",
   "uses": "activity:agent.invoke@1",
   "with": {
    "role": "visual_verifier",
    "outputSchema": "schema:review-result@1",
    "context": {
     "FEATURES_PATH": {
      "$ref": "inputs.features_path"
     },
     "QA_REVIEW": {
      "$ref": "steps.qa.output"
     },
     "ADVERSARY_REVIEW": {
      "$ref": "steps.adversary.output"
     },
     "CRITIC_REVIEW": {
      "$ref": "steps.critic.output"
     }
    },
    "fallbackOutput": {
     "approved": false,
     "summary": "visual verifier unavailable; integration repair and governance must not assume the UI was checked",
     "findings": [
      "visual verification did not produce a valid checkpoint; downstream reviews must not treat the UI as browser-tested"
     ]
    }
   }
  },
  {
   "id": "integration_repair",
   "uses": "activity:agent.invoke@1",
   "while": {
    "condition": "item.continue == true",
    "maxIterations": 3,
    "initial": {
     "continue": true,
     "summary": "global QA and critic findings must be reconciled",
     "remaining": [
      "review QA and critic findings"
     ]
    }
   },
   "with": {
    "role": "integrator",
    "outputSchema": "schema:iteration-result@1",
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
     "VISUAL_REVIEW": {
      "$ref": "steps.visual_verifier.output"
     }
    }
   }
  },
  {
   "id": "final_lint",
   "uses": "activity:gate.run@1",
   "with": {
    "argv": {"$ref": "config.lint_argv"}
   }
  },
  {
   "id": "final_tests",
   "uses": "activity:gate.run@1",
   "with": {
    "argv": {"$ref": "config.test_argv"}
   }
  },
  {
   "id": "governance",
   "uses": "activity:agent.invoke@1",
   "with": {
    "role": "gov_reviewer",
    "outputSchema": "schema:review-result@1",
    "vars": {
     "FEATURES_PATH": {
      "$ref": "inputs.features_path"
     }
    },
    "context": {
     "QA_REVIEW": {
      "$ref": "steps.qa.output"
     },
     "CRITIC_REVIEW": {
      "$ref": "steps.critic.output"
     },
     "ADVERSARY_REVIEW": {
      "$ref": "steps.adversary.output"
     },
     "VISUAL_REVIEW": {
      "$ref": "steps.visual_verifier.output"
     }
    }
   }
  },
  {
   "id": "governance_repair",
   "uses": "subflow:governance-cycle@1",
   "while": {
    "condition": "item.approved != true",
    "maxIterations": 2,
    "initial": {
     "approved": {
      "$ref": "steps.governance.output.approved"
     },
     "verdict": {
      "$ref": "steps.governance.output"
     }
    }
   },
   "with": {
    "features_path": {
     "$ref": "inputs.features_path"
    },
    "verdict": {
     "$ref": "item.verdict"
    },
    "qa_review": {
     "$ref": "steps.qa.output"
    },
    "critic_review": {
     "$ref": "steps.critic.output"
    },
    "adversary_review": {
     "$ref": "steps.adversary.output"
    },
    "visual_review": {
     "$ref": "steps.visual_verifier.output"
    }
   }
  },
  {
   "id": "governance_gate",
   "uses": "activity:gate.assert@1",
   "if": "steps.governance.output.approved != true",
   "with": {
    "value": {"$ref": "steps.governance_repair.output.last.approved"},
    "equals": true,
    "message": "governance did not approve after the repair cycles"
   }
  }
 ],
 "outputs": {
  "qa": {
   "$ref": "steps.qa.output"
  },
  "critic": {
   "$ref": "steps.critic.output"
  },
  "repairs": {
   "$ref": "steps.integration_repair.output"
  },
  "governance": {
   "$ref": "steps.governance.output"
  },
  "governance_repair": {
   "$ref": "steps.governance_repair.output"
  },
  "adversary": {
   "$ref": "steps.adversary.output"
  },
  "visual_verifier": {
   "$ref": "steps.visual_verifier.output"
  }
 }
}
```

- [ ] **Step 2: Validate**

Run: `/tmp/orq-lite-plan flow validate examples/governed-pack/pack/subflows/integrated-review@1.json`
Expected: `valid integrated-review@1 <hash>` (hash will differ from before the edit — that is expected, it is a content digest)

- [ ] **Step 3: Commit**

```bash
git add examples/governed-pack/pack/subflows/integrated-review@1.json
git commit -m "feat(governed-pack): wire visual_verifier into integrated-review@1"
```

---

### Task 3: Wire `visual_review` into `governance-cycle@1.json`'s repair step

**Files:**
- Modify: `examples/governed-pack/pack/subflows/governance-cycle@1.json`

**Interfaces:**
- Consumes: nothing new from Task 1/2 directly — receives `visual_review` as a subflow input, supplied by Task 2's edit to `governance_repair`'s `with` block.
- Produces: `subflows/governance-cycle@1.json` now declares a `visual_review` input (`schema:review-result@1`) and passes it through to its `repair` step's context as `VISUAL_REVIEW`.

- [ ] **Step 1: Add the `visual_review` input declaration**

In `examples/governed-pack/pack/subflows/governance-cycle@1.json`, find the `inputs` block:

```json
 "inputs": {
  "features_path": {
   "schema": "schema:path@1"
  },
  "verdict": {
   "schema": "schema:review-result@1"
  },
  "qa_review": {
   "schema": "schema:review-result@1"
  },
  "critic_review": {
   "schema": "schema:review-result@1"
  },
  "adversary_review": {
   "schema": "schema:review-result@1"
  }
 },
```

Add a `visual_review` entry so it reads:

```json
 "inputs": {
  "features_path": {
   "schema": "schema:path@1"
  },
  "verdict": {
   "schema": "schema:review-result@1"
  },
  "qa_review": {
   "schema": "schema:review-result@1"
  },
  "critic_review": {
   "schema": "schema:review-result@1"
  },
  "adversary_review": {
   "schema": "schema:review-result@1"
  },
  "visual_review": {
   "schema": "schema:review-result@1"
  }
 },
```

- [ ] **Step 2: Add `VISUAL_REVIEW` to the `repair` step's context**

Find the `repair` step's `context` object:

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
     }
    }
```

Add one entry so it reads:

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
     "VISUAL_REVIEW": {
      "$ref": "inputs.visual_review"
     }
    }
```

- [ ] **Step 3: Validate**

Run: `/tmp/orq-lite-plan flow validate examples/governed-pack/pack/subflows/governance-cycle@1.json`
Expected: `valid governance-cycle@1 <hash>`

- [ ] **Step 4: Commit**

```bash
git add examples/governed-pack/pack/subflows/governance-cycle@1.json
git commit -m "feat(governed-pack): thread visual_review into governance-cycle@1"
```

---

### Task 4: Extend `integrator.md` and `gov-reviewer.md` to genuinely reconcile visual findings

**Files:**
- Modify: `examples/governed-pack/pack/prompts/integrator.md`
- Modify: `examples/governed-pack/pack/prompts/gov-reviewer.md`

**Interfaces:**
- Consumes: nothing new (both prompts already exist; this task only edits their text).
- Produces: both prompts now reference `{{VISUAL_REVIEW}}` and treat it as a fourth review of equal weight to QA/critic/adversary — genuine reconciliation language, not a placeholder-safety literal (unlike a different, unrelated spec's `REFACTOR_REVIEW`, which only needed an "n/a" fed to it because it ran in an isolated phase after governance already approved).

- [ ] **Step 1: Rewrite `prompts/integrator.md`**

Replace the full file with:

```markdown
You repair only the integrated findings produced after all planned tickets.
Work directly in the repository. Do not commit.

Canonical contract: {{FEATURES_PATH}}
QA review: {{QA_REVIEW}}
Critic review: {{CRITIC_REVIEW}}
Adversarial falsification review: {{ADVERSARY_REVIEW}}
Visual/UX review: {{VISUAL_REVIEW}}
Feedback driving this repair pass (previous repair state, or a governance
rejection verdict if this pass was triggered by a governance repair cycle):
{{FEEDBACK}}

Reproduce and address every actionable finding from ALL of QA, critic,
adversary, AND the visual/UX review — not only whichever is easiest or
largest. An adversarial finding is a reproduced defect with a concrete
repro, not a suspicion; a visual/UX finding is observed evidence from a real
browser session, not a suspicion either — neither outranks the others by
default, but neither may be silently dropped in favor of a different
finding just because there was only time for one. If you cannot fix
everything in this pass, say so explicitly in `remaining` and name which
specific findings are still open — do not report `continue:false` while any
QA, critic, adversary, or visual/UX finding remains unaddressed.

Strengthen tests so each fix is observable — if a finding came with its own
reproduction (a script, a test, explicit steps), turn that reproduction into
a permanent regression test under `tests/` if one does not already exist, so
the gates themselves catch a future regression. Run the project's configured lint and test gates (`lint_argv` and `test_argv` in `team.json` — the same commands this flow's gate steps run).

Before finishing, write JSON only to `.orquestalite/results/integrator.json`:

{"continue":false,"summary":"what changed and exact gate status","remaining":[]}

Set continue false only when all QA, critic, adversary, AND visual/UX
findings are resolved and both gates pass. Otherwise keep it true and list
concrete remaining findings, including which ones are adversary-sourced or
visual/UX-sourced.
```

- [ ] **Step 2: Rewrite `prompts/gov-reviewer.md`**

Replace the full file with:

```markdown
Read {{FEATURES_PATH}}, the complete repository, all tests, and the hard
conventions. Do not modify files. Verify the public contract end to end and run the project's configured lint and test gates (`lint_argv` and `test_argv` in `team.json` — the same commands this flow's gate steps run). Check that dynamic ticket
boundaries did not leave integration gaps or scope-driven stubs.

Global QA result: {{QA_REVIEW}}
Critic result: {{CRITIC_REVIEW}}
Adversarial falsification result: {{ADVERSARY_REVIEW}}
Visual/UX review: {{VISUAL_REVIEW}}

For every `approved:false` finding above, independently confirm — by reading
the current code and, where practical, re-running the reproduction or the
browser check — whether it is now actually fixed. Do not approve on the
strength of a prior repair pass's own claim alone. An adversarial finding or
a visual/UX finding that is still reproducible against the current tree is
blocking, exactly like a QA or critic finding; it does not matter whether
the repair step addressed a different finding instead. If any of these four
reviews is missing, unavailable, or a fail-closed fallback (its own summary
will say so), treat that as a blocking gap yourself rather than assuming the
missing review would have approved.

Before finishing, write JSON only to
`.orquestalite/results/gov_reviewer.json`:

{"approved":false,"summary":"final governance verdict","findings":["blocking evidence"]}

Set approved true only when the complete contract is observably met, both gates
pass, every QA/critic/adversary/visual finding above is independently
confirmed resolved, and no blocking finding remains.
```

- [ ] **Step 3: Grep-verify every `integrator`/`gov_reviewer` call site now supplies `VISUAL_REVIEW`**

Run: `grep -L "VISUAL_REVIEW" examples/governed-pack/pack/subflows/integrated-review@1.json examples/governed-pack/pack/subflows/governance-cycle@1.json`
Expected: **no output** (empty — `-L` prints files that do NOT contain the string; empty output confirms both subflows now supply it to every `integrator`/`gov_reviewer` invocation)

- [ ] **Step 4: Commit**

```bash
git add examples/governed-pack/pack/prompts/integrator.md examples/governed-pack/pack/prompts/gov-reviewer.md
git commit -m "feat(governed-pack): teach integrator and gov-reviewer to reconcile visual findings"
```

---

### Task 5: Regenerate `pack.json` digests

**Files:**
- Modify: `examples/governed-pack/pack/pack.json` (regenerated, not hand-edited)

**Interfaces:**
- Consumes: every file under `examples/governed-pack/pack/` (Tasks 1-4's new and modified files).
- Produces: an updated `"files"` map in `pack.json` with correct SHA-256 digests for the 1 new file (`prompts/visual-verifier.md`) and the 4 modified files (`subflows/integrated-review@1.json`, `subflows/governance-cycle@1.json`, `prompts/integrator.md`, `prompts/gov-reviewer.md`).

- [ ] **Step 1: Run the existing digest script**

Run: `python3 examples/governed-pack/regen-digests.py`
Expected output: `pack.json: N files digested` where N is one more than the current count. The current `pack.json` (version `"5"`) has 35 entries; expect **36** after this task.

- [ ] **Step 2: Review the diff before committing — this script legitimizes every file it finds under `pack/`, including accidental ones**

Run: `git diff examples/governed-pack/pack/pack.json`
Expected: entries changed only for `subflows/integrated-review@1.json`, `subflows/governance-cycle@1.json`, `prompts/integrator.md`, `prompts/gov-reviewer.md` (modified — new hashes), plus 1 new entry added (`prompts/visual-verifier.md`). No unrelated entries should appear or disappear — if any do, something was accidentally left in or removed from `pack/`, stop and investigate before proceeding.

- [ ] **Step 3: Commit**

```bash
git add examples/governed-pack/pack/pack.json
git commit -m "chore(governed-pack): regenerate pack.json digests for visual verification"
```

---

### Task 6: Update `examples/governed-pack/README.md`

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
   lint → tests → qa → adversary → critic → visual_verifier
   → integration_repair   [loop: reconcile findings, incl. visual/UX]
   → gates → governance
   → governance_repair     [loop ×2: repair → gates → FRESH re-audit]
   → governance_gate       (fail-closed)
```
```

- [ ] **Step 2: Add a fourth numbered lesson after the existing three**

Find the end of the numbered list (after "...blocking findings." for item 3, before "## Flows"). Add:

```markdown
4. **A `visual_verifier` role that checks the feature the way a human
   would.** Not another test runner — a real browser session
   (`agent-browser`, with a playwright/curl fallback chain) that opens the
   app, exercises the flows the feature promises, and requires observed
   evidence (rendered text, a screenshot, console state) for every check,
   never approving one unseen. It self-detects when a project has no UI
   surface at all (a pure API service) and skips cleanly rather than
   forcing a browser check where none applies. When a ticket references a
   design reference image under `design/<file>`, the same role folds a
   Figma comparison into the same evidence list — one mechanism, not two.
   Its findings carry the same weight as QA, critic, and adversary: they
   feed the same `integration_repair` loop and the same final `governance`
   verdict, so an unfixed visual/UX defect blocks approval exactly like an
   unfixed bug does.
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
  `adversary`, `critic`, `gov_reviewer`, `visual_verifier`). The review roles
  are where the bugs are caught, and in the benchmark they were ~78% of a
  governed run's cost — that spend is the point, not the overhead.
```

- [ ] **Step 4: Commit**

```bash
git add examples/governed-pack/README.md
git commit -m "docs(governed-pack): document the visual_verifier finder"
```

---

### Task 7: End-to-end structural check on a scratch project

**Files:** none in the repo — this task operates entirely in a scratch directory outside version control.

**Interfaces:** none new; this task only verifies Tasks 1-6 compose correctly through `orq-lite doctor` and an installed-pack `flow validate` (static checks — no real agent invocation, no browser session, no API cost).

- [ ] **Step 1: Scaffold a throwaway project**

```bash
rm -rf /tmp/visual-verify-check && mkdir -p /tmp/visual-verify-check && cd /tmp/visual-verify-check
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
Expected: `pack install` reports the pack installed at version `5` with no digest mismatch errors — this is the real test that Task 5's digests are correct.

- [ ] **Step 3: Doctor check**

Run: `/tmp/orq-lite-plan doctor`
Expected: no `unknown role` / `missing prompt` errors for `visual_verifier`. A `binary:agent-browser` line should appear among the doctor checks (confirming the tool-level integration this role depends on is already recognized by `doctor`, independent of this plan's changes). A `git tree has uncommitted changes` warning is fine here since nothing has run yet — ignore it.

- [ ] **Step 4: Validate the installed flow (not just the raw path)**

Run: `/tmp/orq-lite-plan flow validate development/factory-governed@2`
Expected: `valid factory-governed@2 <hash>` — confirms the installed copy resolves cleanly through the pack's own subflow references (`integrated-review@1`, which now includes `visual_verifier`) with no broken `$ref`.

- [ ] **Step 5: Clean up**

```bash
rm -rf /tmp/visual-verify-check
```

(No commit — this task touches no repo files.)

---

## Self-Review

**1. Spec coverage:**
- "No son dos subproyectos — es uno solo" (Figma folds into the same role) → Task 1's prompt has both the self-detection step AND the Figma check in one file, one `findings[]` list. ✓
- "Imagen exportada a mano, convención de carpeta + referencia en features.md" → Task 1's prompt reads `design/<file>` references from the ticket's own acceptance criteria in `{{FEATURES_PATH}}`, no new flow input, no MCP/API. ✓
- "Encaje: mismo paso, check adicional" → one role, one prompt, one `findings[]` list handles both the browser check and the Figma check. ✓
- "Ubicación en el pipeline: adentro de integrated_review, junto a qa/adversary/critic" → Task 2 inserts `visual_verifier` inside `integrated-review@1.json`, not after governance. ✓
- "Auto-detección de ausencia de UI" → Task 1's prompt Step 0. ✓
- "Wiring genuino, no placeholder" → Task 2 wires real `VISUAL_REVIEW` refs into `integration_repair`/`governance` context; Task 3 wires it into `governance-cycle@1`'s `repair` step; Task 4 rewrites `integrator.md`/`gov-reviewer.md` prompt *text*, not just a fed literal. ✓
- "`fallbackOutput` debe ser `approved:false`, no `true` como refactorer" → Task 2's `visual_verifier` step fallback is explicit `approved: false`, and the Global Constraints section calls out the asymmetry so an implementer does not copy the wrong pattern. ✓
- Verificación section of the spec (flow validate, no-UI auto-skip, planted UI defect, planted Figma mismatch, digest protocol) → Task 7 covers `flow validate`/`doctor` mechanically; the planted-defect and planted-Figma-mismatch live runs are explicitly **not** a plan task (real API/browser cost, the user's own follow-up), same treatment the refactor-pass plan gave its own live-run verification step.

**2. Placeholder scan:** No TBD/TODO; every step has literal file content, not descriptions of content.

**3. Type/interface consistency:** `visual_verifier`'s output is `schema:review-result@1` (`approved`/`summary`/`findings`) everywhere it's referenced (Tasks 1, 2, 3, 4) — same schema `qa`/`adversary`/`critic` already use, no new schema. Context key name `VISUAL_REVIEW` (Tasks 2, 3, 4) and subflow input/output name `visual_review` (Tasks 2, 3 — lowercase, matching the existing `qa_review`/`critic_review`/`adversary_review` naming convention on `governance-cycle@1`'s inputs) are used consistently and distinctly from each other exactly the way the existing `QA_REVIEW`/`qa_review` pair already is.

**Deviation from the original design doc, and why:** the design doc's JSON sketch (written 2026-08-02) was drafted before the pack's 2026-07-30 restructure was accounted for in this planning pass. This plan corrects the JSON to the pack's real current shape (`config.lint_argv`/`config.test_argv` refs instead of hardcoded `uv run ruff check .` argv arrays, `activity:gate.assert@1` instead of the old inline Python gate script for `governance_gate`, pack version `"5"` and flow `factory-governed@2` instead of `"1"`) — the design *intent* (self-detection, Figma-as-one-more-check, fail-closed fallback, first-class wiring) is unchanged, only the literal JSON syntax is updated to match what actually exists in the repository today.

---

Plan complete and saved to `docs/superpowers/plans/2026-08-02-governed-visual-verification.md`. Two execution options:

**1. Subagent-Driven (recommended)** - I dispatch a fresh subagent per task, review between tasks, fast iteration

**2. Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints

**Which approach?**
