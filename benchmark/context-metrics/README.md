# Context metrics

Measurement harness for the context-efficiency work.
Plan: [`docs/superpowers/plans/2026-08-05-context-efficiency-pass.md`](../../docs/superpowers/plans/2026-08-05-context-efficiency-pass.md)

## Status

**`reference/` is unported scratch material, not a supported tool.** It is the
exact code that produced `baseline-2026-08-05.json` and the measurements the plan
cites, preserved here so the plan is self-contained and its numbers are
reproducible. Task 0 of the plan is to port it into something maintained. Nothing
in `reference/` is wired into CI, and the hardcoded paths in it point at a
scratch directory that no longer exists.

## What was measured

One coder invocation on an identical new ticket against an identical repo state
(`git reset --hard` between arms), varying only the injected context. Base repo:
a 2,229-LOC Python service with green gates. 3 reps per arm except where noted.
19 invocations total, $18.00.

`baseline-2026-08-05.json` holds one row per invocation.

| arm | n | cache_read (k) | vs base | ranges overlap baseline? | turns | lines |
|---|---|---|---|---|---|---|
| A0 baseline | 3 | [1734–1975] | — | — | [27–30] | [112–117] |
| A1 `{{CONVENTIONS}}` | 1 | [2593] | +43% | — | [31] | 114 |
| A2 `{{MEMORY}}` | 3 | [1657–1823] | −7% | yes | [27–30] | 114 |
| A3 repo map, first generator | 3 | [1558–1645] | −11% | **no** | [24–29] | [129–136] |
| A5 repo map, ranked generator | 3 | [1452–1965] | −12% | yes | **[23–26] no overlap** | [112–129] |
| A4 all three | 2 | [1813–2110] | +8% | yes | [28–33] | [133–193] |

**Read the ranges, not the medians.** At n=3 a median difference inside two
overlapping ranges is not a result. The only non-overlapping signals measured
were A3's cache_read, A3's lines-produced, and A5's turn count.

Two arms not in the table reviewed the *same* diff under two `findings` schemas,
once on a correct implementation and once on a deliberately sabotaged one (an
existence check removed and the test covering it weakened, so both gates stay
green while an acceptance criterion is observably violated). On the sabotaged
diff the current schema produced 6 flat findings of which 3 were praise; the
severity schema produced exactly 2 `blocking` + 3 `note`.

## Files

| file | what it is |
|---|---|
| `baseline-2026-08-05.json` | one row per invocation; the frozen baseline |
| `reference/extract.py` | parses agent `stdout.log` across all three provider stream formats (claude `result.usage`, codex `turn.completed.usage`, opencode accumulated `step_finish.tokens`) |
| `reference/classify.py` | structural session-leak classifier — scope marker in the activity path plus session-to-invocation span, **not** dates. Self-test: `r20260727T110944Z-4f8d` must classify leaky (it postdates the fix but ran a stale binary) and `r20260804T174741Z-38d9` clean |
| `reference/verdicts.py` | extracts each invocation's own structured result from its stdout stream, correctly scoped per run+iteration — `results/by-task/` cannot be used for this, see plan Task 1 |
| `reference/overlap.py` | pairwise finding-overlap and unique-contribution per review role |
| `reference/orient.py` | discovery-vs-mutation tool ratios and recurring-search analysis |
| `reference/run-arm.sh` | runs one arm from a pristine repo state; runs the gates **from the harness**, never trusting the agent's self-report |
| `reference/report.py` | per-arm aggregation with the range-overlap flag |
| `reference/gen-arms.py`, `gen-flows.py` | generate the prompt variants and single-step probe flows so the only difference between arms is the injected block |
| `reference/repomap-reference.go.txt` | working repo-map generator: consumes `tree-sitter tags`, ranks by PageRank over the def/ref graph with common-name damping and test down-weighting. Plan Task 9b ports this. Kept as `.txt` so it is not compiled |
| `reference/ticket.json`, `memory-seed.md` | the fixtures used as the independent variable |

## Reproducing

Needs the `tree-sitter` CLI with grammars for the languages under test — see the
bootstrap procedure in
[`docs/superpowers/specs/2026-08-05-repo-map-bootstrap-draft.md`](../../docs/superpowers/specs/2026-08-05-repo-map-bootstrap-draft.md).

Known gaps in what was measured, carried into the plan as open questions:

- The harness coder (claude-sonnet-5) made 9 Bash calls per invocation; production
  claude coders make 28 and production codex coders make 8 **with zero Reads** —
  they shell everything. The repo map had less shell probing to displace than a
  real run would.
- The base repo has 41 source files. Ranking quality can only decide something
  when there are many files to choose among; the TypeScript repo tested for map
  *content* has 387 but has no coder arm yet.
- The A3→A5 comparison varies two things, not one: the ranked rewrite also dropped
  the directory-layout section. Plan Task 9b Step 5 reinstates it.
