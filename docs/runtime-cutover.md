# Durable runtime cutover

The compatibility runtime is deleted only from a commit where the cutover gate
is open. The gate is fail-closed: it consumes observed release evidence and
also verifies mutable local state (controlled projects and the installed pack)
at check time. It never turns test execution into self-attestation.

## Evidence workflow

Generate the schema-shaped template:

```bash
orq-lite cutover template > .orquestalite/cutover-evidence.json
```

Release automation fills the template with:

- every scenario from `internal/legacytest` with its legacy baseline, passing
  v2 commit, and result;
- at least three unique benchmark runs with the same model and config digest,
  all passing with zero critical regressions;
- the required crash-boundary scenarios;
- a completed canary that used v2 as the implicit default;
- a completed rollback from that canary;
- every controlled project that must have no unfinished `tasks.json` or
  `factory.json` state;
- the exact installed `development` pack root, version, and manifest digest.

Paths in the evidence document are absolute or relative to the evidence file.
Timestamps are RFC3339. Unknown fields, duplicate/missing parity cases, invalid
timestamps, changed pack files, unlisted pack resources, and unreadable legacy
state all close the gate.

Run the human or machine-readable check:

```bash
orq-lite cutover check --evidence .orquestalite/cutover-evidence.json --commit <deletion-candidate-sha>
orq-lite cutover check --evidence .orquestalite/cutover-evidence.json --commit <sha> --json
```

The command exits non-zero unless all seven gates pass:

1. parity;
2. comparable benchmarks;
3. chaos boundaries;
4. v2-default canary;
5. rollback;
6. no active legacy state in controlled projects;
7. an immutable, offline-resolvable development pack containing
   `plan-tickets`, `issue-fix`, `pr-review`, `task-list`, `factory-fast`, and
   `factory-governed`.

`runtimeCommit`, every parity/chaos/benchmark result, and the canary must match
the `--commit` deletion candidate. This prevents valid but stale evidence from
opening the gate for newer code.

## Canary default

Normal builds keep `legacy` as the implicit default until the gate opens. A
canary binary selects v2 without maintaining a source fork:

```bash
go build -ldflags "-X main.defaultEngine=v2" ./cmd/orq-lite
```

During coexistence, an implicit v2 default routes unfinished legacy task or
factory state back to the legacy engine with a warning. An explicit
`--engine=v2` fails closed unless the command supports and receives
`--force-new-run`. No compatibility file is deleted or converted.

## Deletion procedure

Only after `cutover check` exits zero may a cleanup change delete
`internal/loops`, `internal/factory`, `internal/tasks`, `internal/results`,
`internal/handoff`, `internal/preflight`, `internal/engine`, the specialized
commands/assets/config, and finally this temporary cutover package. The output
of the successful check belongs in the release artifacts so the deletion is
auditable after the migration tooling is gone.
