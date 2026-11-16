# Paperclip → Orquesta notification relay

This Paperclip plugin forwards issue and agent-run event hints to
`POST /webhooks/paperclip` on the Orquesta API. The engine still inspects the
issue/run, validates the deliverable and chooses every transition. The plugin
does not advance flows or send task output as completion authority.

## Installation

Use a Paperclip release with the plugin SDK and issue-create body
`idempotencyKey` support described in [the SDK](https://github.com/paperclipai/paperclip/tree/master/packages/plugins/sdk)
and [the issue service](https://github.com/paperclipai/paperclip/blob/master/server/src/services/issues.ts).
Install this directory as a local development plugin through Paperclip's plugin
manager, with the matching `@paperclipai/plugin-sdk` available. For a deployed
installation, package it with `npm pack` and install the resulting package using
Paperclip's package installation workflow. No build is needed for these ESM files.

Configure the plugin with:

```json
{
  "orquestaUrl": "https://orquesta.example",
  "orquestaToken": {"type": "secret_ref", "secretId": "paperclip-secret-id"}
}
```

The referenced secret must contain the same value injected as
`PAPERCLIP_WEBHOOK_TOKEN` into Orquesta. The plugin resolves it for each outbound
request. Never put the plaintext token in a flow, issue, plugin JSON or URL.
Orquesta refuses callbacks when that setting is absent, even in development.

In the Orquesta checkout, upgrade the database with `uv run alembic upgrade head`
before starting the updated API. The migration keeps a development slot reserved
while its flow is waiting, paused or awaiting intervention. The API starts the
reconciliation worker automatically and scans every thirty seconds.

Rebuild/install the matching orq-lite binary and point Orquesta's `ORQ_LITE_BIN`
at it. Managed `flow` launches receive a unique `orquesta:<launch-id>` source key.
An operator-supplied `--source-key` is reserved on this path.

## Correlation and recovery

Orq-lite appends a machine-readable marker containing flow, step, attempt and
source-key identifiers to each assigned issue. The relay reads that marker and
posts only normalized identifiers. The API acknowledges the event only after the
engine's inbox commits; it does not wait for a synchronous agent or gate.

The relay retains run correlation in Paperclip plugin state so a run-finished
event can still be mapped after the issue's execution lock clears. If the
provider version omits an event or delivery fails, startup and periodic
reconciliation obtain the same authoritative result. Duplicates cannot create
another assignment or another flow transition.

Issue/run states and actual delivered JSON are fetched from Paperclip, rather
than accepted from the notification. An open issue with a completed run remains
waiting. Credentials and provider responses are never logged by the relay.

## Coding-step variant

See [`examples/paperclip-coding/flow.json`](../../examples/paperclip-coding/flow.json).
Replace the coding step's executor and inputs while retaining its step ID and
output schema. Set `outputMode: "deliverables"` to keep the JSON shape consumed
by subsequent steps; attempt/cost metadata remains in the external-attempt API.
Use `projectId` when the issue must belong to a specific Paperclip project.

The assigned agent publishes the usual role result in an issue document keyed
`orq-result`, e.g. `{"deliverables": {"ticket_id": "T-1", "complete": true,
"summary": "Implemented", "files_changed": [], "gates": [], "remaining": []},
"evidence": ["a persisted proof reference"]}`. Its Paperclip workspace and tools
are configured in Paperclip, while the engine retains the deterministic flow.

## Tests

`node --test integrations/paperclip/relay.test.mjs` runs the relay protocol tests.
The Orquesta suite includes tests with the real orq-lite binary, SQLite and a
controlled HTTP Paperclip service. Set `ORQ_LITE_TEST_BIN` to the freshly built
binary and run `uv run pytest -q test/test_paperclip_engine_integration.py` in
the Orquesta checkout. Both callback delivery and callback loss are exercised.
