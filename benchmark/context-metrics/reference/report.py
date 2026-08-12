#!/usr/bin/env python3
"""Aggregate all experiment arms across reps."""
from __future__ import annotations

import collections
import json
import pathlib
import re
import statistics as st

EXP = pathlib.Path(__file__).parent
RESULTS = EXP / "results"

DISC_BASH = re.compile(r"\b(rg|grep|find|fd|cat|sed|head|tail|ls|tree|nl|awk|wc|git\s+(grep|ls-files|show|diff|log))\b")
GATE_BASH = re.compile(r"\b(pytest|ruff|uv\s+run|npm|eslint|tsc|vitest|jest)\b")
MUT = {"edit", "write", "notebookedit", "multiedit"}
DISC = {"read", "grep", "glob", "notebookread"}


def parse_stream(run_dir: pathlib.Path) -> dict | None:
    logs = list(run_dir.rglob("stdout.log"))
    if not logs:
        return None
    log = logs[0]
    prompt = log.parent / "prompt.md"
    m = dict(prompt_bytes=prompt.stat().st_size if prompt.exists() else 0,
             disc=0, mut=0, gate=0, other=0, disc_bytes=0, all_bytes=0,
             reads=0, files_read=[], cache_read=0, output_tokens=0, cache_write=0,
             cost_usd=0.0, num_turns=0, unresolved=0)
    if prompt.exists():
        m["unresolved"] = prompt.read_text(errors="replace").count("{{")
    pend = {}
    for line in log.open(errors="replace"):
        if not line.startswith("{"):
            continue
        try:
            d = json.loads(line)
        except Exception:
            continue
        t = d.get("type")
        if t == "assistant":
            for c in d.get("message", {}).get("content", []) or []:
                if c.get("type") != "tool_use":
                    continue
                name = (c.get("name") or "").lower()
                inp = c.get("input") or {}
                kind = "other"
                if name in MUT:
                    kind = "mut"
                elif name in DISC:
                    kind = "disc"
                elif name == "bash":
                    cmd = str(inp.get("command", ""))
                    kind = "gate" if GATE_BASH.search(cmd) else ("disc" if DISC_BASH.search(cmd) else "other")
                m[kind] += 1
                if name == "read":
                    m["reads"] += 1
                    fp = str(inp.get("file_path", ""))
                    if fp:
                        m["files_read"].append(fp.split("/base/")[-1])
                pend[c.get("id")] = kind
        elif t == "user":
            for c in d.get("message", {}).get("content", []) or []:
                if c.get("type") != "tool_result":
                    continue
                k = pend.pop(c.get("tool_use_id"), None)
                n = len(json.dumps(c.get("content"))) if c.get("content") is not None else 0
                m["all_bytes"] += n
                if k == "disc":
                    m["disc_bytes"] += n
        elif t == "result":
            u = d.get("usage") or {}
            m["cache_read"] = u.get("cache_read_input_tokens", 0)
            m["cache_write"] = u.get("cache_creation_input_tokens", 0)
            m["output_tokens"] = u.get("output_tokens", 0)
            m["cost_usd"] = d.get("total_cost_usd", 0.0)
            m["num_turns"] = d.get("num_turns", 0)
    return m


def work_done(d: pathlib.Path) -> dict:
    diff = (d / "diff.stat").read_text() if (d / "diff.stat").exists() else ""
    tracked = int(mm.group(1)) if (mm := re.search(r"(\d+) insertion", diff)) else 0
    nf = (d / "newfiles.txt").read_text() if (d / "newfiles.txt").exists() else ""
    newlines = 0
    if "--- NEW FILE" in nf:
        newlines = len([l for l in nf.splitlines() if not l.startswith("--- NEW FILE")])
    files = len(re.findall(r"^ \S+\s+\|", diff, re.M)) + nf.count("--- NEW FILE")
    pyt = (d / "pytest.txt").read_text() if (d / "pytest.txt").exists() else ""
    npass = int(mm.group(1)) if (mm := re.search(r"(\d+) passed", pyt)) else 0
    return dict(lines=tracked + newlines, files=files, tests=npass)


rows = []
for d in sorted(RESULTS.iterdir()):
    if not d.is_dir() or not (d / "meta.json").exists():
        continue
    meta = json.loads((d / "meta.json").read_text())
    s = parse_stream(d / "runs")
    if not s:
        continue
    rows.append({**meta, **s, **work_done(d), "tag": d.name})

coder = [r for r in rows if r["arm"].startswith("A")]
qa = [r for r in rows if r["arm"].startswith("B")]

by = collections.defaultdict(list)
for r in coder:
    by[r["arm"]].append(r)

W = 122
print("=" * W)
print("EXPERIMENT A — one coder invocation, identical ticket, identical repo state, varying injected context")
print("=" * W)
h = (f"{'arm':<17}{'n':>2}{'promptB':>9}{'cacheR_k':>10}{'out_k':>7}{'cost$':>7}{'disc':>6}"
     f"{'discKB':>8}{'turns':>7}{'wall_s':>7}{'lines':>7}{'tests':>6}{'gates':>7}")
print(h); print("-" * len(h))


def med(g, k):
    return st.median([x[k] for x in g])


order = ["A0-baseline", "A1-conventions", "A2-memory", "A3-repomap", "A5-repomapv2", "A4-all"]
for arm in order:
    g = by.get(arm)
    if not g:
        continue
    gates = "ok" if all(x["ruff_exit"] == 0 and x["pytest_exit"] == 0 for x in g) else "FAIL"
    print(f"{arm:<17}{len(g):>2}{med(g,'prompt_bytes'):>9.0f}{med(g,'cache_read')/1e3:>10.0f}"
          f"{med(g,'output_tokens')/1e3:>7.1f}{med(g,'cost_usd'):>7.2f}{med(g,'disc'):>6.0f}"
          f"{med(g,'disc_bytes')/1e3:>8.0f}{med(g,'num_turns'):>7.0f}{med(g,'wall_s'):>7.0f}"
          f"{med(g,'lines'):>7.0f}{med(g,'tests'):>6.0f}{gates:>7}")

base = by.get("A0-baseline")
if base:
    print()
    print("=" * W)
    print("DELTA vs A0-baseline (medians). Negative = cheaper.")
    print("=" * W)
    h2 = f"{'arm':<17}{'cacheR':>9}{'output':>9}{'cost':>9}{'disc calls':>12}{'disc bytes':>12}{'turns':>8}{'wall':>8}{'lines':>8}"
    print(h2); print("-" * len(h2))
    for arm in order[1:]:
        g = by.get(arm)
        if not g:
            continue
        def p(k):
            b, a = med(base, k), med(g, k)
            return f"{100*(a-b)/b:+.0f}%" if b else "n/a"
        print(f"{arm:<17}{p('cache_read'):>9}{p('output_tokens'):>9}{p('cost_usd'):>9}"
              f"{p('disc'):>12}{p('disc_bytes'):>12}{p('num_turns'):>8}{p('wall_s'):>8}{p('lines'):>8}")

    print()
    print("=" * W)
    print("SPREAD within each arm (is the effect bigger than the noise?)")
    print("=" * W)
    h3 = f"{'arm':<17}{'n':>2}{'cacheR_k values':>34}{'min':>9}{'max':>9}{'spread':>9}"
    print(h3); print("-" * len(h3))
    for arm in order:
        g = by.get(arm)
        if not g:
            continue
        v = sorted(round(x["cache_read"] / 1e3) for x in g)
        lo, hi = min(v), max(v)
        print(f"{arm:<17}{len(g):>2}{str(v):>34}{lo:>9}{hi:>9}{100*(hi-lo)/max(lo,1):>8.0f}%")

    print()
    print("=" * W)
    print("WHY A BIGGER PROMPT COSTS MORE: cache_read tracks prompt size x turns")
    print("=" * W)
    print(f"{'arm':<17}{'promptB':>9}{'turns':>7}{'cacheR_k':>10}{'cacheR/turn_k':>15}{'implied prefix_k':>18}")
    print("-" * 76)
    for arm in order:
        g = by.get(arm)
        if not g:
            continue
        pt = med(g, "prompt_bytes"); tn = med(g, "num_turns"); cr = med(g, "cache_read")
        print(f"{arm:<17}{pt:>9.0f}{tn:>7.0f}{cr/1e3:>10.0f}{cr/max(tn,1)/1e3:>15.1f}{cr/max(tn,1)/1e3:>18.1f}")

if qa:
    print()
    print("=" * W)
    print("EXPERIMENT B — same diff under review, findings schema with vs without a severity field")
    print("=" * W)
    for r in sorted(qa, key=lambda x: x["arm"]):
        d = RESULTS / r["tag"]
        res = d / "qa.result.json"
        print(f"\n{r['arm']}  wall={r['wall_s']}s  cacheR={r['cache_read']/1e3:.0f}k  out={r['output_tokens']/1e3:.1f}k  cost=${r['cost_usd']:.2f}")
        if not res.exists():
            print("   (no result written)")
            continue
        j = json.loads(res.read_text())
        f = j.get("findings") or []
        print(f"   approved={j.get('approved')}  findings={len(f)}")
        if f and isinstance(f[0], dict):
            sev = collections.Counter(x.get("severity") for x in f)
            print(f"   severity breakdown: {dict(sev)}")
            for x in f:
                print(f"     [{x.get('severity'):>8}] {str(x.get('detail'))[:105]}")
        else:
            for x in f:
                print(f"     [{'?':>8}] {str(x)[:105]}")

json.dump(rows, (EXP / "all-measurements.json").open("w"), indent=1, default=str)
print(f"\n({len(rows)} runs measured; raw -> all-measurements.json)")
