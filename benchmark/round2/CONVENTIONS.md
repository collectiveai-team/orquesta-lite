# Hookrelay conventions (hard rules)

These are failure-mode rules, not style suggestions. The critic and reviewers
reject code that violates them even when tests pass.

## Layering and validation

- Routes → services → repositories. Route handlers hold zero business or
  storage logic; every write path goes through the service so validation cannot
  be bypassed at the router.
- Partial updates must never reset omitted fields — build updates from
  `model_dump(exclude_unset=True)` or explicit field lists.
- Join and look up by `id`, never by `target_url` or other mutable fields
  (matching new events to subscriptions by `event_types` is the one sanctioned
  content-based query).
- Single source of truth for constants (delivery statuses, header names,
  version string, backoff/attempt parameters): define once in
  `app/core/config.py` or one enum module and import it everywhere, including
  tests.

## Async and concurrency

- Async end to end: async SQLAlchemy sessions, async route handlers, async
  delivery I/O. Never call blocking I/O inside the event loop.
- Every background task created in the lifespan must be cancelled and awaited
  on shutdown. SSE subscriptions must be released on any disconnect path
  (normal close, error, cancellation).
- Bus publishing must never block or raise into the caller's transaction:
  publish after commit, fan out with bounded queues.
- Outbound delivery requests always carry an explicit timeout and run under
  the configured concurrency bound; one slow receiver must not stall the
  dispatcher.

## Data and errors

- UTC everywhere; every timestamp that leaves the service (response bodies,
  SSE frames, signature headers) is RFC3339 with an explicit timezone offset.
  Status strings are the closed set `pending|sending|delivered|dead`.
- Domain errors map to explicit HTTP codes (404/409/422) with the exact bodies
  the spec defines. Never return 500 for a predictable domain condition.
- The signature is computed over the exact bytes sent, keyed with the stored
  secret; the secret itself never appears in any response, log line, or event.
- A delivery that exhausts its attempts must end `dead` with its bookkeeping
  (`attempts`, `last_attempt_at`, `response_status` when one existed) intact;
  the dispatcher catches and persists — it does not propagate exceptions.

## Tests

- Self-contained: tmp-path SQLite per test; webhook receivers are in-process
  ASGI apps reached through the `delivery_client_factory` override; no network
  sockets; no sleeps as synchronization (poll with a bounded deadline instead).
- Existing consumers keep working: never change a shipped response shape,
  status code, or SSE message name; tests from earlier features must stay
  green untouched.
- New behavior lands with tests for the sad paths (404/409/422, retries, dead
  letters, disconnects, shutdown), not only the happy path.
