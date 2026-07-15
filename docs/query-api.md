# Query API

## Durable workflows

The v2 runtime exposes read-only workflow state in addition to the event-log
projection:

- `GET /api/workflows?limit=50`
- `GET /api/workflows/{run_id}`
- `GET /api/workflows/{run_id}/steps`
- `GET /api/workflows/{run_id}/approvals`

These endpoints read `.orquestalite/workflows.db`, the operational checkpoint
store. Existing `/api/runs` endpoints continue to read the rebuildable
`.orquestalite/orq.db` event projection.

`orq-lite serve` (default `127.0.0.1:4173`) exposes read-only JSON endpoints
over a sqlite read-model of `.orquestalite/run.log` (db at
`.orquestalite/orq.db`, built automatically by serve on a 1-second cadence, or
headlessly via `orq-lite index [--rebuild]`). Every endpoint is `GET`, returns
`Cache-Control: no-store`, and the shapes below are a contract shared with the
orquesta control plane (`docs/orq-lite-query-api.md` in that repo) — change
them in both repos or not at all.

Pagination everywhere: `limit` (default 50, max 500) and `offset` (default 0).
Values that fail to parse fall back to the defaults; unknown query params are
ignored.

## GET /api/runs?limit=&offset=&active=true|false

Run history, newest first. `active=true` filters to `status == "running"`
(for correlating companion-app launch records with `run_id`); `active=false`
to finished runs.

```json
{
  "runs": [
    {
      "run_id": "r20260701T120000Z-4f2a",
      "command": "factory",
      "args": ["features.md"],
      "status": "ok",
      "started_at": "2026-07-01T12:00:00.123456789Z",
      "finished_at": "2026-07-01T12:41:07.5Z",
      "duration_s": 2467,
      "orq_version": "v0.2.0",
      "cost_usd": 1.0421,
      "input_tokens": 291042,
      "output_tokens": 48211,
      "agent_runs": 14,
      "tasks_done": 5,
      "tasks_failed": 1
    }
  ],
  "total": 1
}
```

`status` is one of `running` (no `run_end` yet — `finished_at` and
`duration_s` are `null`), `ok`, `error`, `interrupted`. `tasks_done` counts
`task_done` and `task_done_no_commit` events; `tasks_failed` counts
`task_failed`. `cost_usd` prices first-party token counts with the same table
`GET /api/cost` uses; agent runs with unknown models contribute tokens but
zero cost.

## GET /api/runs/{id}

One `RunSummary` (same shape as above). Unknown id:

```json
HTTP 404
{"error": "unknown run id: r20990101T000000Z-dead"}
```

## GET /api/runs/{id}/events?type=&task_id=&limit=&offset=

The run's raw JSONL events, parsed, in log order. `type` and `task_id`
filter; omitted means all.

```json
{
  "events": [
    {"ts": "2026-07-01T12:00:00Z", "event": "run_start", "run_id": "r20260701T120000Z-4f2a", "command": "factory", "args": ["features.md"], "orq_version": "v0.2.0"},
    {"ts": "2026-07-01T12:00:41Z", "event": "task_start", "run_id": "r20260701T120000Z-4f2a", "task_id": "T001", "title": "…", "attempt": 1}
  ],
  "total": 2
}
```

## GET /api/agent-runs?run_id=&task_id=&role=&agent=&limit=&offset=

Individual agent invocations, newest first. All filters optional and ANDed.

```json
{
  "agent_runs": [
    {
      "ts": "2026-07-01T12:05:41Z",
      "run_id": "r20260701T120000Z-4f2a",
      "role": "coder",
      "agent": "sonnet",
      "task_id": "T001",
      "cycle": 1,
      "attempt": 1,
      "provider": "claude",
      "model": "claude-sonnet-4-6",
      "duration_s": 42,
      "exit_code": 0,
      "timed_out": false,
      "rate_limited": false,
      "input_tokens": 18042,
      "output_tokens": 2211,
      "cached_input_tokens": 15020,
      "reasoning_tokens": 0,
      "cost_usd": 0.0873,
      "artifacts_dir": ".orquestalite/runs/r20260701T120000Z-4f2a/agents/T001/coder.c1.a1"
    }
  ],
  "total": 1
}
```

`input_tokens` includes cached input tokens (mirroring the `agent_run`
event). Missing token fields on old events read as 0.

## GET /api/stats/cost?by=run|agent|task|role

Cost rollup grouped by the given dimension (default `run`; unknown values
fall back to `run`), sorted by `cost_usd` descending.

```json
{
  "by": "role",
  "rows": [
    {"key": "coder",  "cost_usd": 0.7311, "input_tokens": 201042, "output_tokens": 31200, "agent_runs": 8},
    {"key": "critic", "cost_usd": 0.3110, "input_tokens": 90000,  "output_tokens": 17011, "agent_runs": 6}
  ]
}
```

## GET /api/flows

The workspace's `flows.json` parsed with the engine's own parser, for
building a launch form without filesystem access. Empty or missing
`flows.json` → `{"flows": []}`. Ordered by name.

```json
{
  "flows": [
    {
      "name": "factory",
      "description": "decompose features.md and build each feature",
      "inputs": {
        "features_file": {"type": "string", "default": null, "required": true},
        "max_cycles": {"type": "number", "default": 3, "required": false}
      },
      "roles": ["coder", "critic", "parser"],
      "preflight": {"coder": "ok", "critic": "missing_prompt", "parser": "missing_role"}
    }
  ]
}
```

`required` means the flow declares no default for that input. `roles` is the
sorted set of agent roles referenced by any agent step, recursively through
`loop`/`retry_until` bodies. `preflight` per role: `ok` (role exists in
`team.json` and its prompt file exists), `missing_role`, or
`missing_prompt`.

## GET /api/doctor

The `orq-lite doctor` preflight as JSON — same check functions, so CLI and
endpoint can never disagree. Cached server-side for 30s.

```json
{
  "ok": false,
  "checks": [
    {"name": "git",             "status": "ok",    "detail": "repository present, tree clean"},
    {"name": "eventdb",         "status": "warn",  "detail": "orq.db not found — run `orq-lite index` or `orq-lite serve` to build it"},
    {"name": "team.json",       "status": "error", "detail": "read team.json: no such file"},
    {"name": "provider:claude", "status": "ok",    "detail": "on PATH"},
    {"name": "binary:gh",       "status": "warn",  "detail": "not on PATH — factory --pr disabled"}
  ]
}
```

`ok` is false iff any check has status `error`. Statuses map to the CLI's
PASS/WARN/FAIL.
