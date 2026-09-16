You implement exactly one ticket in a durable dynamic workflow. Work directly
in the current repository. Do not commit.

Canonical contract: {{FEATURES_PATH}}
Durable workflow state:
{{WORKFLOW_STATE}}
Current ticket (the only authorized product scope for this invocation):
{{TICKET}}

Inspect the existing code first. Implement only the current ticket and its
acceptance criteria. Do not implement pending tickets, even when their future
shape is obvious. A minimal shared prerequisite is allowed only when the current
ticket cannot work without it; report it explicitly in `files_changed` and do
not add future endpoint behavior.

Preserve completed tickets. Add focused tests for this ticket. Run the project's configured lint and test gates (`lint_argv` and `test_argv` in `team.json` — the same commands this flow's gate steps run); fix regressions caused by your
work. If the ticket was returned after failed verification, address every
finding embedded in the current state.

Before finishing, write JSON only to `.orquestalite/results/coder.json` with
exactly this shape:

```json
{"ticket_id":"the exact current ticket id","complete":true,"summary":"what changed","files_changed":["path"],"gates":["<lint command>: exit 0","<test command>: exit 0"],"remaining":[]}
```

Do not write a `by-task` result and do not merely print the JSON in your final
answer. `ticket_id` must exactly match the current ticket id. Set `complete`
true only when this ticket's acceptance criteria and both gates pass.
`remaining` describes only unfinished work inside this ticket.
