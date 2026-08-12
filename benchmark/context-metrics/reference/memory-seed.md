# Repository notes (carried forward by previous iterations)

## Layering — respect this, reviewers check it

`routes/ -> services/ -> repositories/ -> db/`. Routes never touch the ORM
directly; they depend on a service. Services hold rules and raise typed domain
errors; repositories hold queries and return ORM objects. A route that queries
the session directly gets rejected.

Domain errors live next to their service (e.g. `SubscriptionNotFoundError`,
`SubscriptionAlreadyExistsError`, `EventNotFoundError`) and each route maps them
to an `HTTPException` with a lowercase `detail` string. `detail` wording is part
of the contract — tests assert on the exact string.

## Where things are

- `app/main.py` — `create_app()` is the only app factory. Every router must be
  registered here or it does not exist. Startup wires the DB and the dispatcher
  through the lifespan.
- `app/core/config.py` — `Settings` + a cached `get_settings()`. Read config
  through it; never read `os.environ` in a route or service.
- `app/db/session.py` — `configure_engine` / `init_db` / `get_session`.
  `get_session` is the FastAPI dependency; sessions are async throughout.
- `app/db/models.py` — the only place ORM models are declared. `utc_now()` is
  the shared timestamp helper; do not call `datetime.utcnow()` anywhere.
- `app/core/enums.py` — `DeliveryStatus` is the single source of truth for
  status values. Never inline status strings.
- `app/schemas.py` — every request/response model. Response models serialize
  datetimes with an explicit UTC offset via field serializers; a naive
  timestamp in a response is a defect that has already been caught twice.
- `tests/conftest.py` — `test_app` and `client` fixtures. Each test gets an
  isolated tmp SQLite file. Build new tests on these fixtures; do not stand up
  your own app instance unless the test is specifically about lifespan.

## Gates

- `uv run ruff check .` — must exit 0.
- `uv run pytest -q` — must exit 0. Baseline is 25 passing.
- Run both before reporting. A gate claim in the result JSON that does not
  reproduce is treated as a failed ticket.

## Traps hit in earlier tickets

- Adding a router file without registering it in `create_app()` — tests pass
  locally against the module but the endpoint 404s.
- Reusing a mutable default or module-level state across tests: the fixtures
  give you a fresh DB but module singletons persist within a process.
- Async tests need no explicit marker; `asyncio_mode = "auto"` is set in
  `pyproject.toml`.
- `TestClient` serializes requests, so it cannot prove real concurrency;
  concurrency tests drive the app directly instead.
