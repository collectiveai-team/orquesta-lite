# Architecture

Orquestalite has one execution architecture.

```text
CLI aliases / flow CLI / watch / HTTP catalog
                    |
                    v
        pack resolver + v2 compiler
                    |
            pinned compiled IR
                    |
                    v
       durable workflow scheduler
          |         |         |
     activities   SQLite    event outbox
          |
  command / gate / agent / artifact / approval
```

## Compile path

`internal/flow` loads strict `orq.dev/v2` documents, resolves versioned resource references, validates schemas and expressions, compiles subflows recursively, and pins resource and pack digests into immutable IR.

Installed references use `pack[@pack-version]/flow@flow-version`. Local development may validate a JSON path or `flow:name@version` against a local catalog.

## Runtime path

`internal/workflow` owns durable scheduling. SQLite records the run, every materialized step instance, attempts, outputs, approvals, source-key claims, and outbox events. The scheduler implements bounded `foreach` and `while`, retries, handlers, compensation, cancellation, and recovery according to activity effect mode.

`flow resume` loads the stored IR. It verifies the pinned pack snapshot when external pack resources are needed; it never silently recompiles a newer definition.

## Activities

`internal/activity` defines versioned activity specs and executors. Built-ins are:

- `agent.invoke`
- `command.run`
- `gate.run`
- `gate.assert`
- `artifact.*`
- `approval.wait`

External process activities use a pinned manifest and a describe handshake before registration. Runtime policy, not flow input, grants shell/effect capabilities.

## Agent invocation

`internal/invoke`, `fallback`, `providers`, `runner`, and `sessions` form the agent execution adapter. A compiled flow identifies a role; `team.json` resolves that role to ordered agents and escalation agents. Invocation supports rate-limit classification, bounded provider fallback, health skips, session resume, prompt conventions, output-schema validation, cost metadata, and immutable artifacts.

## Packs

`internal/flow/pack.go` validates pack manifests and SHA-256 files. `pack install` stages and verifies bytes before an atomic rename to `.orquestalite/packs/<name>/<version>`.

The canonical built-in pack lives in `examples/governed-pack/pack` and is embedded into release binaries by `examples/governed-pack/embed.go`. `init` materializes that exact verified pack.

## Interfaces

- `cmd/orq-lite`: one CLI surface; development aliases call `RunDevelopmentAlias`, which calls `FlowCLI`.
- `internal/watch`: provider polling and durable idempotent v2 triggers.
- `internal/web`: read-only durable workflow, flow catalog, doctor, and event APIs.
- `internal/doctor`: validates the built-in pack, dynamic team roles, prompt files, provider binaries/credentials, and argv gates.

## State ownership

| State | Owner |
|---|---|
| `.orquestalite/workflows.db` | durable workflow store |
| `.orquestalite/packs/` | pack installer / init |
| `.orquestalite/runs/<id>/` | activity and agent artifact stores |
| `.orquestalite/run.log` | event logger + workflow outbox sink |
| `.orquestalite/results/` | latest role outputs and immutable result archives |
| `.orquestalite/sessions.json` | provider session store |
| `.orquestalite/watch.json` | watch cursor and deduplication state |

The removed engines have no state compatibility layer in the runtime. Old `tasks.json`, `factory.json`, and `flows.json` files are ignored.
