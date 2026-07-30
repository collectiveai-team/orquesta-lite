# Example: the governed development pack (`development@1`)

The recommended production setup for the durable **v2 runtime**: a per-ticket
build flow wrapped in layered, adversarial review with a **veto that has a
repair path**. This is the pack the field lessons in
[`../../guide.md`](../../guide.md) (§4) bake in.

Unlike the other examples — which are `flows.json` reference configs — this one
ships a self-contained **local pack** (`pack/`), because the v2 runtime runs
strict `orq.dev/v2` Flow/Subflow JSON from an installed pack, never from a
downloaded one. Orquesta installs and selects pack versions in production; this
example is the reference copy you can run locally.

## What the flow does (`factory-governed@1`)

```
plan_tickets (budget-sized)
└─ develop_tickets  [per ticket: coder → ticket_qa → replan]
integrated_review:
   lint → tests → qa → adversary → critic
   → integration_repair   [loop: reconcile findings]
   → gates → governance
   → governance_repair     [loop ×2: repair → gates → FRESH re-audit]
   → governance_gate       (fail-closed)
```

Three things distinguish it from a plain per-ticket flow, each a lesson learned
the hard way (see `../../benchmark/results/sixway-r1.md` for round 1 and
`round2-r1.md` / `round3-r1.md` for the later rounds):

1. **An `adversary` role** that hunts for what the *spec didn't say* —
   deriving failure hypotheses from the system's shape (shared state,
   concurrent writers, lifecycles, I/O boundaries) and reproducing them
   against running code. A finding only counts with a reproduction.
2. **A governance repair loop.** A veto is not a dead end: its findings feed
   the integrator, gates re-run, and a **fresh** governance invocation
   re-audits — up to twice, then the run still dies (fail-closed preserved).
   A reviewer that can raise issues must have a path to a fix.
3. **A test-integrity audit** in `ticket_qa` and the adversary: for every
   assertion, would it fail if the behavior regressed? Vacuous guards,
   asserts wrapped in exception handlers, and sleeps-as-synchronization are
   blocking findings.

## Flows

The pack ships seven flows:

| Flow | Description |
|------|-------------|
| `factory-governed@1` | Full governed build; `fast=true` switches to the batch path, `create_pr=true` opens a PR |
| `review-existing@1` | Audit an existing tree |
| `plan-tickets@1` | Planning only — `orq-lite plan` alias |
| `task-list@1` | `orq-lite run` alias |
| `factory-fast@1` | One-batch fast path |
| `issue-fix@1` | Triage → plan → develop — `orq-lite intake` alias and `watch --issues` default |
| `pr-review@1` | Agent-driven PR review — `orq-lite review` alias and `watch --prs` default |

## Run it

> This pack's gate steps take their commands from **your project's config**:
> `lint_argv` and `test_argv` in `team.json`, as argv arrays. The example
> `team.json` here declares a Python/`uv` toolchain; point them at your own
> commands and the pack works unchanged — no flow or subflow JSON to edit.
> Both keys are required: a run whose flow references a missing, non-array, or
> empty argv aborts at startup rather than silently skipping the gate.

The example ships a **haiku-only** team so a full run is cheap. The governed
pack is an *overlay* on an initialized project — run `orq-lite init` first to
scaffold the project (`.gitignore`, base config), then install the pack and
drop this team over the generated one:

```sh
# from a fresh project dir with git initialized and both gates green at HEAD
orq-lite init                              # scaffold project config + .gitignore
orq-lite pack install path/to/examples/governed-pack/pack
cp path/to/examples/governed-pack/{team.json,features.md,CONVENTIONS.md} .

orq-lite doctor                            # resolves the team, checks CLIs + gates
orq-lite pack list                         # what's installed, and which version refs resolve to
orq-lite flow validate development/factory-governed@1
orq-lite flow run development/factory-governed@1 features_path=features.md
```

Every flow in this pack declares its own policy (`metadata.policy`), so the run
above applies `policies/development@3.json` automatically — the run line reports
which one it used and where it came from (`policy=policy:development@3
policy_source=flow-metadata`). Pass `--policy=<ref|path>` only to *override* it
deliberately.

`development/factory-governed@1` names pack `development` (highest installed
version) and flow `factory-governed@1`. To pin the pack version independently of
the flow's, write it explicitly: `development@4/factory-governed@1`.

> `orq-lite doctor` reports a `legacy roles` **warn** for this team (no
> `parser`/`tester`/`reviewer`). That is expected and harmless: those roles are
> only needed by the legacy `plan`/`run`/`factory` commands, not by
> `orq-lite flow run`.

If a run stops (rate limit, timeout, host sleep), resume it — the durable
runtime continues from persisted step state, never repeating finished work:

```sh
orq-lite flow status <run-id>
orq-lite flow resume <run-id>
```

## Production notes

- **Models.** Swap the haiku team for real reviewers: a strong coder
  (e.g. Sonnet) and **Opus on the test/gate roles** (`ticket_qa`, `qa`,
  `adversary`, `critic`, `gov_reviewer`). The review roles are where the bugs
  are caught, and in the benchmark they were ~78% of a governed run's cost —
  that spend is the point, not the overhead.
- **Timeouts.** Size the `coder` timeout to your project's heaviest ticket
  (streaming endpoints, background workers, and lifecycle code are the usual
  culprits) and let the planner split infra concerns into their own tickets.
- **Policy.** Every flow declares `policy:development@3`, so it loads with no
  flag. Its attempt budgets are **runaway backstops, derived from the loop's own
  ceiling rather than guessed**: `iteration_budget` maxes at 200 passes and
  `develop-ticket@1` spends 3 agent invocations per pass, so the worst
  legitimate run is 1 + 200×3 + 11 = 612 agent invocations and ~1024 attempts.
  The policy sets `maxAgentAttempts: 1200` and `maxAttempts: 2400` — roughly 2x
  that, so the cap cannot bind before the loop's declared bound does. Any
  smaller number is a covert cap on how many tickets a run can finish, which is
  what `maxAgentAttempts: 48` silently was: it stopped a governed run at ~15
  tickets. If you lower these, derive the new value the same way.

  `maxDurationSeconds` (8h) is the other brake that works today.

  **`maxCostUSD: 250` does not currently brake anything on the Claude 5 family
  or gpt-5.5.** It is charged from the token usage each agent invocation
  reported, priced through `internal/cost/prices.go`, and a model with no price
  entry contributes 0 rather than a guess — `claude-opus-5`, `claude-sonnet-5`
  and `gpt-5.5` have no entries, so on those models the accumulated cost reads
  $0 and the cap can never fire. It does work for priced models. Treat it as
  inactive until those entries exist; note also that `EstimateUSD` prices cached
  input at the full rate, so a cache-heavy run would be charged well above real
  spend once they do. To budget for real, write your own policy file and pass
  `--policy`; an explicit flag still wins.
- **Iteration budget.** The ticket loop's bound comes from the planner's own
  `iteration_budget` field, re-read before every pass, so a mid-run replan that
  discovers new work extends the loop instead of dying at a stale number. It is
  a *cumulative* total (passes spent + work left + margin), not a count of work
  remaining, and the runtime treats it as a high-water mark: a budget that
  shrinks as tickets land cannot truncate a backlog the loop was already
  promised. The runtime still refuses to run more than 1000 passes for any
  flow, and every plan-driven flow ends its loop with a `gate.assert@1` on the
  plan reaching `status: complete`, so exhausting the budget fails the run
  loudly instead of reporting success over a pending backlog.
