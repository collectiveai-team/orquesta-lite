#!/usr/bin/env python3
"""Orientation cost: how much of each invocation is spent locating things vs changing them."""
import json, collections, os, pathlib, re, statistics as st

HOME = pathlib.Path.home()
rows = [json.loads(l) for l in open("invocations.jsonl")]
runm = {(o["project"], o["run_id"]): o for o in (json.loads(l) for l in open("runs.jsonl"))}
T = [r for r in rows if not runm[(r["project"], r["run_id"])]["leak"] and runm[(r["project"], r["run_id"])]["run_ts"] >= "20260724"]

MUT = {"edit", "write", "notebookedit", "multiedit", "patch", "apply_patch"}
DISC = {"read", "grep", "glob", "notebookread"}


def walk(r):
    """Replay the tool sequence; return (n_calls_before_first_mutation, total_calls,
    discovery_calls, mutation_calls, bytes_of_tool_output_before_first_mutation)."""
    p = pathlib.Path(r["path"].replace("~", str(HOME))) / "stdout.log"
    if not p.exists():
        return None
    seq = []
    obytes = collections.Counter()
    pending = {}
    for line in p.open(errors="replace"):
        if not line.startswith("{"):
            continue
        try:
            d = json.loads(line)
        except Exception:
            continue
        t = d.get("type")
        if t == "assistant":
            for c in d.get("message", {}).get("content", []) or []:
                if c.get("type") == "tool_use":
                    nm = (c.get("name") or "").lower()
                    inp = c.get("input") or {}
                    kind = "mut" if nm in MUT else ("disc" if nm in DISC else None)
                    if nm == "bash":
                        cmd = str(inp.get("command", ""))
                        if re.search(r"\b(rg|grep|find|fd|cat|sed|head|tail|ls|tree|nl|awk|git\s+(grep|ls-files|show|diff|log))\b", cmd):
                            kind = "disc"
                        elif re.search(r"\b(pytest|npm|ruff|eslint|tsc|vitest|jest|go\s+test|uv\s+run|pnpm)\b", cmd):
                            kind = "gate"
                        else:
                            kind = "other"
                    seq.append((kind, c.get("id")))
                    pending[c.get("id")] = kind
        elif t == "user":
            for c in d.get("message", {}).get("content", []) or []:
                if c.get("type") == "tool_result":
                    k = pending.get(c.get("tool_use_id"))
                    cont = c.get("content")
                    n = len(json.dumps(cont)) if cont is not None else 0
                    obytes[k] += n
        elif t == "item.completed":  # codex
            it = d.get("item") or {}
            if it.get("type") == "command_execution":
                cmd = str(it.get("command", ""))
                if re.search(r"\b(rg|grep|find|fd|cat|sed|head|tail|ls|tree|nl|awk|git\s+(grep|ls-files|show|diff|log))\b", cmd):
                    kind = "disc"
                elif re.search(r"\b(pytest|npm|ruff|eslint|tsc|vitest|jest|go\s+test|uv\s+run|pnpm)\b", cmd):
                    kind = "gate"
                else:
                    kind = "other"
                seq.append((kind, None))
                obytes[kind] += len(str(it.get("aggregated_output") or ""))
            elif it.get("type") == "file_change":
                seq.append(("mut", None))
    if not seq:
        return None
    first_mut = next((i for i, (k, _) in enumerate(seq) if k == "mut"), None)
    return dict(
        before=first_mut if first_mut is not None else len(seq),
        total=len(seq),
        disc=sum(1 for k, _ in seq if k == "disc"),
        mut=sum(1 for k, _ in seq if k == "mut"),
        gate=sum(1 for k, _ in seq if k == "gate"),
        disc_bytes=obytes["disc"],
        gate_bytes=obytes["gate"],
        all_bytes=sum(obytes.values()),
    )


print("=" * 108)
print("ORIENTATION COST — tool calls spent locating code before the first file change")
print("=" * 108)
agg = collections.defaultdict(list)
for r in T:
    w = walk(r)
    if w:
        agg[r["role"]].append((r, w))

hdr = f"{'role':<15}{'n':>4}{'med_disc':>9}{'med_mut':>8}{'disc:mut':>9}{'med_before':>11}{'disc_MB':>9}{'%out_disc':>10}"
print(hdr); print("-" * len(hdr))
tot_disc_bytes = tot_bytes = 0
for role, g in sorted(agg.items(), key=lambda kv: -len(kv[1])):
    ds = [w["disc"] for _, w in g]
    ms = [w["mut"] for _, w in g]
    db = sum(w["disc_bytes"] for _, w in g)
    ab = sum(w["all_bytes"] for _, w in g)
    tot_disc_bytes += db; tot_bytes += ab
    ratio = (sum(ds) / sum(ms)) if sum(ms) else float("inf")
    print(f"{role:<15}{len(g):>4}{st.median(ds):>9.0f}{st.median(ms):>8.0f}{ratio:>9.1f}"
          f"{st.median([w['before'] for _,w in g]):>11.0f}{db/1e6:>9.1f}{100*db/max(ab,1):>10.0f}")
print(f"\nTOTAL tool-result bytes fed back into context: {tot_bytes/1e6:.0f} MB")
print(f"  of which repo discovery (read/grep/cat/sed/find): {tot_disc_bytes/1e6:.0f} MB ({100*tot_disc_bytes/max(tot_bytes,1):.0f}%)")
print(f"  ~= {tot_disc_bytes/4/1e6:.1f}M tokens of raw discovery output, re-derived every invocation")

# ---- what do they search for, repeatedly? ----
print("\n" + "=" * 108)
print("RECURRING SEARCHES — what a repo-knowledge index would have answered once")
print("=" * 108)
pat = collections.Counter()
for r in T:
    for s in r["searches"]:
        s = re.sub(r"\s+", " ", s).strip()
        pat[s[:90]] += 1
    for c in r["bash_cmds"]:
        m = re.search(r"\b(?:rg|grep|git grep)\b[^|;]*", c)
        if m:
            frag = re.sub(r"\s+", " ", m.group(0))[:90]
            pat[frag] += 1
for s, n in pat.most_common(30):
    if n < 3:
        break
    print(f"  {n:>4}x  {s}")

# ---- file-level: how concentrated is attention? ----
print("\n" + "=" * 108)
print("ATTENTION CONCENTRATION — do agents read the same core files every time?")
print("=" * 108)
for role in ("coder", "ticket_qa", "ticket_planner"):
    c = collections.Counter()
    n = 0
    for r in T:
        if r["role"] != role:
            continue
        n += 1
        for f in set(r["files_read"]):
            c[f] += 1
    if not c:
        continue
    top = c.most_common(8)
    print(f"\n{role} ({n} invocations) — top files and the share of invocations that read them:")
    for f, k in top:
        print(f"   {100*k/n:>5.0f}%  ({k:>3}/{n})  {f}")
