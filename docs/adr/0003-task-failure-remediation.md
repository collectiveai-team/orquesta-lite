# ADR-0003: Task failure remediation cascade

Date: 2026-05-30
Status: Accepted

## Context

Before this change, the only failure-mode the orchestrator handled gracefully was
agent **rate limiting** (cooldown + fallback to next agent in the role's chain).
Every other failure — codex sandbox blocking writes, an agent timing out, a
malformed result file, a model that simply could not produce the contract, a
task description too ambiguous for any model — all collapsed into the same
opaque outcome: `failed`, with a single `failure_reason` string and no actionable
information for either the human or the reviewer agent.

A debugging session in May 2026 traced three distinct codex failures over five
iterations before identifying that the codex CLI was running with
`sandbox: read-only`, making the result-file write structurally impossible. The
orchestrator hid codex's stderr (which said exactly that) behind a generic
`"did not write result file"` message. The fallback chain never fired because
fallback was gated on rate-limit only. The user had no canonical way to recover
from a class of bugs that the orchestrator was *almost* designed to catch.

## Decision

We introduce a **four-stage remediation cascade** that the orchestrator walks
when a task is not resolving on its own:

```
┌────────────────────────────────────────────────────────────────────────┐
│ 1. Agent fallback (Phase 2)                                            │
│    - Any non-success outcome — RateLimited, ResultMissing, AgentCrashed│
│      InvalidContract — advances the chain to the next configured agent.│
│    - Cooldown applies only for rate-limit.                             │
│    - Cap at MaxAttempts (default 2*len(chain)).                        │
├────────────────────────────────────────────────────────────────────────┤
│ 2. Escalation ladder (Phase 4a)                                        │
│    - When the fix loop detects 2 consecutive identical failure hashes  │
│      (down from 3), invoke the next agent in roles[role].escalation_   │
│      ladder for one more attempt instead of marking failed.            │
│    - Ladder exhaustion falls through to step 3.                        │
├────────────────────────────────────────────────────────────────────────┤
│ 3. Auto-decomposition (Phase 4b)                                       │
│    - On agent_repeated_failure, invoke the parser-decompose prompt to  │
│      break the task into 2-5 subtasks.                                 │
│    - Original task marked status:"decomposed", DecomposedIntoIDs       │
│      records the new IDs. Subtasks are appended and may run in the    │
│      same cycle.                                                       │
├────────────────────────────────────────────────────────────────────────┤
│ 4. Handoff to human (Phase 4c)                                         │
│    - When step 3 is unavailable (no DecomposePrompt configured) or     │
│      returns 0/>5 subtasks, write tasks/handoff-<task_id>.md with the  │
│      full attempt history, hypothesis bools (ConfigSuspect /           │
│      ModelSuspect / TaskSuspect), and suggested next steps.            │
│    - Task marked status:"needs_human", FailureDetails populated.       │
│    - Run continues with remaining pending tasks.                       │
└────────────────────────────────────────────────────────────────────────┘
```

Two transverse changes support the cascade:

- **Phase 1 (visibility):** the `agent_run` event now carries
  `stderr_tail` (2 KB), `stdout_tail` (2 KB), `exit_code`, provider/model
  metadata, session/final-text summaries when streamed by a provider, `cmd_line`
  for legacy commands (with `{{PROMPT}}` elided), and a parsed `codex_header`
  when present. The
  "did not write" error includes the stderr tail and exit code directly. This
  is the single change with the highest leverage — it would have ended the
  May 2026 debugging session in one iteration.
- **Phase 3 (structural contract):** schemas are embedded in the binary via
  `go:embed` and materialised by `orq-lite init` alongside `team.json` and
  `prompts/`. Legacy Codex `cmd` agents can enforce the contract with
  `-o {{RESULT_PATH}} --output-schema schemas/{{ROLE}}.json`; provider agents
  keep the result JSON file as the role contract and use streamed JSONL for
  observability.

**Phase 5 (preflight)** is an OFF-by-default validator. It is not part of the
cascade — it catches obviously-malformed tasks before the first fix loop
iteration. The marker status is `needs_clarification`, distinct from
`needs_human` (which means orq-lite tried hard and gave up).

## New states and statuses

`tasks.Task.Status` now ranges over:
`pending | in_progress | done | failed | decomposed | needs_human | needs_clarification`.

| Status | Meaning | Set when |
|---|---|---|
| `decomposed` | Replaced by a set of subtasks in the same plan | Auto-decomposition succeeded |
| `needs_human` | orq-lite exhausted automated remediation | Handoff written, run continues |
| `needs_clarification` | Task is malformed; preflight refused to run it | Preflight enabled and failed |

`failed` is now reserved for terminal failures that are not the cascade's
domain: `commit_rejected`, `full_suite_failed`, `max_iterations` without
escalation, and `rate_limit_exhausted`.

## `FailureDetails`

For `needs_human` tasks, `Task.FailureDetails` carries:

- `AgentChain []AgentRun` — each attempt's agent name, duration, status, stderr tail.
- `ConfigSuspect`, `ModelSuspect`, `TaskSuspect bool` — hypothesis flags filled by the
  orchestrator (and read by the reviewer agent) to decide remediation. The
  current implementation sets `TaskSuspect=true` whenever the cascade exhausts;
  the other two are populated to `false` initially. Future work: a heuristic at
  the runcmd layer that inspects `codex_header.sandbox` and recent stderr to
  set `ConfigSuspect=true` when the failure pattern matches the May 2026 class.
- `LastStderrTail`, `HandoffPath` — pointers for the human to start from.

## Consequences

**Positive:**

- **Visibility-first.** The most common debugging path — "what did the agent
  actually do?" — is one `cat .orquestalite/run.log | jq` away. Phase 1 alone
  closes the worst-of-class UX bug we hit.
- **Structural codex compliance.** codex agents can no longer fail to write the
  contract; the CLI guarantees it via `-o`. The "model hit its output cap and
  forgot the JSON" failure mode is removed.
- **No silent dead-ends.** Tasks that can't be auto-resolved get a markdown
  handoff document the human can act on without reading the JSONL log.
- **Composable remediation.** Each step is independently testable and
  configurable. A team that only wants steps 1+4 can leave `escalation_ladder`
  empty and not set `decompose_prompt`.
- **Backwards compatible.** Existing `team.json`s that don't set
  `escalation_ladder`, `decompose_prompt`, or `preflight_enabled` behave as
  before — but with the visibility and fallback improvements active.

**Negative:**

- **More config surface.** Three new optional fields on roles
  (`escalation_ladder`, `decompose_prompt`), one new optional flag on limits
  (`preflight_enabled`), and provider fields such as `provider`, `model`, and
  `effort`. The `init` template covers the sensible defaults so most users
  never see this surface.
- **More state transitions.** Three new statuses; tooling that walks
  `tasks.json` (the future `status --watch`) must handle them.
- **Decomposition can over-split.** A bad parser-decompose prompt could produce
  subtasks that themselves fail, leading to many handoff files for what was
  originally one task. The hard cap (1–5 subtasks per decomposition, no
  recursion into already-decomposed paths) bounds the blast radius but does not
  eliminate it.
- **Hypothesis bools are guesses, not measurements.** `ConfigSuspect=true` is
  a hint, not a diagnosis. The reviewer agent and the human are expected to
  verify.

## Alternatives considered

**A. Stop at Phase 2 (fallback only).** Tempting because the visibility and
fallback fixes together cover ~90% of the failures we observed. We rejected
this because the remaining 10% — genuinely-hard or under-specified tasks —
were the ones generating the most operator frustration in long AFK runs. The
escalation + decomposition + handoff cascade exists for those.

**B. Single `failed` status, richer `failure_reason` enum.** Considered
keeping the status enum unchanged and just expanding `failure_reason` to
include `needs_human`, `decomposed`, etc. Rejected because status and reason
are conceptually distinct: a `decomposed` task is not "failed" — it was
replaced. Conflating them would make `NextPending()`'s contract ambiguous
("is a `failed` task with `reason="decomposed"` selectable?").

**C. Synchronous human-in-the-loop on every failure.** Pause the run and
surface a TUI prompt asking the operator to triage. Rejected because the
project's value proposition is AFK execution. Handoff files give the same
information without blocking the run.

**D. Automatic task description rewrite by the reviewer.** When the
hypothesis is `TaskSuspect=true`, have the reviewer rewrite the description in
place and retry. Rejected for v1 because it conflates the reviewer's
"summarise the cycle" role with a per-task rewrite responsibility, and because
auto-decomposition already covers the most common case (a task that needed to
be N tasks).

## Migration notes

- Existing projects' `team.json` works unchanged. Users who want the new
  remediation features add `escalation_ladder` to roles and `decompose_prompt`
  to the parser role. `orq-lite init` writes the recommended defaults.
- Existing `tasks.json` files work unchanged. The new fields
  (`decomposed_into_ids`, `failure_details`) are `omitempty`.
- The `agent_run` event grew new fields. Consumers that parse the log file
  should treat unknown fields as ignorable (the JSONL format already implies
  this).

## Related

- ADR-0001 (CLI subprocess orchestration) — establishes that we cede output
  control to CLIs; this ADR is the response to one consequence of that.
- ADR-0002 (JSON result contracts at known paths) — the contract is what
  step 3 of the cascade re-tries; provider streams improve diagnostics while
  the result file remains the source of truth.
