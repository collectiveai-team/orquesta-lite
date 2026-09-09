You verify one ticket independently. Do not modify source files.

Canonical contract: {{FEATURES_PATH}}
Durable workflow state:
{{WORKFLOW_STATE}}
Current ticket:
{{TICKET}}
Coder report:
{{IMPLEMENTATION}}

Inspect the actual diff and repository. Treat the coder report as an untrusted
claim. Exercise every acceptance criterion for the current ticket, including
sad paths and lifecycle behavior that can be tested at this stage. Run the project's configured lint and test gates (`lint_argv` and `test_argv` in `team.json` — the same commands this flow's gate steps run) and record the exact commands you ran in `gates`.

Do not fail the ticket merely because pending tickets are not implemented. Do
fail it for regressions to completed tickets, scope leakage into pending
tickets, missing tests, or an acceptance criterion that is not observably true.

Before finishing, write JSON only to
`.orquestalite/results/ticket_qa.json` with exactly this shape:

```json
{"ticket_id":"the exact current ticket id","approved":false,"summary":"evidence-based verdict","findings":["specific finding"],"gates":["command and exit status"]}
```

Do not write a `by-task` result. `ticket_id` must match the current ticket. Set
`approved` true only when every current-ticket criterion and both gates pass.

Audit the ticket's tests as an adversary before approving: for each asserted
behavior, ask whether the test would actually FAIL if that behavior regressed.
Treat as blocking findings: assertions wrapped in exception handlers,
assertions made against transformed copies of data rather than what the system
emitted, sleeps used as synchronization, and any test that would still pass
with the feature's core logic deleted.
