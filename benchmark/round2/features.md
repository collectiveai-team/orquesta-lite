# Hookrelay: webhook delivery service (FastAPI + SQLite + SSE)

A self-contained Python 3.12 service that registers webhook subscriptions over a
REST API, ingests events, fans them out to subscribers through an async delivery
dispatcher with retries and HMAC signing, exposes a delivery log and aggregate
stats, and streams delivery status changes over Server-Sent Events. This file is
the full contract; reviewers walk the acceptance bullets one by one, verbatim.

**Stack (fixed — do not substitute):** `uv` project with committed `uv.lock` and
`.python-version` (3.12). Dependencies: `fastapi`, `uvicorn`,
`sqlalchemy[asyncio]`, `aiosqlite`, `pydantic-settings`, `httpx` (runtime — it
is the delivery client); dev group: `pytest`, `pytest-asyncio`
(`asyncio_mode = "auto"` in `pyproject.toml`), `ruff`. Layered structure:
routes → services → repositories; no business or storage logic in route
handlers. SQLAlchemy 2.0 style (`Mapped[T]`, `mapped_column`, async engine,
`expire_on_commit=False`). Pydantic v2 request/response models, separate per
direction.

**Gates:** `uv run ruff check .` and `uv run pytest -q` must exit 0 after every
feature. Tests must be fully self-contained: a throwaway SQLite file per test
(tmp path via settings override), **no network sockets** — webhook receivers in
tests are in-process ASGI apps reached through the delivery-client override
below — and no ordering dependencies between tests.

**Delivery-client override (testability contract):** `app/services/http.py`
defines exactly this seam:

```python
def default_delivery_client() -> httpx.AsyncClient:
    """Real client used in production."""

delivery_client_factory: Callable[[], httpx.AsyncClient] = default_delivery_client
```

The dispatcher obtains its HTTP client exclusively by calling
`http.delivery_client_factory()` during startup. Tests reassign the module
attribute before the app starts, e.g.:

```python
from app.services import http
http.delivery_client_factory = lambda: httpx.AsyncClient(
    transport=httpx.ASGITransport(app=receiver_app), base_url="http://receiver"
)
```

**Global rules:** every acceptance bullet below is a literal check. Behavior
from earlier features must keep working when later features land. Configuration
only via `app/core/config.py` (`pydantic-settings`, env prefix `HOOKRELAY_`);
no constant duplicated outside it. Every timestamp that leaves the service —
response bodies, SSE frames, signature headers — is RFC3339 **UTC with an
explicit timezone offset**.

## Application skeleton and SQLite persistence

Stand up the app factory, settings, and the persistence layer everything else
builds on.

- `app/main.py` exposes `create_app() -> FastAPI` and a module-level
  `app = create_app()`.
- `app/core/config.py` defines `Settings(BaseSettings)` with
  `db_path: str = ".data/hookrelay.db"`, `delivery_max_attempts: int = 3`,
  `delivery_backoff_base_s: float = 0.1`, `delivery_timeout_s: float = 2.0`,
  `delivery_concurrency: int = 10` (env vars `HOOKRELAY_DB_PATH`,
  `HOOKRELAY_DELIVERY_MAX_ATTEMPTS`, `HOOKRELAY_DELIVERY_BACKOFF_BASE_S`,
  `HOOKRELAY_DELIVERY_TIMEOUT_S`, `HOOKRELAY_DELIVERY_CONCURRENCY`), plus a
  `get_settings()` accessor read at startup that tests can override.
- `app/db/models.py`, SQLAlchemy 2.0, tables `subscriptions` / `events` /
  `deliveries`: `Subscription` (`id` str UUID4 PK,
  `target_url` str, `event_types` JSON list of str, `secret` str, `active` bool
  default true, `created_at` UTC datetime), `Event` (`id` str UUID4 PK, `type`
  str, `payload` JSON, `created_at`), `Delivery` (`id` str UUID4 PK, `event_id`
  FK, `subscription_id` FK, `status` str one of `pending|sending|delivered|dead`,
  `attempts` int default 0, `response_status` int nullable, `created_at`,
  `last_attempt_at` / `next_attempt_at` / `delivered_at` nullable UTC
  datetimes) with a unique constraint on `(event_id, subscription_id)`.
- `app/db/session.py`: async engine on `sqlite+aiosqlite:///{db_path}` (parent
  dir created if missing), `async_sessionmaker(expire_on_commit=False)`, and
  `init_db()` creating tables; `create_app()` runs `init_db()` via lifespan.
- `GET /health` → `200` `{"status": "ok"}`; `GET /` → `200`
  `{"service": "hookrelay", "version": "0.1.0"}`.
- The test suite provides an app/client fixture wired to a tmp-path database
  and asserts both routes' status codes and bodies, and that the database file
  exists after startup.

## Subscriptions and event ingest API

Register webhook subscribers and accept events. Ingest only records the event
and creates its pending deliveries — the dispatcher arrives in the next
feature.

- `app/schemas.py`: `SubscriptionCreate {target_url: str (must parse as
  http/https URL), event_types: list[str] (non-empty, each non-empty),
  secret: str (min length 8)}`; `SubscriptionResponse {id, target_url,
  event_types, active, created_at}` — **the secret must never appear in any
  response body**. `EventCreate {type: str (non-empty), payload: dict}`;
  `EventResponse {id, type, payload, created_at}`. Responses use
  `ConfigDict(from_attributes=True)`.
- Repositories in `app/repositories/`, rules in `app/services/`; routes stay
  thin.
- `POST /subscriptions` → `201` with `SubscriptionResponse`; a `target_url`
  already used by an **active** subscription → `409`
  `{"detail": "subscription already exists"}`; invalid body → `422`.
- `GET /subscriptions` → `200` `{"subscriptions": [SubscriptionResponse, ...],
  "total": <int>}` ordered `created_at` desc then `id` asc;
  `GET /subscriptions/{id}` → `200` or `404`
  `{"detail": "subscription not found"}`; `DELETE /subscriptions/{id}` → `204`
  and sets `active = false` (the row is kept but excluded from all future
  matching); unknown id → `404`.
- `POST /events` → `202` `{"event": EventResponse, "deliveries_created": <int>}`
  — creates exactly one `pending` `Delivery` per **active** subscription whose
  `event_types` contains the event's `type`; zero matches is valid
  (`deliveries_created: 0`); invalid body → `422`. `GET /events/{id}` → `200`
  or `404` `{"detail": "event not found"}`.
- Tests cover create→get→list→delete happy paths, the matching rule (only
  active + type-matching subscriptions get deliveries), and every error status
  above (409, both 404s, 422 for each invalid field).

## Delivery dispatcher

Deliver pending rows to their target URLs with signing, bounded retries, and
bounded concurrency.

- `app/services/dispatcher.py`: a dispatcher started and stopped by the app
  lifespan. It claims due deliveries (`status == "pending"` and
  `next_attempt_at` null or `<= now`), sets `sending` + `last_attempt_at`,
  and POSTs JSON `{"delivery_id": str, "attempt": int, "event": {"id", "type",
  "payload", "created_at"}}` to the subscription's `target_url` using the
  client from `http.delivery_client_factory()`, with request timeout
  `delivery_timeout_s` and at most `delivery_concurrency` deliveries in flight
  at once. A due delivery must begin its attempt within 1 second of becoming
  due (poll interval, wake-up mechanism, and claim batching are free choices
  inside that bound).
- Request headers: `X-Hookrelay-Timestamp` (RFC3339 UTC) and
  `X-Hookrelay-Signature` = HMAC-SHA256 hex digest keyed with the
  subscription's `secret` over `f"{timestamp}.{body}"` where `timestamp` is the
  header value and `body` is the **exact request body bytes sent**.
- Outcome handling: a 2xx response → `delivered` (+ `delivered_at`,
  `response_status`). Any non-2xx response, timeout, or connection error
  increments `attempts`; if `attempts < delivery_max_attempts` the row returns
  to `pending` with `next_attempt_at = now + delivery_backoff_base_s *
  2**(attempts-1)`; at `delivery_max_attempts` it becomes `dead`
  (`response_status` recorded when a response existed). A claim whose
  subscription is no longer active becomes `dead` without an HTTP attempt.
- No duplicate work: never a second `Delivery` row for the same
  `(event, subscription)` and never two POSTs for the same delivery attempt —
  a test receiver must observe exactly the expected number of requests.
- Tests drive an in-process ASGI receiver through the client override and
  cover: a successful delivery whose signature the receiver verifies by
  recomputation; a retry sequence (500, 500, 200 → `delivered` with
  `attempts == 3` and exactly 3 received requests); permanent failure
  (always 500 → `dead` at `delivery_max_attempts`); and a timeout counting as
  a failed attempt. Polling uses bounded deadlines, never bare sleeps.

## Delivery log and stats

Operator surface: query deliveries, aggregate health.

- `GET /deliveries?subscription_id=&event_id=&status=&limit=&offset=` → `200`
  `{"deliveries": [DeliveryResponse, ...], "total": <int matching the
  filter>}`, ordered `created_at` desc then `id` asc; `limit` default 20 range
  1..100, `offset >= 0`, out-of-range values → `422`; invalid `status` value →
  `422`. `DeliveryResponse {id, event_id, subscription_id, status, attempts,
  response_status, created_at, last_attempt_at, next_attempt_at,
  delivered_at}`.
- `GET /stats` → `200` `{"deliveries": {"pending": int, "sending": int,
  "delivered": int, "dead": int, "total": int}, "success_rate": float | null,
  "avg_delivery_ms": float | null, "feed_clients": int}` — `success_rate` =
  `delivered / (delivered + dead)`, null when no delivery is terminal;
  `avg_delivery_ms` averages `delivered_at - created_at` in milliseconds over
  `delivered` rows only, null when there are none. Computed with SQL
  aggregates in the repository (no loading full rows into Python).
- Tests seed deliveries in known states via the repository and assert exact
  counts, `success_rate`, `avg_delivery_ms` (including both null cases), plus
  list filters, pagination, and every `422` above.

## SSE delivery status feed

Stream delivery status changes to connected clients from an in-process bus.

- `app/bus.py`: `StatusBus` with `subscribe() -> asyncio.Queue`,
  `unsubscribe(queue)`, `publish(msg: dict)` (non-blocking fan-out; a slow
  consumer must not block publishers — bounded queues, drop-oldest), and a
  `subscriber_count` property. One instance on `app.state`; the `feed_clients`
  field of `GET /stats` reports `subscriber_count`.
- After every delivery status transition is committed, the service/dispatcher
  publishes `{"delivery_id": str, "event_id": str, "subscription_id": str,
  "status": str, "attempts": int, "ts": <RFC3339 UTC>}`.
- `GET /feed` (optional `?subscription_id=` filter) → `text/event-stream`;
  each message is one `data: <json>\n\n` frame; on connect the first frame is
  `data: {"event": "connected"}`; with the filter, only that subscription's
  delivery messages are forwarded; without it, all.
- Every client disconnect path (normal close, error, cancellation) releases
  the bus subscription — observable because `feed_clients` in `GET /stats`
  returns to its prior value.
- Tests cover: a client with a filter receives `connected` and then the
  matching delivery frames; an unrelated subscription's frames are filtered
  out; `feed_clients` drops back to 0 after the client closes.

## Graceful shutdown, resumption, and operator docs

The service must be safe to stop and restart at any moment without losing or
wedging deliveries.

- On app shutdown the dispatcher stops claiming new work; attempts already in
  flight get up to `delivery_timeout_s` to finish and are then cancelled; any
  delivery still `sending` when the app exits is reverted to `pending`
  (attempts unchanged, `next_attempt_at` = now). **After lifespan teardown
  returns, no row has `status == "sending"`.**
- Every background task the app created is cancelled and awaited during
  teardown; shutdown completes even with deliveries in flight, and starting a
  new app instance afterwards works (no leaked global state).
- Resumption: on startup the dispatcher picks up existing due `pending` rows
  (including reverted and scheduled retries) with no new `POST /events` —
  a delivery interrupted by shutdown completes after restart.
- `README.md` at the repo root: quickstart (`uv sync`, run via
  `uvicorn app.main:app`), an endpoint table covering every route above, a
  receiver-side signature verification snippet (recomputing the HMAC from the
  two headers and the raw body), and the two gate commands.
- Tests cover: shutdown mid-delivery against a slow receiver leaves no
  `sending` rows; a fresh app over the same database file then completes the
  delivery; and an end-to-end pass — two subscriptions (one matching), one
  `POST /events`, the receiver gets exactly one correctly signed request, and
  `GET /deliveries` + `GET /stats` reflect the outcome.
