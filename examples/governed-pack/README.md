# Governed development pack

`development@7` is the canonical built-in pack embedded in the binary. The frozen `pack-v5` and `pack-v6` directories preserve pinned older runs.

## Flows

| Flow | Purpose |
|---|---|
| `plan-tickets@1` | Produce or extend a bounded ticket plan. |
| `task-list@1` | Plan, implement with ticket commits, and run final gates. |
| `factory-fast@1` | Legacy one-shot batch with one QA pass; use `factory-governed@2 fast=true` for the durable fast loop. |
| `factory-governed@2` | Implement and commit each ticket, then run integrated governance. |
| `review-existing@1` | Run governance over existing changes. |
| `pr-review@1` | Review a pull request and optionally publish the verdict. |
| `issue-fix@1` | Triage an issue and optionally execute its repair workflow. |

## Ticket delivery

Both `fast` settings use `develop-ticket@2`. Every invocation receives one ticket, runs focused tests, and reports their argv and exit codes. `ticket.preflight` checks that report; `git.commit` then runs the project's commit hooks. A refused commit is repaired and retried. `ticket.checkpoint` prevents completion without a commit.

- `fast=true` (default): `plan.advance` selects the next dependency-ready ticket mechanically. Each ticket is a separate durable invocation, so the `coder` timeout never covers the entire backlog.
- `fast=false`: the planner sees the checkpoint after every ticket and can split, reorder or block remaining work.

There is no `ticket_qa` agent in this delivery loop. `factory-governed@2` runs the full lint and test gates, independent QA, adversary, critic, visual verification, repairs and governance after ticket delivery. `task-list@1` runs the full deterministic gates at the end.

An optional mutation command runs after integrated review in `factory-governed@2`, or after final tests in `task-list@1`:

```bash
orq-lite flow run development@7/factory-governed@2 features_path=features.md mutation_enabled=true 'mutation_argv=["mutmut","run"]'
```

The command must exit nonzero when the required mutation threshold is missed. It is an argv array, not a shell string. Run it in isolated test state if the mutation tool changes files.

The separate `factory-fast@1` flow retains its historical one-shot behavior for pinned callers. It does not use the new ticket checkpoints.

For existing projects, `orq-lite pack sync development --dry-run` previews the v7 pack and any `team.json` prompt-path migration. Run `orq-lite pack sync development` to apply both; v5 and v6 remain installed for pinned runs.

## Validate and install

From the repository root:

```bash
orq-lite pack install examples/governed-pack/pack
orq-lite flow validate development@7/factory-governed@2
orq-lite flow inspect development@7/factory-governed@2
```

When a pack resource changes, run `python3 examples/governed-pack/regen-digests.py` and review the resulting manifest.
