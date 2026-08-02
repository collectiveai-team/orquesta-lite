# Governed development pack

`development@4` is the canonical built-in pack and the source embedded into the release binary. It demonstrates the product's v2-only architecture: flows compose versioned subflows, schemas, policies, and role prompts; the durable scheduler executes the compiled and pinned IR.

## Flows

| Flow | Purpose |
|---|---|
| `plan-tickets@1` | Produce or extend a bounded ticket plan. |
| `task-list@1` | Plan, implement, verify, and run final gates. |
| `factory-fast@1` | Implement a feature batch with one integrated verification. |
| `factory-governed@1` | Develop tickets, then iterate integrated governance. |
| `review-existing@1` | Run governance over existing changes. |
| `pr-review@1` | Review a pull request and optionally publish the verdict. |
| `issue-fix@1` | Triage an issue and optionally execute its repair workflow. |

## Governance design

Delivery and integrated review are separate phases:

```text
ticket planner -> coder -> ticket QA
                         |
                         v
integrated QA + adversary + critic
                         |
                         v
                    integrator
                         |
                         v
                 governance reviewer
                         |
                 repeat or complete
```

- QA validates the integrated behavior and is expected to use browser-oriented project skills for web work.
- The adversary evaluates the product objective, invariants, security boundaries, and realistic misuse—not merely acceptance-criteria wording.
- The critic evaluates correctness risks, maintainability, architecture, and repository conventions.
- The governance reviewer turns evidence into another bounded state or completion.

The current objective and workflow state travel through each iteration, so a ticket never becomes the sole definition of success.

## Quality gates

The pack contains no project toolchain commands. It reads:

```json
{"argv": {"$ref": "config.lint_argv"}}
{"argv": {"$ref": "config.test_argv"}}
```

`orq-lite init --lang ...` fills these values and `doctor` reports missing or unavailable gate executables.

## Validate and install

From the repository root:

```bash
orq-lite pack install examples/governed-pack/pack
orq-lite flow validate development@4/factory-governed@1
orq-lite flow inspect development@4/factory-governed@1
```

When any pack resource changes, run `python3 examples/governed-pack/regen-digests.py` and review the resulting `pack.json` digest changes.
