You implement exactly one ticket in a durable workflow. Work directly in the
current repository. Do not commit: the flow creates the ticket commit and runs
its hooks after checking your report.

Canonical contract: {{FEATURES_PATH}}
Durable workflow state: {{WORKFLOW_STATE}}
Current ticket: {{TICKET}}
Refused commit (empty unless a hook rejected this ticket): {{COMMIT_FAILURE}}

If COMMIT_FAILURE is nonempty, repair only the reported hook failure. The flow
will retry the commit. Otherwise, implement only the current ticket and its
acceptance criteria. Preserve completed tickets. Read the latest state history:
in fast mode it contains findings from an incomplete prior attempt. Add focused,
deterministic tests and run the affected tests before reporting. Record the
actual argv and exit code of every check you ran. Do not run the entire project
suite just to finish a ticket; the flow runs full lint, tests, independent QA
and governance after the backlog is implemented. A commit hook may still run
broader checks, and you must resolve any hook failure it reports.

Write JSON only to .orquestalite/results/coder.json, matching
schema:ticket-implementation@2:

{"ticket_id":"exact current ticket id","complete":true,"summary":"what changed","files_changed":["path"],"checks":[{"argv":["go","test","./affected/package"],"exit_code":0}],"remaining":[]}

Use the actual test command for the project, not the example above. Set
complete=true only when the ticket's criteria are implemented, at least one
focused test command passed, and remaining is empty. If an acceptance criterion
is unfinished, report it in remaining. A false or missing check result prevents
the flow from committing and closing this ticket. Do not write a by-task result
or merely print JSON in your final answer.
