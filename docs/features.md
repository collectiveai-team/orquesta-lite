# Product roadmap

The v2 durable runtime and governed development pack are the product baseline. New work must extend flows, subflows, activities, or operator tooling without introducing a second scheduler.

## 1. Objective capture and loop evaluation

- Add a first-class objective document/schema referenced by every governed iteration.
- Distinguish product objective, constraints, invariants, and current ticket plan.
- Require governance reviewers to cite evidence for completion or another loop.
- Track objective coverage and unresolved risks in durable workflow outputs.

## 2. Role capability profiles

- Let packs declare required capabilities or skills per role.
- QA profiles should distinguish API, CLI, browser UI, accessibility, and data validation.
- Browser-required QA must fail preflight when no supported browser skill/tool is available.
- Adversarial profiles should declare threat model, product invariants, and abuse cases.

## 3. Repository convention discovery

- Generate a compact conventions artifact from repository instructions, linters, tests, and representative code.
- Pin the conventions digest into a run.
- Make critic and coder evidence identify convention conflicts explicitly.

## 4. Durable observability

- Add workflow-native attempt and cost query endpoints backed by `workflows.db`.
- Surface approvals, retries, compensation, and policy-budget consumption in the dashboard.
- Add structured export for benchmark/evaluation tooling without rebuilding a second operational database.

## 5. Pack lifecycle

- Add signed pack provenance and trusted registries.
- Support explicit pack upgrade planning and compatibility checks.
- Keep built-in pack embedding and example validation byte-identical.

## Acceptance rule for roadmap work

Every feature must have a test at the public seam (`flow`, `pack`, `watch`, HTTP durable API, or workflow store) and must preserve deterministic resume from pinned IR.
