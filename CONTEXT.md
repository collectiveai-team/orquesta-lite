# Product context

## Product

Orquestalite is a single-binary, local-first orchestrator for durable coding-agent workflows. The unit of orchestration is a versioned v2 flow compiled from data. Flows compose activities and subflows, and are distributed with schemas, policies, and prompts in verified packs.

There is no compatibility runtime. Public development commands are aliases into the built-in `development` pack.

## Product objective

Given a product objective and a repository, repeatedly produce small verified changes until the objective is satisfied or a bounded policy requires human intervention. Success means more than matching ticket text: the integrated product must work, remain maintainable, respect repository conventions, and preserve declared invariants.

## Core runtime properties

- Strict compilation: unknown fields, unresolved resources, invalid references, and type mismatches fail before execution.
- Reproducibility: compiled IR, resource digests, policy, inputs, and pack snapshot are pinned into the run.
- Durability: runs, steps, attempts, outputs, approvals, and outbox events persist in SQLite.
- Safe recovery: resume uses pinned definitions; activity effect modes control retry and reconciliation.
- Bounded execution: foreach concurrency, retry budgets, while-loop iteration budgets, and policy ceilings are explicit.
- Idempotent integration: external triggers can claim a stable source key exactly once.
- Observable evidence: agent prompts/results/artifacts and workflow events are retained under `.orquestalite`.

## Development pack

The built-in `development@5` pack provides:

- `plan-tickets@1`
- `task-list@1`
- `factory-fast@1`
- `factory-governed@2`
- `review-existing@1`
- `pr-review@1`
- `issue-fix@1`

Its subflows separate ticket implementation from integrated governance. Ticket work uses a planner, coder, and ticket QA. Integrated governance uses QA, adversary, critic, integrator, and governance reviewer roles. Findings become a new bounded workflow state and may create another iteration.

## Role contracts

- `ticket_planner`: turns the current objective and evidence into small executable tickets with acceptance criteria and budgets.
- `coder` / `batch_coder`: implements only the assigned scope, following repository conventions and preserving unrelated changes.
- `ticket_qa`: checks a ticket's acceptance criteria and produces actionable evidence.
- `qa`: tests the integrated behavior. For web projects this role is expected to use an available browser-testing skill or tool, not rely only on code inspection.
- `adversary`: searches for ways the product can violate the higher-level objective, invariants, security expectations, and real user workflows even when literal specs pass.
- `critic`: reviews code structure, correctness risks, maintainability, and local style.
- `integrator`: converts accepted findings into coherent changes without duplicating or contradicting prior work.
- `gov_reviewer`: decides whether evidence justifies another loop, completion, or human intervention.

The objective must be present in the workflow input/evidence throughout the loop. Specs and tickets are projections of that objective, not substitutes for it.

## Configuration

`team.json` declares agents, dynamic roles, session behavior, provider backoff, runtime limits, conventions, and argv quality gates. A run resolves only roles referenced by its compiled IR. Project commands are provided as argv arrays through the read-only `config` namespace.

## State

```text
.orquestalite/
  packs/<name>/<version>/
  workflows.db
  run.log
  runs/<run-id>/
  results/
  sessions.json
  watch.json
```

No execution state is stored in `tasks.json`, `factory.json`, or `flows.json`.

## Non-goals

- A second in-memory flow interpreter.
- Hardcoded domain schedulers beside the durable runtime.
- Implicit engine selection or migration guards in normal execution.
- Unbounded autonomous execution.
- Shell commands supplied by untrusted workflow inputs.
