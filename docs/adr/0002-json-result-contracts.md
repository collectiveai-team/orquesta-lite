# ADR-0002: Inter-agent results via JSON files at known paths

Date: 2026-05-03
Status: Accepted

## Context

The orchestrator needs to know, after each agent invocation, what the agent
decided. The fix loop branches on `tester.status == "pass"` and
`critic.status == "approved"`. The outer review loop branches on
`reviewer.should_stop`. Without a reliable signal, the loops cannot close.

Three mechanisms were considered:

1. **Stdout markers** (the original Ralph approach): the agent prints a
   canonical phrase such as `TASK COMPLETE` or `TESTS PASS`; the orchestrator
   greps stdout for the phrase.
2. **Exit codes**: the agent exits 0 on success, non-zero on failure; the
   orchestrator checks the return code.
3. **JSON files at known paths**: the agent's last action is to write a
   structured JSON file at `.orquestalite/results/<role>.json`; the orchestrator
   parses it.

## Decision

We use **JSON result files at known paths (mechanism 3)**. Each role declares
its `result_path` in `team.json`. Each role's prompt template includes the
explicit JSON schema the agent must produce, with the instruction that
writing the file is the agent's last action.

The orchestrator validates the JSON shape, treats missing/malformed files as
an agent failure, and uses the parsed structure both for control flow and
for injecting feedback into downstream agents' prompts.

## Consequences

**Positive:**

- Deterministic parsing. `json.Unmarshal` either succeeds or returns an error;
  no regex on free-form prose.
- Rich, structured feedback flows between agents. The tester's `failures`
  array (with `test`, `message`, `hint` per entry) can be embedded literally
  in the next coder prompt without lossy summarisation.
- One source of truth per agent invocation. The same file the orchestrator
  reads is the file we capture in the JSONL log's `result_snapshot` for
  post-mortem.
- Agents are decoupled from each other: only the orchestrator knows where
  result files live. Switching to a different inter-agent transport later
  (sockets, message queue) does not require updating prompts.

**Negative:**

- Every prompt template grows by a non-trivial schema block. Prompts must
  be explicit and verbose about the output contract.
- We must handle the failure mode "agent produced output but did not write
  the file" or "wrote invalid JSON". This adds a re-invocation path
  (covered in CONTEXT.md under failure handling: one corrective re-invoke,
  then mark the run as `fail`).
- We pay one extra file write per agent invocation. Negligible.

## Alternatives considered

**Stdout markers** (mechanism 1) are the original Ralph approach and are
maximally minimalist — no JSON schemas, no file IO. We rejected them
because LLMs paraphrase. "All tests passed!" instead of the prescribed
`TESTS PASS` silently breaks the grep, and the failure is hard to detect:
the orchestrator just sees a non-match and assumes failure, while the
agent believes it succeeded. The asymmetry is exactly the kind of bug
that would only manifest in long AFK runs and waste hours.

**Exit codes** (mechanism 2) are simple but inadequate: CLIs like
`claude -p` and `codex exec` exit 0 unless they crash. They do not
distinguish "model said the task is done" from "model gave up". Coercing
the agent to invoke `exit N` requires giving it shell tool-use, which
defeats the simplicity argument.

A **hybrid** of exit codes for stop/go signals plus JSON files for rich
feedback was considered, but ADR-0001's CLI-subprocess decision means the
exit code is unreliable in practice. JSON-only keeps the design coherent.
