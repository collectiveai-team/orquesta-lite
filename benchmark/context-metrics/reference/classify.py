#!/usr/bin/env python3
"""Classify runs as session-scope-clean vs affected by the session-reuse bug."""
import json, collections, sys

rows = [json.loads(l) for l in open("invocations.jsonl")]
runs = collections.defaultdict(list)
for r in rows:
    runs[(r["project"], r["run_id"])].append(r)

out = []
for (proj, run), rs in runs.items():
    scoped = any("[while-" in r["activity"] or "[foreach-" in r["activity"] for r in rs)
    sess = collections.defaultdict(list)
    for r in rs:
        if r["session_id"]:
            sess[r["session_id"]].append(r)
    # a session that spans >=3 invocations with distinct rerun indices in a flat dir
    worst_span = 0
    worst = None
    for sid, g in sess.items():
        retries = {x["retry"] for x in g}
        if len(g) >= 2 and len(retries) >= 2:
            if len(g) > worst_span:
                worst_span, worst = len(g), (sid, g)
    leak = worst_span >= 3 and not scoped

    # cache_read growth ratio for the coder role (the bug's amplifier)
    growth = None
    if worst:
        g = sorted(worst[1], key=lambda x: x["retry"])
        vals = [x["cache_read"] for x in g if x["cache_read"] > 0]
        if len(vals) >= 3 and vals[0] > 0:
            growth = round(max(vals) / vals[0], 1)

    tot_cache = sum(r["cache_read"] for r in rs)
    tot_out = sum(r["output_tokens"] for r in rs)
    tot_in = sum(r["input_tokens"] for r in rs)
    cost = sum(r["cost_usd"] for r in rs)
    out.append(
        {
            "project": proj,
            "run_id": run,
            "run_ts": rs[0]["run_ts"],
            "n_inv": len(rs),
            "n_sess": len(sess),
            "scoped": scoped,
            "leak": leak,
            "worst_span": worst_span,
            "growth": growth,
            "cache_read": tot_cache,
            "input_tokens": tot_in,
            "output_tokens": tot_out,
            "cost_usd": round(cost, 2),
            "roles": sorted({r["role"] for r in rs}),
        }
    )

out.sort(key=lambda x: x["run_ts"])
with open("runs.jsonl", "w") as f:
    for o in out:
        f.write(json.dumps(o) + "\n")

hdr = f"{'run_ts':<17}{'inv':>4}{'sess':>5}{'scoped':>7}{'LEAK':>6}{'span':>5}{'growth':>7}{'cacheR_M':>10}{'out_k':>7}  project"
print(hdr)
print("-" * len(hdr))
for o in out:
    print(
        f"{o['run_ts']:<17}{o['n_inv']:>4}{o['n_sess']:>5}{str(o['scoped']):>7}"
        f"{'YES' if o['leak'] else '-':>6}{o['worst_span']:>5}"
        f"{(str(o['growth'])+'x') if o['growth'] else '-':>7}"
        f"{o['cache_read']/1e6:>10.1f}{o['output_tokens']/1e3:>7.0f}  {o['project']}"
    )

nl = [o for o in out if o["leak"]]
ok = [o for o in out if not o["leak"]]
print(f"\nLEAKY runs: {len(nl)}   CLEAN runs: {len(ok)}")
print(f"tokens in leaky runs: cacheR {sum(o['cache_read'] for o in nl)/1e6:.0f}M")
print(f"tokens in clean runs: cacheR {sum(o['cache_read'] for o in ok)/1e6:.0f}M")
