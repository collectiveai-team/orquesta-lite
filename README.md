![orquestalite hero](docs/hero.png)

# orquestalite

`orq-lite` is a local, durable workflow runtime for coordinating coding agents. Workflow behavior is data: strict v2 flows, subflows, schemas, policies, and prompts are distributed together as verified packs.

There is one execution path. The old hardcoded development loops and the in-memory `flows.json` interpreter are not part of the product.

## Bootstrap a repository

For an existing working repository, the recommended installation path is to let an agent drive the setup. Bootstrap requires judgment that the CLI cannot automate: understanding the existing worktree, making the baseline gates green, pinning the toolchain, configuring agents and roles, choosing the right V2 flow, and validating the pack before spending model time.

Give the agent this instruction:

> Set up Orquesta Lite in this repository by following every instruction in:
> https://raw.githubusercontent.com/lionelchamorro/orquesta-lite/main/guide.md
> Do not summarize the guide—perform the steps, verify each gate, and stop to report any blocker that cannot be resolved safely.

The guide covers installing `orq-lite` when it is not on `PATH`, initializing the built-in development pack, configuring `team.json`, proving lint/test gates, selecting a development and review strategy, launching idempotently, and monitoring or resuming the durable run.

If you only need the binary, install it directly and continue with the quick start:

```bash
go install github.com/collectiveai-team/orquestalite/cmd/orq-lite@latest
```

## Quick start

```bash
go install github.com/collectiveai-team/orquestalite/cmd/orq-lite@latest

cd your-project
orq-lite init --lang auto
orq-lite doctor
orq-lite plan features.md
orq-lite status
```

`init` creates:

- `team.json`, with provider-backed agents, dynamic roles, and `lint_argv` / `test_argv` gates;
- `.orquestalite/packs/development/5`, the built-in digest-verified development pack;
- `.orquestalite/workflows.db` on the first run, for durable workflow state.

It does not create or read `flows.json`.

## Development commands

The familiar commands are aliases into the installed `development` pack:

| Command | Flow |
|---|---|
| `orq-lite plan <plan.md>` | `development/plan-tickets@1` |
| `orq-lite run [--fast]` | `development/task-list@1` |
| `orq-lite factory <features.md> [--fast=false] [--pr]` | `development/factory-governed@2` |
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
development@5/task-list@1
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
      "prompt": ".orquestalite/packs/development/5/prompts/coder.md",
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

The development pack separates delivery from post-delivery governance. `factory` uses batch implementation by default, but the integrated governance phase is mandatory in both batch and ticket modes:

1. plan a bounded objective;
2. implement it as one batch by default, or as coder/ticket-QA iterations with `--fast=false`;
3. run deterministic gates and initial QA/repair for the selected delivery mode;
4. always run integrated QA, adversarial analysis, and code criticism;
5. integrate findings and repeat governance within the policy budget;
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

The end-to-end operator checklist lives in [guide.md](guide.md). Architecture details live in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md), the v2 schema in [docs/activity-protocol.md](docs/activity-protocol.md), and pack rules in [docs/pack-format.md](docs/pack-format.md).
