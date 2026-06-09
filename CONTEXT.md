# orquestalite

Minimalist **Go** orchestrator for the **Ralph** technique: a single binary that
drives multiple CLI-based AI agents in nested loops to autonomously implement a
plan.

> The CLI command is `orq-lite`; the project/runtime name is `orquestalite`.

## Stack

- **Go** (modules, single binary output `orq-lite`).
- Standard library first; only add deps when needed (likely candidates: a JSON
  schema validator and a TUI/colour library for `status --watch`).
- Subprocess execution via `os/exec`. JSON via `encoding/json`. Logging via
  `log/slog` to JSONL.

## Glossary

- **plan** — raw user input describing what to build. Free-form (markdown / text).
  Lives outside the orchestrator's structured state.
- **task** — atomic unit of work extracted from a plan. Sized for one focused
  change. Lives in `.orquestalite/tasks.json`.
- **role** — function performed in the team: `parser`, `coder`, `tester`,
  `critic`, `reviewer`. Roles are abstract; agents are concrete.
- **agent** — concrete binding of a role to a CLI tool + model
  (e.g. `coder → claude --model sonnet`). Defined in `team.json`.
- **review loop** — outermost loop. Runs `N` review cycles. After each cycle,
  the `reviewer` role inspects the work done and may append new tasks.
- **task loop** — middle loop. Iterates over pending tasks in priority order
  until none remain pending in the current review cycle.
- **fix loop** — innermost loop. For one task, runs `coder → tester → critic`
  repeatedly until both tester and critic approve, or `max_iterations` is hit.
- **result contract** — the JSON shape an agent must write to a known path on
  exit. The orchestrator reads it to make control-flow decisions.
- **result archive** — the retained, per-task history of every agent's result
  contract. Distinct from the single latest result file the orchestrator reads
  for control flow: the archive is written for every parseable attempt and
  never overwritten, so the work of earlier tasks and fix-loop iterations is
  not lost.
- **review rubric** — the code-quality criteria the reviewer applies to a
  cycle's changes (structural simplification, file-size smell, branching
  complexity, layer discipline). Findings become new tasks; a structural
  regression prevents the reviewer from stopping the run.

## Architecture decisions (locked)

- **Pure CLI subprocess orchestration.** Every agent is a CLI process invoked
  with a prompt as argument. The orchestrator does not implement tool-use,
  file editing, or test execution — it delegates to the underlying CLIs.
- **JSON result contracts at known paths.** Each agent's last action is to
  write `.orquestalite/results/<role>.json`. The orchestrator parses this file
  to determine outcome. Stdout and exit codes are not used as control signals
  (CLIs like `claude -p` always exit 0 absent crashes).
- **Atomic task granularity.** The `parser` role splits the plan into small,
  individually testable tasks. Large/coarse tasks degrade model output quality.

## Task schema

`.orquestalite/tasks.json`:

```json
{
  "tasks": [
    {
      "id": "T001",
      "title": "short title",
      "description": "what to do + acceptance criteria",
      "status": "pending | in_progress | done | failed",
      "priority": 1,
      "created_in_review_cycle": 0,
      "attempts": 0,
      "last_feedback": null
    }
  ]
}
```

"Are there pending tasks?" → `any(t.status == "pending" for t in tasks)`.

## team.json

Two-layer structure: a global **agent pool** declares provider-based agents or
legacy CLI invocations, and a **roles** map binds each role to an ordered list
of agents (primary first, fallbacks after) plus role-specific fields (prompt,
result path, timeout).

```json
{
  "agents": {
    "claude_sonnet": {
      "provider": "claude",
      "model": "claude-sonnet-4-6",
      "dangerously_skip_permissions": true,
      "rate_limit_pattern": "rate_?limit|429|quota|exceeded.*tokens"
    },
    "claude_opus": {
      "provider": "claude",
      "model": "claude-opus-4-7",
      "dangerously_skip_permissions": true
    },
    "codex_gpt5": {
      "provider": "codex",
      "model": "gpt-5",
      "effort": "medium"
    }
  },
  "roles": {
    "parser":   { "agents": ["claude_opus"],                                 "prompt": "prompts/parser.md",   "result_path": ".orquestalite/results/parser.json",   "timeout_seconds": 300 },
    "coder":    { "agents": ["claude_sonnet", "codex_gpt5", "claude_opus"],  "prompt": "prompts/coder.md",    "result_path": ".orquestalite/results/coder.json",    "timeout_seconds": 900 },
    "tester":   { "agents": ["claude_sonnet", "codex_gpt5"],                 "prompt": "prompts/tester.md",   "result_path": ".orquestalite/results/tester.json",   "timeout_seconds": 600 },
    "critic":   { "agents": ["claude_opus", "claude_sonnet"],                "prompt": "prompts/critic.md",   "result_path": ".orquestalite/results/critic.json",   "timeout_seconds": 300 },
    "reviewer": { "agents": ["claude_opus"],                                 "prompt": "prompts/reviewer.md", "result_path": ".orquestalite/results/reviewer.json", "timeout_seconds": 600 }
  },
  "limits": {
    "max_review_cycles": 3,
    "max_fix_iterations": 5
  },
  "rate_limit_backoff": {
    "initial_seconds": 30,
    "factor": 2,
    "max_seconds": 1800,
    "default_pattern": "rate_?limit|429|quota|too many requests"
  },
  "full_test_command": "uv run pytest"
}
```

### Fallback + backoff algorithm

For a role invocation, the orchestrator iterates `roles[role].agents` in order:

1. Skip agents whose `cooldown_until` is in the future.
2. Run the next available agent. Capture stdout+stderr.
3. Match output against the agent's `rate_limit_pattern` (or the global default).
   On match → set `cooldown_until = now + current_backoff`; advance to next agent.
4. If the run produces a valid `result.json`, return it.
5. If all agents are exhausted (cooldown or rate-limited) → sleep
   `current_backoff` (wall clock) → `current_backoff *= factor` → retry from step 1.
6. If `current_backoff > max_seconds` → mark task `failed` with
   `failure_reason: "rate_limit_exhausted"`.

Cooldowns live only in process memory — not persisted. Restarts assume
agents are fresh.

### `failure_reason` enum

When a task transitions to `failed`, the orchestrator records *why*:

- `"max_iterations"` — fix loop exhausted without convergence
- `"agent_repeated_failure"` — tester `failures` hash unchanged across iterations
- `"rate_limit_exhausted"` — all agents in role rate-limited past `max_seconds`
- `"commit_rejected"` — pre-commit hook rejected after one retry
- `"full_suite_failed"` — task-scoped tests passed but full suite broke
- `"agent_crashed"` — subprocess crashed and re-invocation also failed
- `"invalid_result_json"` — agent failed to write valid contract twice

The reviewer reads this field to decide remediation (propose unblock task,
abandon, retry on next cycle, etc.).

Prompts live in separate markdown files under `prompts/`. Variable
interpolation uses `{{VAR}}` markers (not f-strings or Jinja) to avoid
collisions with the curly braces commonly present in prompt content
(JSON schema examples, code samples).

Standard interpolation variables passed by the orchestrator:

- `{{TASK_TITLE}}`, `{{TASK_DESCRIPTION}}` — current task
- `{{ATTEMPT_NUMBER}}` — fix loop iteration count
- `{{TESTER_FEEDBACK}}`, `{{CRITIC_FEEDBACK}}` — populated on iteration > 1
- `{{REVIEW_CYCLE}}` — outer loop counter

## State between iterations

- **Single working directory.** All agents operate on the user's repo at `.`.
  No worktrees, no branches per task. Tasks are processed sequentially.
- **One commit per successful task.** The fix loop mutates files in place
  across iterations. When `tester == pass AND critic == approved`, the
  orchestrator commits with a message derived from the task title.
  Failed tasks: the orchestrator rolls the tree back to the state captured at
  the start of the task (`git reset --hard` to that commit, plus removal of the
  untracked files the agent created) before moving on, so the next task starts
  clean. Pre-existing untracked files the user already had (scratch notes,
  un-added WIP) are preserved — rollback only undoes the failed agent's work,
  never unrelated files. See `gitx.RollbackTo`.
- **Global memory file**, write-on-discretion: `.orquestalite/memory.md`.
  Each role's result schema includes an optional `notes_for_memory: string | null`
  field. The orchestrator appends non-null entries with metadata:

  ```
  ## [cycle 1, task T003, critic]
  Codebase uses snake_case in DB models but camelCase in API responses;
  conversion happens in the schema layer.
  ```

  The prompt for every role instructs: "Only write to `notes_for_memory`
  if you learned something non-obvious that future iterations need.
  Otherwise leave it null. Do not narrate progress." The whole memory.md
  is injected at the top of every agent prompt.

## Result contracts (per role)

Every agent's last action is to write a JSON file at `result_path`. The
orchestrator validates the shape and uses it to drive control flow.

### `parser.json`
```json
{
  "tasks": [
    { "id": "T001", "title": "...", "description": "...", "priority": 1 }
  ],
  "notes_for_memory": null
}
```
The orchestrator persists these to `.orquestalite/tasks.json` adding
`status: "pending"`, `attempts: 0`, `created_in_review_cycle`.

### `coder.json`
```json
{
  "status": "completed" | "blocked",
  "summary": "what was done / why blocked",
  "files_changed": ["src/foo.py"],
  "notes_for_memory": null
}
```

### `tester.json`
```json
{
  "status": "pass" | "fail",
  "command_run": "uv run pytest tests/test_foo.py",
  "failures": [
    { "test": "...", "message": "...", "hint": "..." }
  ],
  "notes_for_memory": null
}
```

### `critic.json`
```json
{
  "status": "approved" | "rejected",
  "concerns": [
    { "severity": "blocker" | "nit", "where": "src/foo.py:42",
      "issue": "...", "suggestion": "..." }
  ],
  "notes_for_memory": null
}
```

The critic **only vetoes and provides feedback for the coder**. It does not
generate new tasks. Issues outside the current task's scope are written to
`notes_for_memory` so the reviewer can see them at end-of-cycle.

### `reviewer.json`
```json
{
  "summary_of_cycle": "...",
  "new_tasks": [
    { "title": "...", "description": "...", "priority": 2 }
  ],
  "should_stop": true | false,
  "notes_for_memory": null
}
```

## Test scope

- **Inside the fix loop**: the coder declares which test files are relevant
  in its prompt instructions; the tester runs only those. Fast iteration.
- **Before commit**: the orchestrator runs the **full test suite**
  (`team.json.full_test_command`). If it fails, the task is marked `failed`
  and no commit is made. This catches regressions across tasks.

## Outer review loop stop conditions

The reviewer may set `should_stop: true` when it sees the plan is complete.
The orchestrator also enforces `max_review_cycles` as a hard ceiling. Either
condition stops the outer loop. The reviewer prompt receives:

- `tasks.json` with current statuses
- `git log <cycle_start>..HEAD --stat` of the cycle's commits
- `memory.md` accumulated so far

## CLI surface

```
orq-lite init                # scaffold team.json + prompts/ + .orquestalite/
orq-lite plan plan.md        # run parser, write tasks.json, do NOT start loops
orq-lite run                 # run loops over existing tasks.json
orq-lite run plan.md         # plan + run in one call (AFK mode)
orq-lite status              # print current tasks.json with statuses
orq-lite reset               # wipe .orquestalite/ state
```

- **Plan input is free-form markdown.** The parser role is responsible for
  splitting verbose human intent into atomic, testable tasks.
- **Resume is idempotent.** `orq-lite run` without args continues the
  existing `tasks.json`. `orq-lite run plan.md` with prior state asks for
  confirmation (or `--force`).
- **`orq-lite plan plan.md --append`** adds tasks to an existing backlog.

## Observability

- **Dual output**: human-readable lines on stdout + structured JSONL events
  appended to `.orquestalite/run.log`. The log file is the canonical history.
- **Result files are not versioned.** `.orquestalite/results/<role>.json` always
  holds the latest result. The full history of each agent invocation lives in
  `run.log` as `agent_run` events with a `result_snapshot` field containing the
  JSON the agent returned.
- **Log rotation** at 50MB → `.orquestalite/run-<timestamp>.log.gz`.

### Event types in `run.log`

```jsonl
{"ts":"...","event":"cycle_start","cycle":1}
{"ts":"...","event":"task_start","task_id":"T003","priority":2}
{"ts":"...","event":"agent_run","role":"coder","agent":"claude_sonnet","task_id":"T003","attempt":1,"duration_s":42,"status":"completed","result_snapshot":{...}}
{"ts":"...","event":"rate_limit","role":"coder","agent":"claude_sonnet","cooldown_until":"..."}
{"ts":"...","event":"task_done","task_id":"T003","commit_sha":"abc123"}
{"ts":"...","event":"task_failed","task_id":"T007","reason":"max_iterations"}
{"ts":"...","event":"cycle_end","cycle":1,"new_tasks_proposed":4,"reviewer_should_stop":false}
```

### `orq-lite status`

- One-shot: prints the tasks table (id, title, status, attempts, failure_reason)
  + a "currently:" line derived from the tail of `run.log`.
- `--watch` flag: refresh every second.

### Repo hygiene

- `.orquestalite/` is added to `.gitignore` by `orq-lite init`. The directory
  holds runtime state, never committed to the user's repo.
- `team.json` and `prompts/` are committed (they are project configuration).
- `plan.md` location is at the user's discretion; orq-lite accepts any path.

## Fix loop semantics

For each task, the orchestrator runs the fix loop:

1. **Sequential with short-circuit**: `coder → tester`. If `tester.status == "fail"`,
   skip `critic` and jump back to coder with the tester feedback. If tester
   passes, run `critic`.
2. **AND condition for completion**: the loop closes only when
   `tester.status == "pass" AND critic.status == "approved"`.
3. **On `max_iterations` reached**: mark the task as `failed`, persist
   `last_feedback`, and continue with the next task. The reviewer in the
   outer loop is responsible for deciding what to do with failed tasks
   (propose unblock subtasks, abandon, etc.).
4. **Feedback injection**: the orchestrator (not the coder) is responsible
   for reading `tester.json` / `critic.json` and embedding their feedback
   into the next coder prompt. Agents do not read each other's result files
   directly — only the orchestrator does. This keeps the file layout an
   internal detail of orq-lite.
