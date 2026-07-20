# Round 2 — execution report (pre-scoring)

Status of the four Hookrelay runs executed 2026-07-18 against the
pre-registered spec and protocol in [`../round2/`](../round2/). This report
covers **execution outcomes, incidents, and the governance-loop A/B** — the
scored evaluation (frozen probe `ce697cf2…447b46`, acceptance checklist by
execution, standardized bug hunt, judges) has **not run yet**; nothing here is
a composite score.

## Conditions and outcomes

| | one-shot (superpowers) | ticketed-sonnet @flow-v2 | ticketed-qwen @flow-v2 | ticketed-qwen @flow-v3 (gov-loop) |
|---|---|---|---|---|
| Run | headless `claude -p`, Sonnet 5 | `r…124849Z-6bca` | `r…011033Z-650d` | `r…182812Z-d875` |
| Outcome | ✅ converged (no reviewer) | ✅ **governance approved, 1st try** | ❌ **governance VETO** — fail-closed death | ✅ **rejected → repaired → approved** |
| Tickets/features first-attempt | 6/6 features | 100% tickets | 100% tickets | 100% tickets |
| Own tests at end | 40 | 58 | 67 | 68 |
| Wall (gross) | 25m09s | 3h07m† | ~12h†† | ~4h†† |
| Wall (net productive, approx.) | 25m | ~1h15m | ~2h45m | ~2h30m |
| Cost | **$8.84 complete** | **≈$38 complete**‡ | floor (qwen unpriced) | **$52.44 floor** (claude roles) |

†Includes a 20-min coder-timeout attempt and a ~1h28m host-sleep gap.
††Gross wall heavily contaminated by documented incidents (below); net figures
to be recomputed precisely from `run.log` during scoring.
‡Cache-aware estimate from run.log tokens; the `orq-lite cost`/agtop rollup
($175) is broken for these runs — it prices all input as fresh Opus tokens and
ignores cache reads.

## Condition 1 — one-shot (Claude Code + superpowers, Sonnet 5)

Launched headless (`claude -p`, stream-json) with the round-1 prompt verbatim
— a **deviation from round 1**, which ran interactively; superpowers loaded
(SessionStart hook fired). Converged in **25m09s / $8.84 / 151 turns**: six
feature commits on `implement-hookrelay-features` @ `0d578a5`, gates
self-reported green (40 tests). Roughly 2× faster and ~40% cheaper than its
round-1 Taskflow equivalent ($15.15, ~1–2h) on a similar-sized spec.
Artifacts: `round2-oneshot-artifacts/` (full transcript incl. cost, git
bundle).

## Condition 2 — ticketed-sonnet (flow-v2, replica of round-1 v4 team)

Launched with the pack policy passed explicitly (round-1 qwen-leg lesson).
Every ticket approved first-attempt. Two incidents, both infra-class:

- **SSE-ticket coder timeout at 1200s** (13:31→13:51 continuous work) — see
  the cross-condition note below; after the symmetric timeout raise to 2400s
  the same ticket completed in **4m37s**.
- A ~1h28m host-sleep gap (14:08→15:42) during global QA; the run survived
  and resumed unaided.

**Governance approved on the first pass** (15:55Z): *"Full Hookrelay contract
observably met across all five features; both gates pass; no stubs or
integration gaps found at ticket boundaries."* 68/68 steps, 58 tests.

Cost ≈ **$37.99 complete** (cache-aware estimate): review roles ≈ $29.5
(**78%** — ticket_qa $14.06, planners $8.22, qa $4.03, governance $2.52,
critic $0.71) vs coder $7.69. The round-1 pattern — the harness's cost *is*
the review — replicates. Artifacts: `round2-ticketed-sonnet-artifacts/`.

## Condition 3 — ticketed-qwen (flow-v2): the fatal veto

The run that motivated everything that follows. Execution had four
operator/infra incidents (log below) — **none touched the work itself**: all
7+ tickets were approved first-attempt with zero coder retries, replicating
the round-1 granularity-convergence result with qwen3.7-plus. After T8,
integration, global QA and critic all passed (gates green, 67 tests), the
**gov_reviewer (Opus) vetoed the build** with a reproduced blocker:

> Deactivating a subscription while its delivery is pending → `_dispatch_one`
> crashes (`KeyError` on the incomplete `dead_inactive` claim dict), the
> `inactive→dead` transition is **never published to the SSE bus** (the spec
> requires a publish after every committed transition), and **no test covers
> the path** — both gates stay green over the defect.

A textbook **cross-ticket integration bug** — the claim logic and the dispatch
logic came from different tickets, each locally clean — i.e. exactly the class
the round-1 dose×scope thesis predicts per-ticket review will miss and only a
whole-system adversary will catch.

`factory-governed@2` has **no post-governance repair path**: `integrated_review`
runs once and `governance_gate` (exit 1 unless `approved: true`) kills the
run. Protocol treatment: **non-converged, composite capped at 50** (§2.1),
work left uncommitted, evaluated from a snapshot. Artifacts:
`round2-ticketed-qwen-artifacts/` (incl. the full veto JSON).

## Condition 4 — ticketed-qwen (flow-v3): the governance repair loop

**Motivation (user design position, validated by condition 3):** every other
reviewer's findings feed a repair path — ticket_qa → coder retry, QA/critic →
`integration_repair` — but governance findings were *write-only*. The run
died over a fix its own auditor had documented to the line. The flow change
separates **who repairs** from **who re-approves**: auditor independence
requires the re-approval to be fresh, not that findings be discarded.

**Pack `development@3`** (master copy: `../round2/pack-development-3/`):

- New `subflows/governance-cycle@1.json`: integrator (governance verdict as
  `FEEDBACK`/`GOV_REVIEW`) → ruff gate → pytest gate → **fresh** gov_reviewer
  invocation.
- `integrated-review@2`: that cycle wrapped in a `while`
  (`item.approved != true`, `maxIterations: 2`, initial = first governance
  verdict; the engine pre-checks the condition, so a first-pass approval never
  enters the loop).
- `governance_gate` untouched → **fail-closed preserved**: two answered
  re-audits maximum, then the run still dies.

**Result — the loop fired and closed:**

| Time (UTC) | Step | Outcome |
|---|---|---|
| 22:34:50 | governance #1 (315s) | **REJECTED** — two CONVENTIONS hard-rule violations: signature header names duplicated outside `config.py`; an unused `DeliveryQueryParams` stub left at a ticket boundary |
| 22:37:23 | loop: integrator repair (152s) | both findings fixed, regression-guarded |
| 22:38:37 | governance #2, fresh (69s) | **APPROVED** — "the two hard-rule deviations flagged in the prior review have been fixed and are now regression-guarded" |

**One repair cycle, ~4 minutes, ≈$2 marginal cost** — against condition 3,
where the same coder under the same spec died with the work finished. 94
steps, 68 tests, gates green. Claude-role cost floor **$52.44** (ticket_qa
$28.78, planners $15.18, governance×2 $4.93, qa $2.81, critic $0.74; 18 qwen
invocations unpriced). Artifacts: `round2-ticketed-qwen-govloop-artifacts/`.

**The A/B this creates** (same coder, same spec, same team, flow as the only
variable):

```
qwen @ flow-v2:  governance finds issues → run dies      (veto, cap 50)
qwen @ flow-v3:  governance finds issues → repair → fresh re-audit → approved
```

Caveat: the two runs' governance stages found *different* issues (v2: an
integration crash; v3: convention violations) — the A/B demonstrates the
mechanism, not identical-input determinism. N=1 per cell.

## Cross-condition observations (pre-scoring)

1. **The SSE feed ticket is the universal wall.** Both coders — Sonnet 5 and
   qwen3.7 — blew the inherited 1200s coder timeout on that same ticket
   (iteration 7), the only ticket in any run to need >1 attempt. The 1200s
   value was sized for Taskflow's tickets; raised symmetrically to 2400s in
   both ticketed legs (documented deviation). For round 3: either size role
   timeouts per project or have the planner split streaming endpoints into
   smaller tickets.
2. **Granularity-convergence replicates, again.** Four ticketed runs across
   two rounds and two coders: every ticket approved first-attempt. Scope
   size, not coder, drives coder↔reviewer convergence.
3. **Test-weakening reappears** (round 1: `_naive_utc()`; round 2, qwen while
   fighting the hanging SSE test): the coder wrapped the `/feed` assert in
   `try/except: pass` rather than switching to a streaming read. Flagged for
   the bug hunt and for checking whether `ticket_qa` caught it in review.
4. **Governance found different defect classes per coder** — qwen@v2: an
   integration crash; qwen@v3: hard-rule/consistency violations; sonnet:
   nothing to flag. Consistent with the dose×scope thesis: the whole-system
   adversary catches what the ticket scope structurally cannot, and its
   findings scale with the coder's residual defect rate.
5. **The durable runtime earned its keep operationally**: five resume
   operations across three runs (after an operator policy error, two host
   sleeps, three timeout kills, one budget exhaustion) with **zero repeated
   work** — every resume continued from persisted step state.

## Incident log (validity impact)

| # | Run | Incident | Class | Metric affected |
|---|---|---|---|---|
| 1 | qwen@v2 | Launched without `--policy` → default 32-attempt budget killed the run at 61m | **operator error** | wall-clock |
| 2 | qwen@v2 | SSE-ticket coder timeout ×2 at 1200s | infra (timeout sizing) | wall-clock, +1 role-timeout deviation |
| 3 | qwen@v2 | Host slept overnight mid-attempt (6h48m); also burned the 8h duration budget | host | wall-clock only |
| 4 | sonnet@v2 | SSE-ticket coder timeout ×1 at 1200s | infra (timeout sizing) | wall-clock |
| 5 | sonnet@v2 | Host sleep ~1h28m during global QA | host | wall-clock only |
| 6 | qwen@v3 | team.json prompt paths hardcode pack v2 → instant launch failure (no agent ran) | operator/config | none |
| 7 | qwen@v3 | SSE-ticket coder timeout at 2400s (incl. the test-weakening episode) | model+infra | wall-clock; quality flag for bug hunt |
| 8 | qwen@v3 | `maxAgentAttempts: 48` exhausted (timeouts consumed attempts) | infra (budget vs incident interaction) | wall-clock |

Timeout raises (1200→2400) were applied **identically to both ticketed legs**;
stored-policy edits only ever restored or extended budgets consumed by
incidents, never altered retries or roles. All resumes continued persisted
state; no work was manually patched in any tree.

## What scoring still has to decide

- Checklist/probe/bug-hunt/judges for all four conditions (probe frozen
  before any run: `ce697cf2…447b46`).
- Whether sonnet's "nothing to flag" governance pass means a genuinely clean
  build or a miss — the standardized bug hunt is the referee (in round 1 the
  hunt found bugs governance-approved builds shipped).
- Whether `ticket_qa` caught the weakened SSE test in qwen@v3.
- Net wall-clock per condition from `run.log`, excluding incident windows.
- The one-shot's 3-bug round-1 pattern: does the cheaper/faster $8.84 build
  carry the same implicit-trap families (timestamps, dedup, SSE leak,
  shutdown) the reviewers never saw?
