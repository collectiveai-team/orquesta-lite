#!/usr/bin/env python3
"""Extract per-agent-invocation telemetry from all orquesta-lite runs under ~/Projects."""
from __future__ import annotations

import json
import os
import re
import sys
from pathlib import Path

HOME = Path.home()
ROOT = HOME / "Projects"
OUT = Path(sys.argv[1] if len(sys.argv) > 1 else "invocations.jsonl")

# ---------- helpers ----------

READ_CMD_RE = re.compile(
    r"\b(cat|sed|head|tail|less|bat|rg|grep|ag|ack|find|fd|ls|tree|wc|awk|jq|nl|diff|git\s+(?:diff|show|log|grep|ls-files|status))\b"
)
SEARCH_CMD_RE = re.compile(r"\b(rg|grep|ag|ack|find|fd|git\s+grep|git\s+ls-files)\b")
TEST_CMD_RE = re.compile(r"\b(pytest|npm\s+test|npm\s+run\s+test|vitest|jest|go\s+test|ruff|eslint|tsc|mypy|pyright|orq-test|orq-lint|uv\s+run)\b")
SKILL_PATH_RE = re.compile(r"(?:\.claude|\.agents|\.codex|\.superpowers)/skills/([A-Za-z0-9._@-]+)")
SKILL_MD_RE = re.compile(r"skills/([A-Za-z0-9._@-]+)/SKILL\.md")


def classify_cmd(cmd: str) -> str:
    c = cmd.strip()
    if TEST_CMD_RE.search(c):
        return "gate/test"
    if SEARCH_CMD_RE.search(c):
        return "search"
    if READ_CMD_RE.search(c):
        return "read"
    return "other"


def norm_path(p: str, cwd: str) -> str:
    if not p:
        return ""
    p = p.strip().strip("'\"")
    if cwd and p.startswith(cwd):
        p = p[len(cwd) :].lstrip("/")
    p = p.replace(str(HOME), "~")
    return p


# ---------- per-format parsers ----------


def parse_claude(path: Path, cwd_hint: str) -> dict:
    r = new_rec()
    cwd = cwd_hint
    for line in path.open(errors="replace"):
        line = line.strip()
        if not line or not line.startswith("{"):
            continue
        try:
            d = json.loads(line)
        except Exception:
            continue
        t = d.get("type")
        if t == "system" and d.get("subtype") == "init":
            cwd = d.get("cwd") or cwd
            r["cwd"] = cwd
            r["n_tools_available"] = len(d.get("tools") or [])
            r["n_mcp_servers"] = len(d.get("mcp_servers") or [])
            r["permission_mode"] = d.get("permissionMode")
        elif t == "assistant":
            for c in d.get("message", {}).get("content", []) or []:
                if c.get("type") != "tool_use":
                    continue
                name = c.get("name") or "?"
                inp = c.get("input") or {}
                r["tools"][name] = r["tools"].get(name, 0) + 1
                if name in ("Read", "NotebookRead"):
                    fp = norm_path(str(inp.get("file_path", "")), cwd)
                    if fp:
                        r["files_read"].append(fp)
                elif name in ("Edit", "Write", "NotebookEdit", "MultiEdit"):
                    fp = norm_path(str(inp.get("file_path", "")), cwd)
                    if fp:
                        r["files_written"].append(fp)
                elif name == "Grep":
                    r["searches"].append(str(inp.get("pattern", ""))[:120])
                elif name == "Glob":
                    r["searches"].append("glob:" + str(inp.get("pattern", ""))[:120])
                elif name == "Bash":
                    cmd = str(inp.get("command", ""))
                    r["bash_kinds"][classify_cmd(cmd)] = r["bash_kinds"].get(classify_cmd(cmd), 0) + 1
                    r["bash_cmds"].append(cmd[:300])
                    for m in SKILL_MD_RE.finditer(cmd):
                        r["skills"].append(m.group(1))
                elif name == "Skill":
                    r["skills"].append(str(inp.get("skill", "?")))
                elif name in ("Task", "Agent"):
                    r["subagents"].append(str(inp.get("subagent_type", "?")))
                elif name == "Workflow":
                    r["workflows"] += 1
        elif t == "result":
            u = d.get("usage") or {}
            r["input_tokens"] = u.get("input_tokens", 0) or 0
            r["output_tokens"] = u.get("output_tokens", 0) or 0
            r["cache_read"] = u.get("cache_read_input_tokens", 0) or 0
            r["cache_write"] = u.get("cache_creation_input_tokens", 0) or 0
            r["cost_usd"] = d.get("total_cost_usd") or 0.0
            r["num_turns"] = d.get("num_turns") or 0
            r["is_error"] = bool(d.get("is_error"))
            r["stop_reason"] = d.get("stop_reason") or d.get("subtype")
            r["terminal_reason"] = d.get("terminal_reason")
            r["result_text_len"] = len(d.get("result") or "")
            r["duration_ms"] = d.get("duration_ms") or 0
            r["permission_denials"] = len(d.get("permission_denials") or [])
        elif t == "rate_limit_event":
            r["rate_limit_events"] += 1
    return r


def parse_codex(path: Path, cwd_hint: str) -> dict:
    r = new_rec()
    cwd = cwd_hint
    r["cwd"] = cwd
    for line in path.open(errors="replace"):
        line = line.strip()
        if not line or not line.startswith("{"):
            continue
        try:
            d = json.loads(line)
        except Exception:
            continue
        t = d.get("type")
        if t == "item.completed":
            it = d.get("item") or {}
            it_t = it.get("type")
            if it_t == "command_execution":
                cmd = str(it.get("command", ""))
                # strip the /bin/zsh -lc " wrapper
                inner = re.sub(r'^/bin/\w+\s+-\w+\s+"?', "", cmd).rstrip('"')
                kind = classify_cmd(inner)
                r["tools"]["Bash"] = r["tools"].get("Bash", 0) + 1
                r["bash_kinds"][kind] = r["bash_kinds"].get(kind, 0) + 1
                r["bash_cmds"].append(inner[:300])
                for m in SKILL_MD_RE.finditer(inner):
                    r["skills"].append(m.group(1))
                if kind == "search":
                    r["searches"].append(inner[:120])
                elif kind == "read":
                    fm = re.findall(r"[\w./~@-]+\.(?:py|ts|tsx|js|jsx|go|md|json|yaml|yml|toml|sh|sql|css|html)", inner)
                    for f in fm[:4]:
                        r["files_read"].append(norm_path(f, cwd))
            elif it_t == "file_change":
                for ch in it.get("changes", []) or []:
                    fp = norm_path(str(ch.get("path", "")), cwd)
                    if fp:
                        r["files_written"].append(fp)
                if not it.get("changes"):
                    r["tools"]["Edit"] = r["tools"].get("Edit", 0) + 1
                r["tools"]["Edit"] = r["tools"].get("Edit", 0) + 1
            elif it_t == "agent_message":
                r["num_turns"] += 1
                r["result_text_len"] = len(it.get("text") or "")
        elif t == "turn.completed":
            u = d.get("usage") or {}
            r["input_tokens"] = (u.get("input_tokens", 0) or 0) - (u.get("cached_input_tokens", 0) or 0)
            r["cache_read"] = u.get("cached_input_tokens", 0) or 0
            r["output_tokens"] = (u.get("output_tokens", 0) or 0) + (u.get("reasoning_output_tokens", 0) or 0)
            r["reasoning_tokens"] = u.get("reasoning_output_tokens", 0) or 0
    return r


def parse_opencode(path: Path, cwd_hint: str) -> dict:
    r = new_rec()
    cwd = cwd_hint
    r["cwd"] = cwd
    for line in path.open(errors="replace"):
        line = line.strip()
        if not line or not line.startswith("{"):
            continue
        try:
            d = json.loads(line)
        except Exception:
            continue
        t = d.get("type")
        part = d.get("part") or {}
        if t == "step_finish":
            tk = part.get("tokens") or {}
            cache = tk.get("cache") or {}
            r["input_tokens"] += tk.get("input", 0) or 0
            r["output_tokens"] += (tk.get("output", 0) or 0) + (tk.get("reasoning", 0) or 0)
            r["reasoning_tokens"] += tk.get("reasoning", 0) or 0
            r["cache_read"] += cache.get("read", 0) or 0
            r["cache_write"] += cache.get("write", 0) or 0
            r["cost_usd"] += part.get("cost", 0) or 0
            r["num_turns"] += 1
        elif t in ("tool", "tool_use", "part_updated", "message_part_updated"):
            if part.get("type") == "tool" or t == "tool":
                name = part.get("tool") or part.get("name") or "?"
                state = part.get("state") or {}
                if state.get("status") not in (None, "completed"):
                    continue
                key = (name, json.dumps(state.get("input"), sort_keys=True)[:200])
                if key in r["_seen"]:
                    continue
                r["_seen"].add(key)
                inp = state.get("input") or {}
                r["tools"][name] = r["tools"].get(name, 0) + 1
                lname = name.lower()
                if lname in ("read", "readfile"):
                    fp = norm_path(str(inp.get("filePath") or inp.get("file_path") or ""), cwd)
                    if fp:
                        r["files_read"].append(fp)
                elif lname in ("edit", "write", "patch", "multiedit"):
                    fp = norm_path(str(inp.get("filePath") or inp.get("file_path") or ""), cwd)
                    if fp:
                        r["files_written"].append(fp)
                elif lname in ("grep", "glob"):
                    r["searches"].append(str(inp.get("pattern", ""))[:120])
                elif lname == "bash":
                    cmd = str(inp.get("command", ""))
                    r["bash_kinds"][classify_cmd(cmd)] = r["bash_kinds"].get(classify_cmd(cmd), 0) + 1
                    r["bash_cmds"].append(cmd[:300])
                    for m in SKILL_MD_RE.finditer(cmd):
                        r["skills"].append(m.group(1))
                elif lname in ("task", "agent"):
                    r["subagents"].append(str(inp.get("subagent_type", "?")))
        elif t == "text" and part.get("type") == "text":
            r["result_text_len"] = max(r["result_text_len"], len(part.get("text") or ""))
    return r


def new_rec() -> dict:
    return {
        "cwd": "",
        "tools": {},
        "files_read": [],
        "files_written": [],
        "searches": [],
        "bash_kinds": {},
        "bash_cmds": [],
        "skills": [],
        "subagents": [],
        "workflows": 0,
        "input_tokens": 0,
        "output_tokens": 0,
        "reasoning_tokens": 0,
        "cache_read": 0,
        "cache_write": 0,
        "cost_usd": 0.0,
        "num_turns": 0,
        "is_error": False,
        "stop_reason": None,
        "terminal_reason": None,
        "result_text_len": 0,
        "duration_ms": 0,
        "rate_limit_events": 0,
        "permission_denials": 0,
        "n_tools_available": 0,
        "n_mcp_servers": 0,
        "permission_mode": None,
        "_seen": set(),
    }


# ---------- walk ----------

ROLE_RE = re.compile(r"^([a-z_0-9]+)\.c(\d+)\.a(\d+)(?:\.r(\d+))?$")

rows = []
for meta_path in ROOT.rglob(".orquestalite/runs/*/agents/*/*/meta.json"):
    inv_dir = meta_path.parent
    activity_dir = inv_dir.parent
    run_dir = activity_dir.parent.parent
    orq_dir = run_dir.parent.parent
    project_dir = orq_dir.parent
    try:
        meta = json.loads(meta_path.read_text())
    except Exception:
        continue

    provider = meta.get("provider", "?")
    stdout = inv_dir / "stdout.log"
    if not stdout.exists():
        continue

    cwd_hint = str(project_dir)
    try:
        if provider == "claude":
            r = parse_claude(stdout, cwd_hint)
        elif provider == "codex":
            r = parse_codex(stdout, cwd_hint)
        else:
            r = parse_opencode(stdout, cwd_hint)
    except Exception as e:
        r = new_rec()
        r["parse_error"] = repr(e)

    r.pop("_seen", None)

    m = ROLE_RE.match(inv_dir.name)
    role = m.group(1) if m else inv_dir.name.split(".")[0]
    cycle = int(m.group(2)) if m else -1
    attempt = int(m.group(3)) if m else -1
    retry = int(m.group(4)) if (m and m.group(4)) else 0

    prompt_p = inv_dir / "prompt.md"
    prompt_bytes = prompt_p.stat().st_size if prompt_p.exists() else 0

    run_id = run_dir.name
    ts = run_id[1:17] if run_id.startswith("r") else ""

    rows.append(
        {
            "project": str(project_dir).replace(str(HOME) + "/Projects/", ""),
            "run_id": run_id,
            "run_ts": ts,
            "activity": activity_dir.name,
            "invocation": inv_dir.name,
            "role": role,
            "cycle": cycle,
            "attempt": attempt,
            "retry": retry,
            "agent_alias": meta.get("agent"),
            "provider": provider,
            "model": meta.get("model"),
            "session_id": meta.get("session_id"),
            "duration_s": meta.get("duration_s", 0),
            "exit_code": meta.get("exit_code"),
            "prompt_bytes": prompt_bytes,
            "stdout_bytes": stdout.stat().st_size,
            "path": str(inv_dir).replace(str(HOME), "~"),
            **r,
        }
    )

with OUT.open("w") as f:
    for row in rows:
        f.write(json.dumps(row) + "\n")

print(f"wrote {len(rows)} invocations to {OUT}")
errs = [r for r in rows if r.get("parse_error")]
print(f"parse errors: {len(errs)}")
for e in errs[:5]:
    print("  ", e["path"], e["parse_error"])
