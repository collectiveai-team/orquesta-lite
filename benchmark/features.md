# Taskflow: async job service (FastAPI + SQLite + WebSocket + Prefect)

A self-contained Python 3.12 service that accepts "jobs" over a REST API, persists
them in SQLite, processes them through a Prefect (v3) flow behind a work queue, and
streams job lifecycle events over a WebSocket. This file is the full contract; the
governance roles review against it verbatim.

**Stack (fixed — do not substitute):** `uv` project with committed `uv.lock` and
`.python-version` (3.12). Dependencies: `fastapi`, `uvicorn`, `sqlalchemy[asyncio]`,
`aiosqlite`, `prefect>=3`, `pydantic-settings`; dev group: `pytest`, `pytest-asyncio`
(`asyncio_mode = "auto"` in `pyproject.toml`), `httpx`, `ruff`. Layered structure:
routes → services → repositories; no business or storage logic in route handlers.
SQLAlchemy 2.0 style (`Mapped[T]`, `mapped_column`, async engine,
`expire_on_commit=False`). Pydantic v2 request/response models, separate per
direction.

**Gates:** `uv run ruff check .` and `uv run pytest -q` must exit 0 after every
feature. Tests must be fully self-contained: a throwaway SQLite file per test (tmp
path via settings override), Prefect exercised through `prefect_test_harness` (one
session-scoped fixture in `tests/conftest.py`), no network access, no external
services, no ordering dependencies.

**Global rules:** every acceptance bullet below is a literal check — reviewers walk
them one by one. Existing behavior from earlier features must keep working when later
features land (existing consumers keep working is an implicit acceptance criterion of
every feature). Configuration only via `app/core/config.py` (`pydantic-settings`,
env prefix `TASKFLOW_`); no constant duplicated outside it.

## Application skeleton and SQLite persistence

Stand up the app factory, settings, and the persistence layer everything else builds on.

- `app/main.py` exposes `create_app() -> FastAPI` and a module-level `app = create_app()`.
- `app/core/config.py` defines `Settings(BaseSettings)` with `db_path: str = ".data/taskflow.db"` and `worker_mode: Literal["inline", "prefect"] = "inline"` (env vars `TASKFLOW_DB_PATH`, `TASKFLOW_WORKER_MODE`), plus a `get_settings()` accessor that the app reads at startup and tests can override.
- `app/db/models.py`: SQLAlchemy 2.0 `Job` model, table `jobs`: `id` (str UUID4 primary key), `type` (str), `status` (str, one of `pending|running|succeeded|failed`), `payload` (JSON), `result` (JSON, nullable), `error` (str, nullable), `created_at` / `started_at` / `finished_at` (UTC datetimes, only `created_at` non-null at insert).
- `app/db/session.py`: async engine on `sqlite+aiosqlite:///{db_path}` (parent dir created if missing), `async_sessionmaker(expire_on_commit=False)`, and `init_db()` that creates tables; `create_app()` runs `init_db()` via lifespan.
- `GET /health` → `200` `{"status": "ok"}`; `GET /` → `200` `{"service": "taskflow", "version": "0.1.0"}`.
- `tests/conftest.py` provides an app/client fixture wired to a tmp-path database; `tests/test_health.py` asserts both routes' status codes and bodies, and that the database file is created after startup.

## Jobs REST API

Full CRUD-style surface for jobs. Creation only records the job (`pending`) — processing arrives in the next feature.

- `app/schemas.py`: `JobCreate {type: Literal["word_count", "reverse", "summary_stats"], payload: dict}` and `JobResponse {id, type, status, payload, result, error, created_at, started_at, finished_at}` with `ConfigDict(from_attributes=True)`.
- `app/repositories/jobs.py` (`JobRepository`: create / get / list with filters / count aggregates / update status+result) and `app/services/jobs.py` (`JobService` holding the rules below). Routes in `app/routes/jobs.py` stay thin.
- `POST /jobs` → `201` with the created `JobResponse`, `status == "pending"`; unknown `type` or non-dict `payload` → `422`.
- `GET /jobs?status=&limit=&offset=` → `200` `{"jobs": [JobResponse, ...], "total": <int matching the filter>}`, ordered `created_at` desc then `id` asc; `limit` default 20 range 1..100, `offset >= 0`, out-of-range values → `422`; invalid `status` value → `422`.
- `GET /jobs/{id}` → `200`, or `404` `{"detail": "job not found"}`.
- `DELETE /jobs/{id}` → `204`; `409` `{"detail": "job is running"}` when `status == "running"`; `404` when unknown.
- `tests/test_jobs_api.py` covers create→get→list→delete happy paths, both filters and pagination, and every error status above (404, 409, 422 for type, limit, and status).

## Prefect processing pipeline behind a dispatcher

Process jobs through a Prefect flow of discrete tasks, enqueued at creation time via a worker-mode dispatcher.

- `app/worker/flow.py`: `@flow(name="process-job", log_prints=True)` `process_job(job_id: str)` composed of three `@task`s: `mark_running` (sets `status="running"`, `started_at`), `execute` (declared with `retries=1`), `finalize` (sets `succeeded` + `result` + `finished_at`, or `failed` + `error` + `finished_at`). A failing job (unknown type, missing/empty `payload["text"]`) must end `status="failed"` with a non-empty `error`; the flow itself must not raise.
- Deterministic results computed from `payload["text"]` (str): `word_count` → `{"words": int, "chars": int}`; `reverse` → `{"text": <reversed string>}`; `summary_stats` → `{"lines": int, "words": int, "unique_words": int}` (case-insensitive uniqueness).
- `app/services/dispatcher.py`: `JobDispatcher` Protocol with `enqueue(job_id: str) -> None`; `InlineDispatcher` schedules `process_job` on the running event loop (`asyncio.create_task`), `PrefectDispatcher` submits via `prefect.deployments.run_deployment("process-job/taskflow", parameters={"job_id": ...}, timeout=0)`. `JobService` picks the dispatcher from `Settings.worker_mode` and calls it after the `POST /jobs` commit.
- `app/worker/__main__.py`: `python -m app.worker` serves the deployment — `serve(process_job.to_deployment(name="taskflow"))`.
- `tests/test_worker.py` (under `prefect_test_harness`): runs `process_job` directly for each of the three types asserting DB status transitions, timestamps, and exact result payloads; runs the failure case asserting `failed` + `error`; asserts a `POST /jobs` in inline mode reaches a terminal status (poll `GET /jobs/{id}` with a bounded deadline) with the correct result; asserts the deployment built by `to_deployment` is named `taskflow`.

## WebSocket job event stream

Stream job lifecycle events to connected clients from an in-process event bus.

- `app/events.py`: `EventBus` with `subscribe() -> asyncio.Queue`, `unsubscribe(queue)`, `publish(event: dict)` (non-blocking fan-out; a slow subscriber must not block publishers — bounded queues, drop-oldest), and a `subscriber_count` property. One bus instance on `app.state`.
- Lifecycle events published by the service/worker persistence paths: `{"event": "job.created"|"job.started"|"job.succeeded"|"job.failed", "job_id": str, "status": str, "ts": <RFC3339 UTC>}` — `job.created` on POST, the rest from the flow's state transitions.
- `WS /ws/jobs` (optional `?job_id=` filter): on connect immediately sends `{"event": "connected"}`, then forwards matching events as JSON text frames; on client disconnect the subscription is removed (assert via `subscriber_count`).
- When `worker_mode == "prefect"`, a lifespan background task polls the `jobs` table every 0.5s and publishes the same events for status changes it observes (the worker runs out-of-process there); the change-detection function `diff_job_states(previous: dict[str, str], current: dict[str, str]) -> list[event]` is pure and unit-tested. The poller must not run in `inline` mode.
- `tests/test_ws.py`: over the TestClient WebSocket — create a job in inline mode and assert the ordered sequence `connected → job.created → job.started → job.succeeded` for that `job_id` with the filter applied; assert an unrelated job's events are filtered out; assert `subscriber_count` returns to 0 after disconnect; unit-test `diff_job_states` (new job, status change, no change).

## Stats endpoint and end-to-end verification

Aggregate metrics endpoint plus a black-box test of the whole pipeline, and operator docs.

- `GET /stats` → `200` `{"jobs": {"pending": int, "running": int, "succeeded": int, "failed": int, "total": int}, "by_type": {"word_count": int, "reverse": int, "summary_stats": int}, "avg_duration_s": float | null}` — `avg_duration_s` averages `finished_at - started_at` over terminal jobs only, `null` when there are none. Computed with SQL aggregates in the repository (no loading full rows into Python).
- `README.md` at the repo root: quickstart (`uv sync`, run API via `uvicorn app.main:app`, run worker via `python -m app.worker` with `TASKFLOW_WORKER_MODE=prefect`), an endpoint table covering every route above, a `websocket` usage example, and the two gate commands.
- `tests/test_stats.py`: seeds jobs in known states via the repository and asserts exact counts and `avg_duration_s` (including the `null` case).
- `tests/test_e2e.py`: public-surface-only lifecycle in inline mode — `POST /jobs` (each of the three types), observe `job.succeeded` on `/ws/jobs`, `GET /jobs/{id}` shows the exact expected result, `GET /jobs?status=succeeded` includes them, and `GET /stats` reflects the final counts.
