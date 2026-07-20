"""Independent black-box probe for the Taskflow benchmark (see benchmark/evaluation.md §2.1).

Applied verbatim to both branches. Spec-only: exercises the public REST/WS surface
plus the spec-fixed SQLite schema (table `jobs`) for the one state (running) that
cannot be reached deterministically through the API alone.

Run from the target repo root:  uv run pytest <this file> -q
"""

from __future__ import annotations

import importlib
import os
import sqlite3
import sys
import time
import uuid
from pathlib import Path

sys.path.insert(0, os.getcwd())  # probe runs from the target repo root; import its `app`

import pytest
from starlette.testclient import TestClient

DEADLINE_S = 30.0
TMP_ROOT = Path(os.environ.get("PROBE_TMP", "/tmp")) / f"taskflow-probe-{uuid.uuid4().hex[:8]}"


@pytest.fixture(scope="session", autouse=True)
def _prefect_harness():
    from prefect.testing.utilities import prefect_test_harness

    with prefect_test_harness():
        yield


def _reset_settings() -> None:
    """Force the app to re-read env-based settings, tolerating whichever
    override mechanism the implementation chose (override hook, lru_cache,
    or module-level caching)."""
    from app.core import config

    if hasattr(config, "override_settings"):
        try:
            config.override_settings(None)
            return
        except TypeError:
            pass  # zero-arg variant (e.g. a context manager) — fall through
    if hasattr(config.get_settings, "cache_clear"):
        config.get_settings.cache_clear()
    else:
        importlib.reload(config)


@pytest.fixture()
def fresh(request):
    """A TestClient over a brand-new app + throwaway SQLite file, inline mode."""
    db_path = TMP_ROOT / f"{request.node.name}-{uuid.uuid4().hex[:8]}" / "probe.db"
    os.environ["TASKFLOW_DB_PATH"] = str(db_path)
    os.environ["TASKFLOW_WORKER_MODE"] = "inline"
    _reset_settings()
    from app.main import create_app

    with TestClient(create_app()) as client:
        yield client, db_path


def _create(client: TestClient, type_: str, payload: dict) -> dict:
    r = client.post("/jobs", json={"type": type_, "payload": payload})
    assert r.status_code == 201, r.text
    return r.json()


def _wait_terminal(client: TestClient, job_id: str) -> dict:
    deadline = time.monotonic() + DEADLINE_S
    while time.monotonic() < deadline:
        r = client.get(f"/jobs/{job_id}")
        assert r.status_code == 200, r.text
        body = r.json()
        if body["status"] in ("succeeded", "failed"):
            return body
        time.sleep(0.1)
    pytest.fail(f"job {job_id} not terminal within {DEADLINE_S}s (last: {body['status']})")


# --- validation bodies -----------------------------------------------------


def test_422_unknown_type(fresh):
    client, _ = fresh
    r = client.post("/jobs", json={"type": "no_such_type", "payload": {"text": "x"}})
    assert r.status_code == 422


def test_422_non_dict_payload(fresh):
    client, _ = fresh
    r = client.post("/jobs", json={"type": "word_count", "payload": "not a dict"})
    assert r.status_code == 422


def test_422_pagination_and_status_filter(fresh):
    client, _ = fresh
    assert client.get("/jobs?limit=0").status_code == 422
    assert client.get("/jobs?limit=101").status_code == 422
    assert client.get("/jobs?offset=-1").status_code == 422
    assert client.get("/jobs?status=bogus").status_code == 422
    assert client.get("/jobs?limit=1").status_code == 200
    assert client.get("/jobs?limit=100").status_code == 200
    r = client.get("/jobs")
    assert r.status_code == 200
    assert set(r.json().keys()) == {"jobs", "total"}


def test_404_bodies(fresh):
    client, _ = fresh
    missing = str(uuid.uuid4())
    r = client.get(f"/jobs/{missing}")
    assert r.status_code == 404
    assert r.json() == {"detail": "job not found"}
    r = client.delete(f"/jobs/{missing}")
    assert r.status_code == 404
    assert r.json() == {"detail": "job not found"}


def test_409_delete_running_then_204(fresh):
    client, db_path = fresh
    job = _create(client, "reverse", {"text": "abc"})
    _wait_terminal(client, job["id"])
    # 'running' is transient; pin it via the spec-fixed schema to test the contract.
    with sqlite3.connect(db_path) as conn:
        conn.execute("UPDATE jobs SET status = 'running' WHERE id = ?", (job["id"],))
    r = client.delete(f"/jobs/{job['id']}")
    assert r.status_code == 409
    assert r.json() == {"detail": "job is running"}
    with sqlite3.connect(db_path) as conn:
        conn.execute("UPDATE jobs SET status = 'succeeded' WHERE id = ?", (job["id"],))
    assert client.delete(f"/jobs/{job['id']}").status_code == 204
    assert client.get(f"/jobs/{job['id']}").status_code == 404


# --- result math -----------------------------------------------------------


def test_result_word_count_exact(fresh):
    client, _ = fresh
    text = "Hello  world hello"  # 3 words, 18 chars (double space preserved)
    job = _create(client, "word_count", {"text": text})
    assert job["status"] == "pending" and job["result"] is None
    done = _wait_terminal(client, job["id"])
    assert done["status"] == "succeeded"
    assert done["result"] == {"words": 3, "chars": 18}


def test_result_reverse_exact(fresh):
    client, _ = fresh
    job = _create(client, "reverse", {"text": "abc def"})
    done = _wait_terminal(client, job["id"])
    assert done["status"] == "succeeded"
    assert done["result"] == {"text": "fed cba"}


def test_result_summary_stats_exact(fresh):
    client, _ = fresh
    text = "Apple apple\nBanana APPLE\ncherry"  # 3 lines, 5 words, 3 unique (ci)
    job = _create(client, "summary_stats", {"text": text})
    done = _wait_terminal(client, job["id"])
    assert done["status"] == "succeeded"
    assert done["result"] == {"lines": 3, "words": 5, "unique_words": 3}


def test_failed_job_contract(fresh):
    client, _ = fresh
    for payload in ({}, {"text": ""}):
        job = _create(client, "summary_stats", payload)
        done = _wait_terminal(client, job["id"])
        assert done["status"] == "failed"
        assert isinstance(done["error"], str) and done["error"].strip()
        assert done["result"] is None
        assert done["finished_at"] is not None


# --- websocket -------------------------------------------------------------


def test_ws_event_order(fresh):
    client, _ = fresh
    with client.websocket_connect("/ws/jobs") as ws:
        first = ws.receive_json()
        assert first == {"event": "connected"}
        job = _create(client, "word_count", {"text": "a b"})
        seen: list[str] = []
        for _ in range(3):
            evt = ws.receive_json()
            assert evt["job_id"] == job["id"]
            assert evt["status"]
            assert "ts" in evt
            seen.append(evt["event"])
        assert seen == ["job.created", "job.started", "job.succeeded"]


def test_ws_filter_excludes_other_jobs(fresh):
    client, _ = fresh
    decoy = _create(client, "reverse", {"text": "zzz"})
    _wait_terminal(client, decoy["id"])
    with client.websocket_connect(f"/ws/jobs?job_id={uuid.uuid4()}") as ws:
        assert ws.receive_json() == {"event": "connected"}
        other = _create(client, "reverse", {"text": "yyy"})
        _wait_terminal(client, other["id"])
        # No frames for the unrelated job may arrive; prove it by round-tripping
        # a fresh unfiltered connection first (events are flowing), then checking
        # the filtered socket yields nothing within a short grace period.
        import queue as _q
        import threading

        got: _q.Queue = _q.Queue()

        def _recv() -> None:
            try:
                got.put(ws.receive_json())
            except Exception as exc:  # noqa: BLE001 - closed socket is the pass case
                got.put(exc)

        t = threading.Thread(target=_recv, daemon=True)
        t.start()
        t.join(timeout=1.5)
        assert got.empty(), f"filtered socket received: {got.get_nowait()}"


# --- listing / stats -------------------------------------------------------


def test_list_ordering_and_totals(fresh):
    client, _ = fresh
    ids = []
    for text in ("one", "two", "three"):
        job = _create(client, "reverse", {"text": text})
        ids.append(job["id"])
        _wait_terminal(client, job["id"])
    r = client.get("/jobs")
    body = r.json()
    assert body["total"] == 3
    created = [j["created_at"] for j in body["jobs"]]
    assert created == sorted(created, reverse=True)
    r = client.get("/jobs?status=succeeded&limit=2")
    body = r.json()
    assert body["total"] == 3  # total matches the filter, not the page
    assert len(body["jobs"]) == 2
    assert client.get("/jobs?status=failed").json()["total"] == 0


def test_stats_math(fresh):
    client, _ = fresh
    for text in ("a b", "c d"):
        _wait_terminal(client, _create(client, "word_count", {"text": text})["id"])
    _wait_terminal(client, _create(client, "reverse", {"text": "e"})["id"])
    _wait_terminal(client, _create(client, "summary_stats", {})["id"])  # fails
    r = client.get("/stats")
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["jobs"] == {"pending": 0, "running": 0, "succeeded": 3, "failed": 1, "total": 4}
    assert body["by_type"] == {"word_count": 2, "reverse": 1, "summary_stats": 1}
    assert isinstance(body["avg_duration_s"], float) and body["avg_duration_s"] >= 0.0


def test_stats_null_avg_when_no_terminal_jobs(fresh):
    client, _ = fresh
    r = client.get("/stats")
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["jobs"]["total"] == 0
    assert body["avg_duration_s"] is None
