# TODO: examples/ralph-loop

Spec: docs/superpowers/specs/2026-07-13-ralph-loop-example-design.md

- [x] flows.json — ralph_loop flow (nested retry_until, printf round capture, test gate)
- [x] team.json — haiku agents, ralph_coder/ralph_reviewer + 5 required roles
- [x] prompts/ralph-coder.md — same-prompt task picker, plan.md grammar, result contract
- [x] prompts/ralph-reviewer.md — adversarial validation, appends `- [ ]` to plan.md
- [x] plan.md — seeded demo plan documenting the grammar
- [x] README.md — ascii flow + mermaid + inputs/roles/caveats (issue-fix shape)
- [x] examples/README.md — add table row
- [x] Verify: engine tests, go build/vet/test, {{VAR}} cross-check
- [x] Commit spec + example

## Review

- Went beyond the spec's "scratch program" verification: added two permanent
  tests in `internal/engine/examples_test.go`:
  - `TestExampleConfigsParse` — every example's `flows.json` parses via
    `engine.LoadFlows` and every `team.json` loads + resolves via
    `config.Load`/`Resolve` (guards ALL examples against schema drift, not
    just this one).
  - `TestRalphLoopFlowConverges` — drives the real
    `examples/ralph-loop/flows.json` end to end with scripted agents
    (2 tasks → reviewer finding → fix → approval): proves the nested
    `retry_until` convergence, the `printf %s {_attempt}` round capture
    (`REVIEW_ROUND`/`ROUND` = "2" in round 2), and the feedback threading
    (`PREVIOUS_SUMMARY`, `TEST_OUTPUT`, `REVIEWER_FEEDBACK`).
- `{{VAR}}` cross-check: every prompt variable has a matching flow input;
  `MEMORY`/`CONVENTIONS` are invoker-injected.
- `go build ./...`, `go vet ./...`, `go test ./...` all green.
- Known limits (documented in the example README): result contracts carry the
  control flow (model-obedience risk, same as other examples); hard budgets
  (25 coder passes/round, 4 rounds) abort the flow by design.
