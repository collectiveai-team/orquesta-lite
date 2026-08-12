#!/usr/bin/env python3
"""Do the four integrated-review reviewers find DIFFERENT defects, or duplicate each other?"""
import json, collections, re, itertools

V = [json.loads(l) for l in open("verdicts.jsonl")]

STOP = set("""the a an and or of to in for with that this it is are was were be been being on at by from as not no
into than then them they their there these those which who whom whose what when where why how all any both each few
more most other some such only own same so too very can will just should now does did doing done has have had having
if but because until while about against between through during before after above below up down out off over under
again further once here why any i you he she we us our your its his her my me""".split())


def toks(s):
    return {w.lower() for w in re.findall(r"[A-Za-z_][A-Za-z_0-9]{3,}", s or "") if w.lower() not in STOP}


def sim(a, b):
    ta, tb = toks(a), toks(b)
    if not ta or not tb:
        return 0.0
    return len(ta & tb) / len(ta | tb)


ROLES = ["qa", "critic", "adversary", "gov_reviewer"]
runs = collections.defaultdict(lambda: collections.defaultdict(list))
for v in V:
    if v["role"] in ROLES and v["verdict"] and v["verdict"].get("findings"):
        runs[(v["project"], v["run_id"])][v["role"]].extend(
            [f for f in v["verdict"]["findings"] if isinstance(f, str)]
        )

print("=" * 104)
print("INTEGRATED REVIEW: pairwise finding overlap per run (Jaccard > 0.30 counted as duplicate)")
print("=" * 104)
pair_tot = collections.Counter()
pair_dup = collections.Counter()
uniq_by_role = collections.Counter()
tot_by_role = collections.Counter()

for key, byrole in sorted(runs.items()):
    present = [r for r in ROLES if byrole.get(r)]
    if len(present) < 2:
        continue
    print(f"\n{key[1]}  ({key[0]})")
    for r in present:
        print(f"   {r:<13} {len(byrole[r])} findings")
    for a, b in itertools.combinations(present, 2):
        dups = 0
        for fa in byrole[a]:
            if any(sim(fa, fb) > 0.30 for fb in byrole[b]):
                dups += 1
        n = len(byrole[a])
        pair_tot[(a, b)] += n
        pair_dup[(a, b)] += dups
        if n:
            print(f"     {a:>12} -> {b:<13} {dups}/{n} of {a}'s findings duplicated in {b}  ({100*dups/n:.0f}%)")
    # uniqueness: a finding no other role reported
    for r in present:
        others = [f for o in present if o != r for f in byrole[o]]
        for fa in byrole[r]:
            tot_by_role[r] += 1
            if not any(sim(fa, fb) > 0.30 for fb in others):
                uniq_by_role[r] += 1

print("\n" + "=" * 104)
print("AGGREGATE pairwise duplication")
print("=" * 104)
for (a, b), n in sorted(pair_tot.items(), key=lambda kv: -kv[1]):
    d = pair_dup[(a, b)]
    print(f"  {a:>13} -> {b:<14} {d:>4}/{n:<4} ({100*d/n:>3.0f}%) of {a}'s findings also raised by {b}")

print("\n" + "=" * 104)
print("UNIQUE CONTRIBUTION per reviewer (findings no other reviewer in the same run raised)")
print("=" * 104)
cr = collections.Counter()
for v in V:
    if v["role"] in ROLES:
        cr[v["role"]] += v["cache_read"]
hdr = f"{'role':<14}{'findings':>10}{'unique':>8}{'uniq%':>7}{'cacheR_M':>10}{'M per unique':>14}"
print(hdr); print("-"*len(hdr))
for r in ROLES:
    if not tot_by_role[r]:
        continue
    u = uniq_by_role[r]
    print(f"{r:<14}{tot_by_role[r]:>10}{u:>8}{100*u/tot_by_role[r]:>7.0f}{cr[r]/1e6:>10.1f}"
          f"{(cr[r]/1e6/u) if u else float('nan'):>14.2f}")
