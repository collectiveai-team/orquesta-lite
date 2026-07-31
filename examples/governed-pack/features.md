# Demo: a tiny keyed counter API (FastAPI + in-memory store)

A deliberately small spec so a full governed run is cheap to watch end to end.
Two features, no database, no external services. This file is the full
contract; the governance roles review against it verbatim.

**Stack (fixed):** a `uv` project, Python 3.12, `fastapi` + `uvicorn`, dev
group `pytest` + `httpx` + `ruff`. Layered structure: routes → service. No
business logic in route handlers. Every acceptance bullet below is a literal
check reviewers walk one by one.

**Gates:** `uv run ruff check .` and `uv run pytest -q` must exit 0 after every
feature. These are this demo project's commands; the pack runs whatever
`lint_argv` / `test_argv` declare in `team.json`.

## Application skeleton

- `app/main.py` exposes `create_app() -> FastAPI` and a module-level
  `app = create_app()`.
- `GET /health` → `200` `{"status": "ok"}`.
- A test asserts the health route's status code and exact body.

## Keyed counter

- `app/service.py` holds an in-memory `Counter` service: `increment(key: str,
  by: int = 1) -> int` returns the new value; `get(key: str) -> int` returns
  the current value (0 for an unseen key); `reset(key: str) -> None`. The
  store is a single instance on `app.state`.
- `POST /counters/{key}/increment` with optional JSON body `{"by": int}`
  (default 1, must be a non-zero integer, else `422`) → `200`
  `{"key": str, "value": int}`.
- `GET /counters/{key}` → `200` `{"key": str, "value": int}` (0 for an unseen
  key).
- `DELETE /counters/{key}` → `204`, resetting the key to 0.
- Tests cover: increment then get returns the accumulated value; default
  step of 1; an unseen key reads 0; `by: 0` and non-integer `by` return
  `422`; delete resets to 0.
