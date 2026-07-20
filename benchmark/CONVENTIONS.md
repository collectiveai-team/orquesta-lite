# Taskflow conventions (hard rules)

These are failure-mode rules, not style suggestions. The critic and reviewers reject
code that violates them even when tests pass.

## Layering and validation

- Routes → services → repositories. Route handlers hold zero business or storage
  logic; every write path (including future bulk/alternative paths) goes through the
  service so validation cannot be bypassed at the router.
- Partial updates must never reset omitted fields — build updates from
  `model_dump(exclude_unset=True)` or explicit field lists.
- Join and look up by `id`, never by name or other mutable fields.
- Single source of truth for constants (job types, statuses, queue/deployment names,
  version string): define once in `app/core/config.py` or one enum module and import
  it everywhere, including tests.

## Async and concurrency

- Async end to end: async SQLAlchemy sessions, async route handlers, async Prefect
  flow/tasks. Never call blocking I/O inside the event loop.
- Every background task created in the lifespan must be cancelled and awaited on
  shutdown. WebSocket subscriptions must be released on any disconnect path
  (normal close, error, cancellation).
- Event publishing must never block or raise into the caller's transaction: publish
  after commit, fan out with bounded queues.

## Data and errors

- UTC everywhere; timestamps serialized as RFC3339. Status strings are the closed
  set `pending|running|succeeded|failed`.
- Domain errors map to explicit HTTP codes (404/409/422) with the exact bodies the
  spec defines. Never return 500 for a predictable domain condition.
- A job that fails must record a non-empty `error` and a `finished_at`; the flow
  catches and persists — it does not propagate exceptions to the queue.

## Tests

- Self-contained: tmp-path SQLite per test, `prefect_test_harness` session fixture,
  no network, no sleeps as synchronization (poll with a bounded deadline instead).
- Existing consumers keep working: never change a shipped response shape, status
  code, or event name; tests from earlier features must stay green untouched.
- New behavior lands with tests for the sad paths (404/409/422/failed job), not only
  the happy path.
