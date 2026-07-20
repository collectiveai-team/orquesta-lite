# Probe defect log (evaluation.md §1.2)

## v2.0 → v2.1 (2026-07-18, found during first evaluation run)

**Defect:** `test_probe.py` declares `from __future__ import annotations`; the
in-process receiver imported `FastAPI/Request/Response` inside
`Receiver.build()`. Under PEP 563, the handler's `request: Request` annotation
is the *string* `"Request"`, resolved against module globals — where the name
did not exist. FastAPI silently fell back to treating `request` as a required
query parameter, so the receiver answered **422 to every delivery without
executing the handler**. Every delivery-dependent probe test failed/hung
identically across all three conditions (7/15 each) while manual verification
of the same behaviors passed.

**Detection:** identical failure pattern across three independent
implementations + checklist agents verifying the same behaviors green →
§1.2 "no conforming implementation could pass" trigger. Confirmed by driving
the injected client directly: `{"detail":[{"type":"missing","loc":["query",
"request"]}]}` with zero receiver hits.

**Fix (minimal):** move the fastapi imports to module level so the deferred
annotation resolves. No test logic changed.

**Action:** per protocol, v2.1 re-run against ALL conditions; v2.0 results
discarded. v2.0 frozen hash remains in PROBE_SHA256 for the audit trail;
v2.1 hash in PROBE_SHA256.v2.1.

## v2.1 → v2.2 (2026-07-18, same evaluation session)

**Defect:** the two SSE tests (`test_sse_feed_filtering`,
`test_feed_clients_released_on_disconnect`) drove `GET /feed` through
starlette's `TestClient.stream(...)`. That client buffers the response body —
an infinite `text/event-stream` never completes, so both tests hung (timeout)
on every condition, while all three implementations' own SSE tests and the
checklist agents' live verification of the same behaviors passed.

**Fix (minimal):** the two tests became async (`asyncio_mode = "auto"` is
mandated by the spec) and drive the ASGI app directly via a `FeedTap` helper
(raw http scope, receive/send callables, `asyncio.wait_for` hard timeout on
every await, disconnect delivered as `http.disconnect`). App lifespan is
entered manually (`app.router.lifespan_context`) so dispatcher, bus, REST
calls, and the tap all share one event loop. Assertions unchanged.

**Action:** v2.2 re-run against ALL conditions; v2.1 SSE results discarded.
