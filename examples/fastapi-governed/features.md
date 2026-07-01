# FastAPI Items Service

A small FastAPI application that manages "items" in memory. Standard library + FastAPI
only. Layered structure (routes → service → repository), Pydantic v2 request/response
models, async route handlers, and pytest tests via FastAPI's `TestClient`. Everything
must pass `uv run ruff check .` and `uv run pytest -q`.

## Health and Application Skeleton

Stand up the FastAPI app and its health surface.

- Create `app/main.py` exposing `app = FastAPI()` and a factory `create_app() -> FastAPI`.
- `GET /` returns `200` with `{"service": "items", "version": "0.1.0"}`.
- `GET /health` returns `200` with `{"status": "ok"}`.
- Add `tests/test_health.py` using `fastapi.testclient.TestClient` asserting both routes' status codes and JSON bodies.

## In-Memory Items CRUD

Add full CRUD for items backed by an in-memory repository.

- Pydantic v2 models in `app/schemas.py`: `ItemCreate {name: str (1..100), price: float (>= 0)}` and `ItemResponse {id: int, name: str, price: float}` (use `ConfigDict(from_attributes=True)`).
- An in-memory `ItemRepository` in `app/repository.py` (dict keyed by autoincrement id) and an `ItemService` in `app/service.py` holding business logic. No storage logic in the route handlers.
- Routes in `app/routes.py` mounted on the app:
  - `POST /items` → `201` with the created `ItemResponse`.
  - `GET /items` → `200` with a list of `ItemResponse`.
  - `GET /items/{item_id}` → `200`, or `404` `{"detail": "item not found"}` when missing.
  - `DELETE /items/{item_id}` → `204`, or `404` when missing.
- Add `tests/test_items.py` covering create→get→list→delete happy paths and the 404 cases.

## Item Search and Pagination

Extend the list endpoint with filtering and pagination.

- `GET /items` accepts query params `q: str | None` (case-insensitive substring match on `name`), `limit: int = 20` (1..100), and `offset: int = 0` (>= 0), validated with FastAPI `Query(...)`.
- Response is the filtered, paginated slice (stable order by `id`). Invalid params return `422`.
- Add `tests/test_search.py` covering: substring filtering, limit/offset paging, and a `422` for an out-of-range `limit`.
