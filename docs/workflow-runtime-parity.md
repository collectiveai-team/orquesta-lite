# Workflow runtime parity matrix

This matrix is the deletion gate for the legacy runtime. A row may be marked
`v2` only when the referenced scenario test passes against the durable runtime.

| Capability | Legacy source | Scenario | V2 status |
| --- | --- | --- | --- |
| Agent contract fallback | `internal/invoke` | `invalid_contract_fallback` | core green (`TestRawInvalidContractFallsBack`, `TestRawAllInvalidReturnsStableContractError`) |
| Bounded fix retry | `internal/loops/fix.go` | `bounded_retry` | core green (`TestSchedulerEnforcesRunAttemptBudget`) |
| Lint/test/critic gates | `internal/loops/fix.go` | `quality_gate_failure` | pending |
| Full-suite before commit | `internal/loops/task.go` | `full_suite_gate` | pending |
| Rollback failed task | `internal/loops/task.go` | `rollback_on_failure` | pending |
| Task decomposition depth | `internal/loops/task.go` | `decomposition_limit` | pending |
| Human handoff | `internal/handoff` | `needs_human` | core green (`TestSchedulerUnsafeActivityRequiresApproval`) |
| Review-cycle stop | `internal/loops/review.go` | `review_stop` | pending |
| Factory plan reuse | `internal/factory` | `resume_reuses_plan` | pending |
| Repeated-failure stop | `internal/factory` | `repeated_failure` | pending |
| Residue checkpoint | `internal/factory` | `residue_checkpoint` | pending |
| Merge conflict pause | `internal/factory` | `merge_conflict` | pending |
| Budget exhaustion | `internal/factory` | `budget_exhausted` | core green; pack scenario pending |
| Interrupt and resume | `internal/factory` | `interrupt_resume` | core green (`TestSchedulerRunsAndResumesWithoutRepeatingSucceededStep`) |
| Publish reconciliation | `internal/commands/factorycmd.go` | `publish_uncertain` | core green (`TestResumeReconcilesBeforeRepeating`, chaos suite); pack scenario pending |

Rules:

- Legacy and v2 scenarios run on separate temporary repositories or pure fakes.
- A capability is removed only after its v2 scenario and crash boundaries pass.
- A discarded capability requires an ADR update explaining why it is no longer
  part of the product contract.

## Current deletion gate

The core runtime gate is implemented, but deletion is intentionally **not
open**. The development-specific rows still require the external development
pack, three comparable benchmark runs, a dogfooded canary release, tested
rollback, and confirmation that controlled projects have no active legacy run.
Until those external facts exist, `internal/loops`, `internal/factory`, and
`internal/engine` remain frozen compatibility code.
