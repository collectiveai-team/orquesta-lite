"""Independent evaluation probe for the Hookrelay benchmark (round 2).

Black-box probes written from features.md ALONE, frozen before any condition
runs (see PROBE_SHA256). Copied verbatim into each implementation's repo root
and run with `uv run pytest -q test_probe.py`.

Probe version: 2.2 (pre-registered as 2.0 before any run; two probe defects
fixed during evaluation per evaluation.md §1.2 — both re-run against ALL
conditions. See PROBE_DEFECTS.md: v2.1 fixed the receiver's unresolvable
`Request` annotation (422'd every delivery); v2.2 rewrote the two SSE tests as
async raw-ASGI drivers because starlette's TestClient buffers responses and
can never read an infinite SSE stream).
"""

from __future__ import annotations

import asyncio
import hashlib
import hmac
import importlib
import json
import os
import sqlite3
import sys
import time
from datetime import datetime

import pytest
from fastapi import FastAPI, Request, Response

sys.path.insert(0, os.getcwd())

FAST_ENV = {
    "HOOKRELAY_DELIVERY_BACKOFF_BASE_S": "0.05",
    "HOOKRELAY_DELIVERY_TIMEOUT_S": "1.0",
    "HOOKRELAY_DELIVERY_MAX_ATTEMPTS": "3",
}
SECRET = "s3cret-key-123"
MISSING_ID = "00000000-0000-4000-8000-000000000000"


# ---------------------------------------------------------------- harness ----


def _purge_app_modules() -> None:
    for name in [n for n in list(sys.modules) if n == "app" or n.startswith("app.")]:
        del sys.modules[name]


class Receiver:
    """In-process ASGI webhook receiver with a programmable response plan."""

    def __init__(self, plan: list[int] | None = None, delay: float = 0.0):
        self.requests: list[dict] = []
        self.plan = list(plan or [])
        self.delay = delay

    def build(self):
        recv = FastAPI()

        @recv.post("/{path:path}")
        async def hook(request: Request):
            body = await request.body()
            self.requests.append(
                {
                    "body": body,
                    "headers": {k.lower(): v for k, v in request.headers.items()},
                }
            )
            if self.delay:
                await asyncio.sleep(self.delay)
            status = self.plan.pop(0) if self.plan else 200
            return Response(status_code=status)

        return recv


def _fresh_main(tmp_path, receiver: Receiver):
    """Fresh app module on a tmp DB, delivery client routed to `receiver`."""
    import httpx

    os.environ["HOOKRELAY_DB_PATH"] = str(tmp_path / "probe.db")
    os.environ.update(FAST_ENV)
    _purge_app_modules()
    http_mod = importlib.import_module("app.services.http")
    receiver_app = receiver.build()
    http_mod.delivery_client_factory = lambda: httpx.AsyncClient(
        transport=httpx.ASGITransport(app=receiver_app), base_url="http://receiver"
    )
    return importlib.import_module("app.main")


def _make_client(tmp_path, receiver: Receiver):
    """Fresh app instance on a tmp DB, delivery client routed to `receiver`."""
    from fastapi.testclient import TestClient

    main = _fresh_main(tmp_path, receiver)
    return TestClient(main.app)


class FeedTap:
    """Raw-ASGI driver for the SSE endpoint.

    (v2.2) starlette's TestClient buffers responses, so an infinite SSE stream
    can never be read through it — drive the ASGI app directly instead, with a
    hard timeout on every await.
    """

    def __init__(self, app, query: str = ""):
        self._app = app
        self._query = query
        self._sent: asyncio.Queue = asyncio.Queue()
        self._disconnect = asyncio.Event()
        self._got_request = False
        self._task = None
        self._buffer = ""
        self.status = None
        self.headers: dict[str, str] = {}

    async def _receive(self):
        if not self._got_request:
            self._got_request = True
            return {"type": "http.request", "body": b"", "more_body": False}
        await self._disconnect.wait()
        return {"type": "http.disconnect"}

    async def _send(self, message):
        await self._sent.put(message)

    async def start(self, timeout: float = 5.0):
        scope = {
            "type": "http",
            "asgi": {"version": "3.0"},
            "http_version": "1.1",
            "method": "GET",
            "path": "/feed",
            "raw_path": b"/feed",
            "query_string": self._query.encode(),
            "headers": [(b"host", b"probe"), (b"accept", b"text/event-stream")],
            "scheme": "http",
            "server": ("probe", 80),
            "client": ("probe", 1),
            "root_path": "",
        }
        self._task = asyncio.ensure_future(self._app(scope, self._receive, self._send))
        start = await asyncio.wait_for(self._sent.get(), timeout)
        assert start["type"] == "http.response.start", start
        self.status = start["status"]
        self.headers = {k.decode().lower(): v.decode() for k, v in start.get("headers", [])}

    async def next_frame(self, timeout: float = 10.0) -> dict:
        while True:
            if "\n\n" in self._buffer:
                block, self._buffer = self._buffer.split("\n\n", 1)
                for line in block.splitlines():
                    if line.startswith("data:"):
                        return json.loads(line[len("data:") :].strip())
                continue
            message = await asyncio.wait_for(self._sent.get(), timeout)
            if message["type"] == "http.response.body":
                self._buffer += message.get("body", b"").decode()
                if not message.get("more_body", True) and "\n\n" not in self._buffer:
                    raise AssertionError("SSE stream ended without a data frame")

    async def close(self, timeout: float = 5.0):
        self._disconnect.set()
        if self._task is not None:
            try:
                await asyncio.wait_for(self._task, timeout)
            except (TimeoutError, asyncio.TimeoutError):
                self._task.cancel()


async def _rest(main):
    import httpx

    return httpx.AsyncClient(
        transport=httpx.ASGITransport(app=main.app), base_url="http://probe"
    )


def _wait(cond, timeout: float = 8.0, interval: float = 0.05) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if cond():
            return True
        time.sleep(interval)
    return False


def _tz_aware(stamp: str) -> bool:
    return datetime.fromisoformat(stamp.replace("Z", "+00:00")).tzinfo is not None


def _mk_sub(client, url="http://receiver/hook", types=("order.created",), secret=SECRET):
    r = client.post(
        "/subscriptions",
        json={"target_url": url, "event_types": list(types), "secret": secret},
    )
    assert r.status_code == 201, r.text
    return r.json()


def _mk_event(client, type_="order.created", payload=None):
    r = client.post("/events", json={"type": type_, "payload": payload or {"n": 1}})
    assert r.status_code == 202, r.text
    return r.json()


def _deliveries(client, **params):
    r = client.get("/deliveries", params=params)
    assert r.status_code == 200, r.text
    return r.json()


def _next_data_frame(lines) -> str:
    for line in lines:
        if line.startswith("data:"):
            return line[len("data:") :].strip()
    raise AssertionError("SSE stream ended without a data frame")


# ------------------------------------------------------------------ probes ----


def test_health_and_root(tmp_path):
    with _make_client(tmp_path, Receiver()) as client:
        r = client.get("/health")
        assert r.status_code == 200 and r.json() == {"status": "ok"}
        r = client.get("/")
        assert r.status_code == 200
        assert r.json() == {"service": "hookrelay", "version": "0.1.0"}
        assert os.path.exists(os.environ["HOOKRELAY_DB_PATH"])


def test_subscription_shape_secret_hidden_tz_aware(tmp_path):
    with _make_client(tmp_path, Receiver()) as client:
        sub = _mk_sub(client)
        assert sub["target_url"] == "http://receiver/hook"
        assert sub["event_types"] == ["order.created"]
        assert sub["active"] is True
        assert SECRET not in json.dumps(sub), "secret leaked into response"
        assert _tz_aware(sub["created_at"]), "created_at is not timezone-aware"
        got = client.get(f"/subscriptions/{sub['id']}")
        assert got.status_code == 200
        assert SECRET not in got.text
        listed = client.get("/subscriptions")
        assert listed.status_code == 200
        assert SECRET not in listed.text
        assert listed.json()["total"] == 1


def test_subscription_conflict_and_reactivation_semantics(tmp_path):
    with _make_client(tmp_path, Receiver()) as client:
        sub = _mk_sub(client)
        dup = client.post(
            "/subscriptions",
            json={
                "target_url": "http://receiver/hook",
                "event_types": ["x"],
                "secret": SECRET,
            },
        )
        assert dup.status_code == 409, dup.text
        assert dup.json() == {"detail": "subscription already exists"}
        gone = client.delete(f"/subscriptions/{sub['id']}")
        assert gone.status_code == 204
        after = client.get(f"/subscriptions/{sub['id']}")
        assert after.status_code == 200 and after.json()["active"] is False
        again = client.post(
            "/subscriptions",
            json={
                "target_url": "http://receiver/hook",
                "event_types": ["x"],
                "secret": SECRET,
            },
        )
        assert again.status_code == 201, "inactive URL must be reusable"


def test_not_found_bodies(tmp_path):
    with _make_client(tmp_path, Receiver()) as client:
        r = client.get(f"/subscriptions/{MISSING_ID}")
        assert r.status_code == 404 and r.json() == {"detail": "subscription not found"}
        r = client.delete(f"/subscriptions/{MISSING_ID}")
        assert r.status_code == 404
        r = client.get(f"/events/{MISSING_ID}")
        assert r.status_code == 404 and r.json() == {"detail": "event not found"}


def test_validation_422(tmp_path):
    with _make_client(tmp_path, Receiver()) as client:
        bad_subs = [
            {"target_url": "not a url", "event_types": ["a"], "secret": SECRET},
            {"target_url": "http://r/h", "event_types": [], "secret": SECRET},
            {"target_url": "http://r/h", "event_types": ["a"], "secret": "short"},
        ]
        for body in bad_subs:
            assert client.post("/subscriptions", json=body).status_code == 422, body
        assert client.post("/events", json={"type": "", "payload": {}}).status_code == 422
        assert client.post("/events", json={"type": "a", "payload": "nope"}).status_code == 422
        for params in [
            {"status": "bogus"},
            {"limit": 0},
            {"limit": 101},
            {"offset": -1},
        ]:
            assert client.get("/deliveries", params=params).status_code == 422, params


def test_event_ingest_matching_and_inactive_exclusion(tmp_path):
    with _make_client(tmp_path, Receiver()) as client:
        sub_a = _mk_sub(client, url="http://receiver/a", types=["order.created"])
        _mk_sub(client, url="http://receiver/b", types=["user.signup"])
        ev = _mk_event(client, "order.created")
        assert ev["deliveries_created"] == 1
        assert _tz_aware(ev["event"]["created_at"])
        got = client.get(f"/events/{ev['event']['id']}")
        assert got.status_code == 200 and got.json()["type"] == "order.created"
        none = _mk_event(client, "unmatched.type")
        assert none["deliveries_created"] == 0
        client.delete(f"/subscriptions/{sub_a['id']}")
        after = _mk_event(client, "order.created")
        assert after["deliveries_created"] == 0, "inactive subscription still matched"


def test_delivery_happy_path_and_signature(tmp_path):
    receiver = Receiver()
    with _make_client(tmp_path, receiver) as client:
        _mk_sub(client)
        ev = _mk_event(client)
        assert _wait(
            lambda: _deliveries(client, status="delivered")["total"] == 1
        ), "delivery never completed"
        assert len(receiver.requests) == 1, "expected exactly one POST to the receiver"
        req = receiver.requests[0]
        ts = req["headers"].get("x-hookrelay-timestamp")
        sig = req["headers"].get("x-hookrelay-signature")
        assert ts and sig, "signature headers missing"
        assert _tz_aware(ts), "signature timestamp is not timezone-aware"
        expected = hmac.new(
            SECRET.encode(), f"{ts}.".encode() + req["body"], hashlib.sha256
        ).hexdigest()
        assert sig == expected, "HMAC does not verify against the sent bytes"
        sent = json.loads(req["body"])
        assert sent["event"]["id"] == ev["event"]["id"]
        assert sent["attempt"] == 1
        row = _deliveries(client, status="delivered")["deliveries"][0]
        assert row["response_status"] == 200
        assert row["attempts"] == 1
        assert _tz_aware(row["created_at"]) and _tz_aware(row["delivered_at"])


def test_delivery_retry_then_success(tmp_path):
    receiver = Receiver(plan=[500, 500, 200])
    with _make_client(tmp_path, receiver) as client:
        _mk_sub(client)
        _mk_event(client)
        assert _wait(lambda: _deliveries(client, status="delivered")["total"] == 1)
        assert len(receiver.requests) == 3, "expected exactly three attempts"
        row = _deliveries(client)["deliveries"][0]
        assert row["attempts"] == 3


def test_delivery_dead_after_max_attempts(tmp_path):
    receiver = Receiver(plan=[500, 500, 500, 500])
    with _make_client(tmp_path, receiver) as client:
        _mk_sub(client)
        _mk_event(client)
        assert _wait(lambda: _deliveries(client, status="dead")["total"] == 1)
        assert len(receiver.requests) == 3, "dead delivery must stop at max attempts"
        row = _deliveries(client, status="dead")["deliveries"][0]
        assert row["attempts"] == 3
        assert row["response_status"] == 500
        assert row["delivered_at"] is None


def test_deliveries_list_filters_and_pagination(tmp_path):
    receiver = Receiver()
    with _make_client(tmp_path, receiver) as client:
        _mk_sub(client)
        events = [_mk_event(client, payload={"n": i}) for i in range(3)]
        assert _wait(lambda: _deliveries(client, status="delivered")["total"] == 3)
        page = _deliveries(client, limit=2)
        assert page["total"] == 3 and len(page["deliveries"]) == 2
        rest = _deliveries(client, limit=2, offset=2)
        assert rest["total"] == 3 and len(rest["deliveries"]) == 1
        one = _deliveries(client, event_id=events[0]["event"]["id"])
        assert one["total"] == 1
        assert one["deliveries"][0]["event_id"] == events[0]["event"]["id"]
        stamps = [d["created_at"] for d in _deliveries(client)["deliveries"]]
        assert stamps == sorted(stamps, reverse=True), "not ordered created_at desc"


def test_stats_math(tmp_path):
    receiver = Receiver(plan=[200, 500, 500, 500])
    with _make_client(tmp_path, receiver) as client:
        _mk_sub(client)
        _mk_event(client)
        _mk_event(client)
        assert _wait(
            lambda: (
                _deliveries(client, status="delivered")["total"] == 1
                and _deliveries(client, status="dead")["total"] == 1
            )
        )
        stats = client.get("/stats").json()
        assert stats["deliveries"]["delivered"] == 1
        assert stats["deliveries"]["dead"] == 1
        assert stats["deliveries"]["total"] == 2
        assert stats["success_rate"] == pytest.approx(0.5)
        assert stats["avg_delivery_ms"] is not None and stats["avg_delivery_ms"] >= 0


def test_stats_empty_nulls(tmp_path):
    with _make_client(tmp_path, Receiver()) as client:
        stats = client.get("/stats").json()
        assert stats["deliveries"]["total"] == 0
        assert stats["success_rate"] is None
        assert stats["avg_delivery_ms"] is None
        assert stats["feed_clients"] == 0


async def test_sse_feed_filtering(tmp_path):
    receiver = Receiver()
    main = _fresh_main(tmp_path, receiver)
    async with main.app.router.lifespan_context(main.app):
        client = await _rest(main)
        r = await client.post(
            "/subscriptions",
            json={
                "target_url": "http://receiver/a",
                "event_types": ["a.happened"],
                "secret": SECRET,
            },
        )
        assert r.status_code == 201, r.text
        sub_a = r.json()
        r = await client.post(
            "/subscriptions",
            json={
                "target_url": "http://receiver/b",
                "event_types": ["b.happened"],
                "secret": SECRET,
            },
        )
        assert r.status_code == 201, r.text
        tap = FeedTap(main.app, f"subscription_id={sub_a['id']}")
        await tap.start()
        assert tap.status == 200
        assert tap.headers.get("content-type", "").startswith("text/event-stream")
        first = await tap.next_frame()
        assert first == {"event": "connected"}
        r = await client.post("/events", json={"type": "b.happened", "payload": {}})
        assert r.status_code == 202
        r = await client.post("/events", json={"type": "a.happened", "payload": {}})
        assert r.status_code == 202
        seen = []
        while True:
            msg = await tap.next_frame()
            seen.append(msg)
            if msg.get("status") in ("delivered", "dead"):
                break
        for msg in seen:
            assert msg["subscription_id"] == sub_a["id"], f"foreign frame leaked: {msg}"
            assert _tz_aware(msg["ts"])
        await tap.close()
        await client.aclose()


async def test_feed_clients_released_on_disconnect(tmp_path):
    main = _fresh_main(tmp_path, Receiver())
    async with main.app.router.lifespan_context(main.app):
        client = await _rest(main)
        assert (await client.get("/stats")).json()["feed_clients"] == 0
        tap = FeedTap(main.app)
        await tap.start()
        first = await tap.next_frame()
        assert first == {"event": "connected"}
        assert (await client.get("/stats")).json()["feed_clients"] == 1
        await tap.close()
        released = False
        deadline = time.monotonic() + 8.0
        while time.monotonic() < deadline:
            if (await client.get("/stats")).json()["feed_clients"] == 0:
                released = True
                break
            await asyncio.sleep(0.05)
        assert released, "SSE subscription leaked after client disconnect"
        await client.aclose()


def test_shutdown_leaves_no_sending_and_resumes(tmp_path):
    slow = Receiver(delay=30.0)
    client = _make_client(tmp_path, slow)
    db_path = os.environ["HOOKRELAY_DB_PATH"]
    with client:
        _mk_sub(client)
        _mk_event(client)
        assert _wait(lambda: len(slow.requests) >= 1), "delivery never started"
    # lifespan teardown has returned: the in-flight attempt was cancelled and
    # the row must not be stuck in `sending`.
    con = sqlite3.connect(db_path)
    try:
        statuses = [r[0] for r in con.execute("SELECT status FROM deliveries")]
    finally:
        con.close()
    assert statuses and "sending" not in statuses, f"stuck rows after shutdown: {statuses}"
    # A fresh app over the same DB must resume and complete the delivery
    # without a new POST /events.
    receiver = Receiver()
    client2 = _make_client(tmp_path, receiver)
    with client2:
        assert _wait(
            lambda: _deliveries(client2, status="delivered")["total"] == 1
        ), "interrupted delivery did not resume after restart"
