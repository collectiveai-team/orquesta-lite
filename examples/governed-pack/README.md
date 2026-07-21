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

## Run it

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
orq-lite flow validate development/factory-governed@1
orq-lite flow run development/factory-governed@1 \
  --policy=.orquestalite/packs/development/1/policies/development@2.json \
  features_path=features.md
```

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
- **Policy.** `policies/development@2.json` sets attempt/duration budgets;
  raise `maxDurationSeconds` for large specs, and always pass `--policy`
  explicitly so a run isn't capped by an engine default.
