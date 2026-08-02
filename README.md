# orquestalite

`orq-lite` is a local, durable workflow runtime for coordinating coding agents. Workflow behavior is data: strict v2 flows, subflows, schemas, policies, and prompts are distributed together as verified packs.

There is one execution path. The old hardcoded development loops and the in-memory `flows.json` interpreter are not part of the product.

## Quick start

```bash
go install github.com/lionelchamorro/orquestalite/cmd/orq-lite@latest

cd your-project
orq-lite init --lang auto
orq-lite doctor
orq-lite plan features.md
orq-lite status
```

`init` creates:

- `team.json`, with provider-backed agents, dynamic roles, and `lint_argv` / `test_argv` gates;
- `.orquestalite/packs/development/4`, the built-in digest-verified development pack;
- `.orquestalite/workflows.db` on the first run, for durable workflow state.

It does not create or read `flows.json`.

## Development commands

The familiar commands are aliases into the installed `development` pack:

| Command | Flow |
|---|---|
| `orq-lite plan <plan.md>` | `development/plan-tickets@1` |
| `orq-lite run [--fast]` | `development/task-list@1` |
| `orq-lite factory <features.md> [--fast] [--pr]` | `development/factory-governed@1` |
| `orq-lite review [--pr N]` | `development/pr-review@1` |
| `orq-lite intake --issue issue.md` | `development/issue-fix@1` |

These aliases do not select an engine and have no compatibility fallback.

## Packs and flow references

```bash
orq-lite pack install ./my-pack
orq-lite pack list
orq-lite flow list
orq-lite flow validate development/task-list@1
orq-lite flow inspect development/task-list@1
orq-lite flow run development/task-list@1 features_path="features.md" fast=false
```

Pack versions and flow versions are independent. `development/task-list@1` selects the newest installed development pack containing flow version 1. Pin both when reproducibility requires it:

```text
development@4/task-list@1
```

Pack installation verifies the manifest, every listed SHA-256 digest, unlisted files, and symlinks before an atomic install under `.orquestalite/packs/<name>/<version>`.

## Durable operations

```bash
orq-lite status [--watch]
orq-lite flow status <run-id>
orq-lite flow events <run-id>
orq-lite flow resume <run-id>
orq-lite flow cancel <run-id>
orq-lite flow approve <run-id> <approval-id> --decision approve
```

The runtime persists runs, step instances, attempts, outputs, approvals, and outbox events in `.orquestalite/workflows.db`. A resume uses the pinned compiled IR and pack digest stored with the run, not whatever happens to be installed later.

## Team configuration

`team.json` separates agents from roles. A flow may invoke any declared role; only roles referenced by its compiled IR are resolved.

```json
{
  "agents": {
    "codex": {"provider": "codex", "model": "gpt-5.5", "effort": "medium"}
  },
  "roles": {
    "coder": {
      "agents": ["codex"],
      "prompt": ".orquestalite/packs/development/4/prompts/coder.md",
      "result_path": ".orquestalite/results/coder.json",
      "timeout_seconds": 1800
    }
  },
  "limits": {"resume_sessions": true},
  "rate_limit_backoff": {"initial_seconds": 30, "factor": 2, "max_seconds": 1800},
  "lint_argv": ["go", "vet", "./..."],
  "test_argv": ["go", "test", "./..."]
}
```

The governed pack obtains project-specific quality gates only through `config.lint_argv` and `config.test_argv`; it does not hardcode Go, Python, or Node commands.

## Governance loop

The development pack separates delivery from post-delivery governance:

1. plan and implement bounded tickets;
2. verify each ticket against its acceptance criteria;
3. run integrated QA, adversarial analysis, and code criticism;
4. turn findings into a new bounded plan;
5. integrate fixes and repeat within the policy budget;
6. run final lint/test gates before completion.

`qa`, `adversary`, and `critic` have distinct contracts. QA validates behavior and can use browser-oriented skills when the project provides them. The adversary evaluates the declared product objective and invariants, not only the literal ticket text. The critic reviews code quality, maintainability, and convention fit. See [the governed pack](examples/governed-pack/README.md).

## Watch and dashboard

```bash
orq-lite watch . --issues --prs
orq-lite serve --addr 127.0.0.1:4173
```

`watch` compiles its configured v2 flows at startup and emits idempotent triggers. The dashboard exposes only durable workflow APIs, the verified flow catalog, doctor checks, and live runtime events.

## Other commands

```text
doctor              validate the pack, roles, prompts, providers, credentials, and gates
log                 inspect the JSONL runtime event stream
cost                roll up recorded agent usage
reset               remove local .orquestalite state
update --check      check for a newer release
version             print the binary version
```

Architecture details live in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md), the v2 schema in [docs/activity-protocol.md](docs/activity-protocol.md), and pack rules in [docs/pack-format.md](docs/pack-format.md).
