![orquestalite hero](docs/hero.png)

# orquestalite

`orq-lite` is a local, durable workflow runtime for coordinating coding agents. Workflow behavior is data: strict v2 flows, subflows, schemas, policies, and prompts are distributed together as verified packs.

There is one execution path. The old hardcoded development loops and the in-memory `flows.json` interpreter are not part of the product.

## Bootstrap a repository

For an existing working repository, the recommended installation path is to let an agent drive the setup. Bootstrap requires judgment that the CLI cannot automate: understanding the existing worktree, making the baseline gates green, pinning the toolchain, configuring agents and roles, choosing the right V2 flow, and validating the pack before spending model time.

Give the agent this instruction:

> Set up Orquesta Lite in this repository by following every instruction in:
> https://raw.githubusercontent.com/collectiveai-team/orquesta-lite/main/guide.md
> Do not summarize the guide—perform the steps, verify each gate, and stop to report any blocker that cannot be resolved safely.

The guide covers installing `orq-lite` when it is not on `PATH`, initializing the built-in development pack, configuring `team.json`, proving lint/test gates, selecting a development and review strategy, launching idempotently, and monitoring or resuming the durable run.

If you only need the binary, install it directly and continue with the quick start:

```bash
go install github.com/collectiveai-team/orquesta-lite/cmd/orq-lite@latest
```

## Quick start

```bash
go install github.com/collectiveai-team/orquesta-lite/cmd/orq-lite@latest

cd your-project
orq-lite init --lang auto
orq-lite doctor
orq-lite plan features.md
orq-lite status
```

## Install the using-orq-lite skill

The `using-orq-lite` skill is the working reference for the flow lifecycle: choosing a flow, writing `team.json`, monitoring and recovering runs, and authoring flows. Install it into an agent-aware repository with:

```bash
npx skills add collectiveai-team/orquesta-lite --skill using-orq-lite
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
    "codex": {"provider": "codex", "model": "gpt-5.6-terra", "effort": "medium"}
  },
  "roles": {
    "coder": {
      "agents": ["codex"],
      "prompt": ".orquestalite/packs/development/5/prompts/coder.md",
      "result_path": ".orquestalite/results/coder.json",
      "timeout_seconds": 1800
    }
  },
  "limits": {
    "resume_sessions": true,
    "usage_guard": {
      "cache_ttl_seconds": 30,
      "max_reading_age_seconds": 900,
      "on_unavailable": "fallback",
      "providers": {
        "claude": {"max_used_percent": {"5h": 75, "7d": 60}},
        "codex": {"max_used_percent": {"5h": 80, "7d": 70}}
      }
    }
  },
  "rate_limit_backoff": {"initial_seconds": 30, "factor": 2, "max_seconds": 1800},
  "lint_argv": ["go", "vet", "./..."],
  "test_argv": ["go", "test", "./..."]
}
```

The governed pack obtains project-specific quality gates only through `config.lint_argv` and `config.test_argv`; it does not hardcode Go, Python, or Node commands.

### Provider usage guard

`limits.usage_guard` is optional. Before each configured Claude or Codex agent
starts (and before a corrective retry), orq-lite reads the account's 5-hour and
7-day subscription usage. A window at or above `max_used_percent` skips that
agent and advances to the next role fallback. If every eligible agent is
skipped, the role fails with a provider-usage-threshold error; it never waits
for a subscription reset.

Providers do not always expose every window for every plan. When at least one
configured window is available, orq-lite enforces the available values and
emits `provider_usage_partial` for the missing ones. If none of the configured
windows is available, `on_unavailable` applies. Codex uses the canonical
`rateLimitsByLimitId.codex` bucket when the installed App Server supplies it,
with its legacy rate-limit snapshot as a compatibility fallback.

The safe default for an unavailable usage source is `"fallback"`. Set
`"on_unavailable": "allow"` only when preserving execution is more important
than protecting an external subscription. The reader result is cached for 30
seconds by default and invalidated after each actual agent execution.

Registered providers are associated with their usage guard automatically. A
custom command whose executable is directly named `claude` or `codex` is also
detected. Wrappers must declare what they consume:

```json
{"cmd": ["company-agent-wrapper", "{{PROMPT}}"], "usage_provider": "claude"}
```

Every provider reports consumption on the same canonical scale: percent of the
window already used, from 0 to 100. Each reader converts at its own parse
boundary, because the scale cannot be inferred from a value alone - `0.04` is
equally valid as "0.04% used" and as a fraction meaning "4% used". A provider
with no converter is rejected rather than guessed at, and a reading off the
declared scale is treated as no reading rather than clamped into range.

Readings also carry when they were measured, which is not the same as when they
were read. `max_reading_age_seconds` (default 900) bounds how old a measurement
may be and still be enforced; anything older is reported as a stale window and
excluded, exactly like a window the provider never sent. This matters because
the providers differ sharply:

| | Codex | Claude |
| --- | --- | --- |
| Primary source | local `codex app-server` RPC | `GET /api/oauth/usage` |
| Local fallback | `~/.codex/sessions/**/rollout-*.jsonl`, written every turn | none - the CLI records only a breach flag, not a percentage |
| Throttling | none; the RPC is a local process | the endpoint rate-limits repeated polling |

Because Claude has no local percentage to fall back on, the guard keeps its
last successful reading and reuses it when a lookup fails, subject to the same
age limit. A rate-limited lookup is recognised as such and does **not** escalate
to Claude's interactive `/usage` panel: that panel is backed by the same account
and the same endpoint, so it would spend its timeout only to fail identically.

Claude first uses the local OAuth subscription reading. If credentials or that
request are unavailable, it opens Claude's bounded interactive `/usage` panel
as a fallback and parses the 5-hour and weekly values reported by the CLI.

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
