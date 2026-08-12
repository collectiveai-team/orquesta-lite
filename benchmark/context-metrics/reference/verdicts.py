#!/usr/bin/env python3
"""Extract each invocation's own structured verdict (the JSON it wrote to results/<role>.json)
directly from its stdout stream, so verdicts are correctly scoped per run+iteration."""
import json, collections, os, pathlib, re

HOME = pathlib.Path.home()
rows = [json.loads(l) for l in open("invocations.jsonl")]
runm = {(o["project"], o["run_id"]): o for o in (json.loads(l) for l in open("runs.jsonl"))}
T = [r for r in rows if not runm[(r["project"], r["run_id"])]["leak"] and runm[(r["project"], r["run_id"])]["run_ts"] >= "20260724"]

RESULT_RE = re.compile(r"\.orquestalite/results/[a-z_]+\.json")


def extract_verdict(r):
    """Return the last JSON object the agent wrote to its result path."""
    p = pathlib.Path(r["path"].replace("~", str(HOME))) / "stdout.log"
    if not p.exists():
        return None
    best = None
    for line in p.open(errors="replace"):
        if "results/" not in line:
            continue
        try:
            d = json.loads(line)
        except Exception:
            continue
        t = d.get("type")
        cands = []
        if t == "assistant":
            for c in d.get("message", {}).get("content", []) or []:
                if c.get("type") == "tool_use":
                    inp = c.get("input") or {}
                    fp = str(inp.get("file_path", ""))
                    if RESULT_RE.search(fp):
                        cands.append(inp.get("content") or inp.get("new_string") or "")
                    # bash heredoc writes
                    elif c.get("name") == "Bash":
                        cmd = str(inp.get("command", ""))
                        if RESULT_RE.search(cmd):
                            cands.append(cmd)
        elif t == "item.completed":  # codex
            it = d.get("item") or {}
            if it.get("type") == "command_execution":
                cmd = str(it.get("command", ""))
                if RESULT_RE.search(cmd):
                    cands.append(cmd)
        else:  # opencode
            part = d.get("part") or {}
            st = part.get("state") or {}
            inp = st.get("input") or {}
            blob = json.dumps(inp)
            if RESULT_RE.search(blob):
                cands.append(inp.get("content") or inp.get("command") or "")
        for c in cands:
            for m in re.finditer(r"\{.*\}", c, re.S):
                try:
                    j = json.loads(m.group(0))
                except Exception:
                    continue
                if isinstance(j, dict) and (
                    "approved" in j or "complete" in j or "continue" in j or "status" in j
                ):
                    best = j
    return best


ITER = re.compile(r"\[while-(\d+)\]")
out = []
for r in T:
    v = extract_verdict(r)
    m = ITER.search(r["activity"])
    out.append(
        {
            **{k: r[k] for k in ("project", "run_id", "run_ts", "activity", "invocation", "role", "retry",
                                  "cache_read", "output_tokens", "duration_s", "provider", "model", "path")},
            "iter": int(m.group(1)) if m else None,
            "verdict": v,
        }
    )

with open("verdicts.jsonl", "w") as f:
    for o in out:
        f.write(json.dumps(o) + "\n")

got = sum(1 for o in out if o["verdict"])
print(f"extracted verdicts for {got}/{len(out)} trusted invocations")
byrole = collections.Counter()
for o in out:
    if o["verdict"]:
        byrole[o["role"]] += 1
print("by role:", dict(byrole))

# approval stats for reviewer roles
print("\n" + "=" * 100)
print("REVIEWER VERDICTS: approval rate and finding counts")
print("=" * 100)
agg = collections.defaultdict(lambda: dict(n=0, approved=0, findings=0, zero_find=0, cr=0))
for o in out:
    v = o["verdict"]
    if not v or "approved" not in v:
        continue
    a = agg[o["role"]]
    a["n"] += 1
    a["approved"] += 1 if v.get("approved") else 0
    f = v.get("findings") or []
    a["findings"] += len(f)
    a["zero_find"] += 1 if len(f) == 0 else 0
    a["cr"] += o["cache_read"]
hdr = f"{'role':<15}{'n':>4}{'approved':>9}{'appr%':>7}{'findings':>10}{'find/inv':>9}{'0-find':>8}{'cacheR_M':>10}{'M/finding':>11}"
print(hdr); print("-"*len(hdr))
for role, a in sorted(agg.items(), key=lambda kv: -kv[1]["cr"]):
    print(f"{role:<15}{a['n']:>4}{a['approved']:>9}{100*a['approved']/a['n']:>7.0f}{a['findings']:>10}"
          f"{a['findings']/a['n']:>9.1f}{a['zero_find']:>8}{a['cr']/1e6:>10.1f}"
          f"{(a['cr']/1e6/a['findings']) if a['findings'] else float('nan'):>11.2f}")
