# ADR-0005: Durable dynamic workflow runtime

Date: 2026-07-14
Status: Accepted; cutover completed 2026-08-01

## Context

Orquestalite previously had specialized Go schedulers and a separate in-memory flow interpreter. Adding features to both produced divergent semantics for validation, recovery, resume, quality gates, and observability.

## Decision

Orquestalite has one runtime with four layers:

1. `internal/flow`: strict versioned flow/subflow documents and immutable compiled IR.
2. `internal/workflow`: durable scheduling, retry, resume, budgets, approvals, and transactional event outbox.
3. `internal/activity`: typed bounded effects with explicit recovery semantics.
4. Packs: domain flows, subflows, policies, prompts, schemas, and optional activity manifests.

High-level operations are subflows, not opaque Go activities. Activities do not schedule work or own retry loops.

Operational state is stored in `.orquestalite/workflows.db`. Runs pin compiled IR, policy, resources, and pack digests.

The CLI's development commands are aliases into the installed `development` pack. No engine flag or compatibility routing remains.

## Consequences

- Flows, roles, prompts, policies, and domain loops can evolve as verified data.
- Resume behavior is explicit for successful, failed, and uncertain effects.
- The core scheduler has no knowledge of tickets, coder/tester roles, factory queues, branches, or PRs.
- There is one validation and recovery model to test and operate.
- Old `tasks.json`, `factory.json`, and `flows.json` state is not converted or read.

## Guardrails

- A run pins compiled IR, policy, pack digests, and activity versions.
- Shell permission and effect capabilities come from runtime policy, never untrusted flow input.
- Loops and retries are structurally bounded.
- External activities must describe the same contract as their pinned manifest.
- Packs fail closed on missing, changed, unlisted, or symlinked resources.

The original implementation plan was removed after cutover; the current architecture is documented in `docs/ARCHITECTURE.md`.
