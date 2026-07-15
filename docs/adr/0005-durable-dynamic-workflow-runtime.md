# ADR-0005: Durable dynamic workflow runtime

Date: 2026-07-14
Status: Accepted

## Context

orq-lite currently has two execution models. `internal/loops` and
`internal/factory` implement the production development lifecycle with durable
task/factory files and domain-specific recovery. `internal/engine` interprets
configuration-driven flows, but stores context in memory and does not provide
equivalent validation, idempotency, recovery, or resume semantics.

Extending the generic engine with native actions that invoke the specialized
loops would preserve two schedulers while hiding one behind an opaque action.
It would also prevent users from changing the implementation and verification
strategy declaratively.

## Decision

We will converge on one generic workflow runtime with four layers:

1. `internal/flow`: versioned flow/subflow documents and an immutable compiled IR.
2. `internal/workflow`: durable state machine, scheduling, retry, resume, budgets,
   approvals, and event outbox.
3. `internal/activity`: typed, bounded effects with schemas, idempotency mode,
   reconciliation, and optional compensation.
4. External packs: domain workflows, policies, prompts, schemas, and activities.

Flow v2 definitions are runtime data. High-level operations such as
`implement-ticket` are subflows, not Go activities. Activities do not schedule
other work or own retry loops.

Operational workflow state is stored in `.orquestalite/workflows.db`.
`.orquestalite/orq.db` remains a rebuildable read-model of `run.log`; it is not
used for checkpoints. State transitions and their events are committed through
a transactional outbox.

The current engine, loops, and factory packages are frozen for compatibility.
Commands migrate to versioned flow aliases one at a time. Unfinished legacy
state is drained by the legacy runtime rather than converted automatically.

## Consequences

Positive:

- Flows, subflows, roles, and policies can change without recompiling orq-lite.
- Resume has explicit semantics for successful, failed, and uncertain effects.
- The core no longer knows coder/tester/critic, tasks, factory branches, or PRs.
- Orquesta can operate the same runtime through stable run/step/attempt models.

Negative:

- Two runtimes coexist during migration.
- External effects require activity-specific reconciliation to be safely retried.
- A development pack must reach parity before specialized packages can be removed.

## Guardrails

- No new features are added to `internal/engine`, `internal/loops`, or
  `internal/factory` except critical compatibility fixes.
- `command.run` is at-most-once by default; an uncertain outcome is not retried.
- A run pins compiled IR, policy, pack digests, and activity versions.
- Parallel execution is deferred until sequential kill-and-resume tests pass.

## Implementation

See `docs/superpowers/plans/2026-07-14-durable-dynamic-workflow-runtime.md`.
