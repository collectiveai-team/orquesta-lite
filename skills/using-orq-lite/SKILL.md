---
name: using-orq-lite
description: Use when working with orq-lite flows — writing a team.json, choosing and launching a flow, authoring or editing a v2 flow/subflow/pack, monitoring or recovering a run that failed, hung, sits in needs_human or aborted with "agent.invoke permanent", or enabling the rtk and headroom context-optimization tools.
---

# Working with orq-lite flows

orq-lite runs coding-agent workflows durably. Behavior is **data**: versioned v2 flows compose activities and subflows, shipped with schemas, policies and prompts inside verified packs. You change a flow, validate it, and run it — there is no imperative runtime to patch.

Two things you need before any flow runs: a `team.json` that maps roles to agents, and a flow to run. `guide.md` in the repo root is the full setup narrative (gates, conventions, objective writing, GitHub watch mode). This skill is the working reference for the flow lifecycle.

## 1. team.json

`Validate()` enforces exactly this, and rejects the config before a run is created:

- at least one agent
- every role: non-empty `agents`, all resolving to declared agents; non-empty `prompt` and `result_path`; `timeout_seconds > 0`
- each agent declares **either** `provider` **or** `cmd`, never both

Minimum viable config for the shipped pack:

```json
{
  "agents": {
    "primary": {
      "provider": "claude",
      "model": "claude-sonnet-5",
      "dangerously_skip_permissions": true,
      "rate_limit_pattern": "(?i)(rate[ _-]?limit|429|quota|too many requests|session limit|usage limit|spend limit|credit balance|overloaded)"
    }
  },
  "roles": {
    "coder": {
      "agents": ["primary"],
      "prompt": ".orquestalite/packs/development/5/prompts/coder.md",
      "result_path": ".orquestalite/results/coder.json",
      "timeout_seconds": 1800
    }
  },
  "conventions_file": "CONVENTIONS.md",
  "lint_argv": ["uv", "run", "ruff", "check", "."],
  "test_argv": ["uv", "run", "pytest", "-q"]
}
```

Providers: `claude`, `codex`, `gemini`, `opencode`. Optional per agent: `effort`, `safe_mode`.

**A flow only resolves the roles it references** — there is no required role set. Read the flow's steps to know which roles to declare. `factory-governed@2` needs `ticket_planner`, `coder`/`batch_coder`, `ticket_qa`, `qa`, `adversary`, `critic`, `integrator`, `gov_reviewer`.

`lint_argv` / `test_argv` are argv, not shell strings, because flows run them directly. A flow referencing `config.lint_argv` when the project does not declare it **cannot start** — the pre-run validator rejects it.

Two fields that decide whether a run survives trouble:

- **`rate_limit_pattern` must match what your provider actually says.** A throttle the regex misses is classified *permanent* and aborts the run, surfacing as `agent.invoke permanent: all agents for role X are marked skipped` — which reads as a role misconfiguration and is not. The pattern above covers `spend limit` and `rate limit` with a space; older scaffolds miss both.
- **`escalation_ladder`** lists fallback agents tried after the primary chain is exhausted.

## 2. Choosing a flow

| Situation | Flow | Alias |
|---|---|---|
| Build a whole spec, with governance | `factory-governed@2` | `factory` |
| Decompose a plan into tickets only | `plan-tickets@1` | `plan` |
| Tickets already exist | `task-list@1` | `run` |
| Code exists, needs auditing | `review-existing@1` | — |
| A PR needs a verdict | `pr-review@1` | `review` |
| A GitHub issue needs triage then work | `issue-fix@1` | `intake` |

`factory-governed@2` takes `fast` (default **true**): `true` gives the whole backlog to `batch_coder` in one pass; `false` runs one ticket per iteration through `develop-ticket@1`.

**That choice is not economic.** Measured on the same spec: batch $30.51 / 94 min, per-ticket $31.63 / 101 min, identical output quality on four independent instruments. Choose on **feedback locality** — per-ticket catches a defect against a small diff attributed to one ticket and re-scopes it through the planner at 89% fidelity; batch delivers findings after dozens of files are written, unattributable, with one repair loop capped for the whole change set. A single long session also costs ~28% more than N short ones for the same work, because a growing prefix is re-read every turn.

## 3. Running

```bash
orq-lite doctor                                    # preflight: packs, roles, prompts, gates, CLIs, credentials
orq-lite flow validate <pack>/<flow>@<v>           # compiles without executing
orq-lite flow run <pack>/<flow>@<v> features_path=features.md fast=false
orq-lite factory features.md                       # alias equivalent
```

`flow run` accepts `key=value` inputs, `--policy=<ref|path>` and `--source-key=<stable-key>` for idempotent external triggers. Both `doctor` and `flow validate` are free — a run that dies on validation after ten minutes of agent time would have died for free ten minutes earlier.

## 4. Monitoring and recovering

```bash
orq-lite status [--watch]                          # all durable runs
orq-lite flow status|events <run-id>               # one run's state / history
orq-lite log [--role R] [--event T]                # replay agent traffic
orq-lite cost                                      # spend rollup
orq-lite flow resume|cancel <run-id>
orq-lite flow approve <run-id> <approval-id> --decision approve|reject
```

| Symptom | Cause and fix |
|---|---|
| `agent.invoke permanent: all agents … marked skipped` | usually a throttle the `rate_limit_pattern` missed, not a role problem |
| `status=needs_human` | `agent.invoke` died mid-call; use `approve` or `cancel` — `resume` alone returns `workflow needs human input` |
| `workflow duration budget exhausted`, little agent time | `maxDurationSeconds` is **wall-clock**; a run idling or on a sleeping machine spends it doing nothing |
| `status=running`, no live process | zombie; check `flow status` before assuming progress |
| Result written but validation failed | `outputSchema` and the prompt's declared shape disagree |
| A literal `{{VAR}}` in `runs/*/agents/*/prompt.md` | key unsupplied at that call site |
| `gate.run gate_failed … permanent` and the run ends | **a failing gate aborts the whole flow** — see below |

**Verify from the stream, not the summary.** `.orquestalite/runs/<run-id>/agents/<activity>/<invocation>/stdout.log` is evidence; the agent's own result JSON is a claim. And `results/by-task/` accumulates across runs (the `.rN` suffix never resets), so run history cannot be reconstructed from it.

**Recompile before verifying anything against a binary.** A stale binary has already produced a wrong conclusion in this project — seven runs postdated a fix and still carried the bug it fixed.

## 5. Runtime semantics that change how you write flows

- **A failing `gate.run` aborts the entire flow.** The following step never executes. To hand a gate's result to a reviewer *as data*, use `command.run` with `argv: ["sh","-c","<cmd>; echo EXIT=$?"]` — always exits 0, real code in `stdout`. Do not "fix" gate ordering by deleting an `if:` guard; that guard is usually what keeps a red test from killing the run.
- **`agent.invoke@1` is `EffectAtMostOnce`, so policy retry never applies.** The scheduler gates retries to Pure/Idempotent/Reconcilable. Its recovery is the bounded same-agent corrective retry inside `internal/invoke` plus the multi-agent fallback chain.
- **`{{VAR}}` is a plain `strings.ReplaceAll`** — no conditionals, no error on unknown keys. Adding a var to a *shared* prompt means supplying it at every call site, even as `"n/a"`.
- **Injected context is billed per turn**: cost ≈ size × turns × consuming prompts. A 1,335-token note in three prompts made a run 19% more expensive with no measurable benefit.

## 6. Authoring flows

Read `authoring-flows.md` in this skill directory: the flow/subflow JSON shape, the six activities and their effect classes, `if:`/`while:` controls, `$ref` wiring between steps, prompt and schema contracts, pack layout, and the three traps that make a valid-looking flow invisible or unvalidatable.

## 7. rtk and headroom

Two external tools cut what each invocation carries. Both are **on by default** and both **degrade silently when absent** — a run without them works, it costs more. Measured on the same spec: the compression proxy −38%, the command filter −25%, with identical output.

```bash
orq-lite doctor    # says which are actually active
```

```
[PASS] compression_proxy      reachable at http://127.0.0.1:8787
[PASS] command_filter         verified: /opt/homebrew/bin/rtk
```

A `[WARN]` on either means the run proceeds without it — deliberate, but it is money.

**headroom** is the compression proxy: a **daemon** that must already be running. orq-lite probes the address and never supervises it.

```bash
uv tool install --python 3.13 "headroom-ai[all]"   # NOT "headroom" — different project on PyPI
headroom proxy --port 8787                          # leave running
```

**rtk** is the command filter: a one-shot binary invoked by a `PreToolUse` hook, no daemon.

```bash
brew install rtk && command -v rtk    # must resolve by NAME
```

Two traps, both verified:

- **Do not run `rtk init -g`.** It writes a hook into your global `~/.claude/settings.json`, affecting every unrelated session. orq-lite writes the equivalent hook into the *project's* `.claude/settings.json`, merging with any hooks already there. (`rtk init -g` also prompts and answers *no* without a TTY, so in automation it silently does nothing.)
- **The binary must be on `PATH`.** The hook rewrites `git status` to `rtk git status` with no path; unresolvable, every rewritten command dies with exit 127 and the agent retries blind — a failure that looks like agent confusion. orq-lite verifies this before installing the hook and skips the filter rather than breaking every shell call.

Turn either off in `team.json` (omitting the block enables both):

```json
{
  "runtime": {
    "context_optimization": {
      "compression_proxy": { "enabled": false },
      "command_filter":    { "enabled": false }
    }
  }
}
```

`url` and `binary` in the same blocks point at a pinned or vendored install.
