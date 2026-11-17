# Durable external steps

`activity:external.dispatch@1` gives work to an external employee and suspends
the durable run in `waiting_external`. The workflow remains the owner of its
definition, validation, gates, and transitions. A callback is a request to check
the external task; it cannot choose the next step.

## Paperclip connection

Add connection metadata to `team.json`:

```json
{
  "runtime": {
    "external_adapters": {
      "paperclip": {
        "type": "paperclip",
        "base_url": "http://localhost:3100",
        "company_id": "company-uuid",
        "token_env": "PAPERCLIP_API_KEY",
        "employees": {"implementer": "agent-uuid"}
      }
    }
  }
}
```

`token_env` names an environment variable supplied to the process. Keep the
credential in your secret provider or local environment; do not put its value
in `team.json`, a flow, or a callback. With Varlock, inject the credential using
`varlock run -- orq-lite ...`. Omit `token_env` only for a Paperclip deployment
whose authentication configuration permits it.

An employee alias resolves to a concrete Paperclip agent before dispatch. The
attempt saves that ID, its request, deadline, idempotency key, and returned issue
and run references. Later configuration changes do not reassign that attempt.

## A mixed synchronous and external flow

```json
{
  "apiVersion": "orq.dev/v2",
  "kind": "Flow",
  "metadata": {"name": "external-example", "version": "1"},
  "steps": [
    {
      "id": "prepare",
      "uses": "activity:command.run@1",
      "with": {"argv": ["git", "status", "--short"]}
    },
    {
      "id": "implementation",
      "uses": "activity:external.dispatch@1",
      "with": {
        "adapter": "paperclip",
        "employee": "implementer",
        "instructions": "Implement the requested change and provide reviewable evidence.",
        "completionCondition": "Issue done, successful run, approved deliverable and evidence attached.",
        "timeoutSeconds": 3600,
        "requireEvidence": true,
        "completionGate": "item.deliverables.approved == true",
        "routes": {
          "success": "continue",
          "timeout": "continue",
          "cancelled": "needs_human",
          "blocked": "needs_human",
          "failed": "fail",
          "invalid": "needs_human"
        }
      }
    },
    {
      "id": "verify",
      "uses": "activity:command.run@1",
      "if": "steps.implementation.output.outcome == \"success\"",
      "with": {"argv": ["git", "diff", "--check"]}
    },
    {
      "id": "timeout_route",
      "uses": "activity:command.run@1",
      "if": "steps.implementation.output.outcome == \"timeout\"",
      "with": {"argv": ["echo", "External task exceeded its deadline"]}
    }
  ]
}
```

The step output is `{outcome, decision, result}`. `continue` means that the
declared outcome becomes available to subsequent `if` expressions; a timeout
still has outcome `timeout`, never `success`. `fail` stops the run.
`needs_human` preserves the attempt and stops scheduling pending intervention.
Temporary unavailability waits by default; the remaining failure outcomes
default to intervention. Evidence is required by default. An optional literal
`outputSchema: "schema:deliverable@1"` pins and validates the deliverables
schema. `completionGate` evaluates against the authoritative typed result as
`item`; downstream synchronous gates can add project-specific checks.

To replace a native coding invocation while preserving its downstream JSON
contract, use `outputMode: "deliverables"` with the same `outputSchema` and step
ID. Orq-lite sends the pinned JSON schema to the Paperclip agent with the task.
The successful step output is then the deliverables JSON itself; references,
decisions and usage remain available through the external-attempt API. Failure
branches that explicitly continue use the default envelope mode instead.
`projectId` optionally assigns the issue to a specific Paperclip project and
is retained in the durable dispatch request. A runnable variant is in
[`examples/paperclip-coding`](../examples/paperclip-coding/flow.json).

A finished agent run with an issue still in progress keeps waiting. A completed
issue must also have a successful run, the pinned responsible employee, valid
deliverables, and required evidence before the engine accepts success.

| Outcome | Default decision |
| --- | --- |
| `success` | Continue after schema, evidence and completion gate validation |
| `waiting` | Keep waiting; a finished run alone does not complete the issue |
| `timeout` | Request external cancellation and require intervention |
| `cancelled`, `blocked`, `failed` | Require intervention |
| `unavailable` | Wait and check again |
| `invalid` | Require intervention; includes wrong employee or invalid output |

A failure explicitly routed to `continue` leaves the step in `routed` and its
attempt in `failed`, with the original outcome in its output. It is never
recorded as a successful external attempt. The run can still complete after
executing the flow's declared recovery branch.

## Callbacks and recovery

The host API receives and authenticates Paperclip webhooks, extracts identifiers,
and calls the transport-independent engine operation:

```go
run, err := runtime.ResumeExternal(ctx, flowRunID, stepID, attemptID, external.Event{
    ID: eventID,
    Type: "agent.run.finished",
    References: external.References{IssueID: issueID, RunID: paperclipRunID},
})
```

The engine durably records the event before processing it. The inbox does not
need MCP or a public endpoint inside orq-lite. Events arriving before dispatch
references have been saved remain pending. Duplicate events and callbacks for
an older attempt cannot produce another transition. Event payloads are never
accepted as authoritative task results.

An Orquesta host running outside the Go module can use the CLI bridge:
`flow external receive` commits the notification and returns a receipt without
loading adapters or running the flow. A separate worker calls `reconcile` or
keeps `watch` running. Missing credentials, unavailable executors, and slow
downstream steps therefore do not prevent durable webhook acceptance.

The Orquesta API implements `POST /webhooks/paperclip`, protected by its
dedicated callback bearer token. The [Paperclip relay](../integrations/paperclip/README.md)
forwards normalized IDs from issue/run events. Managed flow launches use
`orquesta:<launch-id>` source keys; `flow lookup <source-key>` exposes a bounded
status snapshot so API restarts and process exits preserve `waiting_external`.
The host starts periodic recovery automatically and reserves the project's
development slot until the durable run is terminal.

Dispatch intent is stored before the network call. Recovery reuses the same
attempt and Paperclip issue-creation idempotency key after a crash, including a
crash between successful creation and local acknowledgement. Paperclip retains
those keys for seven days; a prepared dispatch whose outcome remains unknown
past the engine's six-day recovery window requires intervention instead of
blindly creating another issue.

```sh
orq-lite flow status RUN_ID
orq-lite flow external attempts RUN_ID
orq-lite flow external reconcile RUN_ID
orq-lite flow external watch RUN_ID
orq-lite flow pause RUN_ID
orq-lite flow resume RUN_ID
orq-lite flow cancel RUN_ID
orq-lite flow external retry RUN_ID ATTEMPT_ID
orq-lite flow resume RUN_ID
```

`watch` checks every 30 seconds while the run waits. A service can schedule
`Runtime.ReconcileExternal` periodically; this uses the same verification path
as callbacks and recovers lost notifications. A service restart can discover
waiting runs in the store and resume reconciliation. Waiting requires no live
orq-lite process.

An embedded host can run `Runtime.RunExternalReconciler(ctx, interval)` for
periodic recovery. It also recovers a crash between committing an external
decision and executing the next step. Checking an existing attempt does not
spend another attempt or require spare attempt budget. Workflow duration limits
still include time spent waiting; expiry requests remote cancellation and
records its acknowledgement.

Pausing prevents new dispatches; explicit resume unpauses the run. Cancelling
first stops local scheduling, then requests external cancellation and records
whether Paperclip accepted it. Retrying retains previous attempts and creates
a new attempt ID when resumed. Retrying a paused run preserves the pause.
Each new attempt resolves the employee again; an existing attempt always keeps
its original stable employee ID. Retry is refused while the previous dispatch
is uncertain or cancellation of nonterminal work is unconfirmed, so it cannot
silently overlap assignments. Reconcile the uncertain attempt or confirm its
external cancellation before trying again.
To replay a notification manually, save the
event JSON and use
`orq-lite flow external receive RUN_ID STEP_ID ATTEMPT_ID event.json`.
Then invoke `reconcile`, or let the periodic worker process the queued event.

The read-only dashboard and `GET /api/workflows/{id}/external` show employee,
issue/run references, attempt history, elapsed wait, last check, decision,
cancellation response, and available Paperclip usage. The dashboard does not
host a public webhook receiver.

Usage is retained by execution run and updated cumulatively. Repeated or older
observations do not add the same cost twice or reduce spending already counted
against the workflow budget. The attempt view aggregates known reported runs;
unreported cost/tokens remain unavailable, while an explicitly reported zero
is shown as zero. No local price estimate substitutes for provider reporting.

A pause fences new dispatch intents and prevents advancement. A dispatch already
claimed before the pause may finish registering its references; cancellation
requests cleanup of such in-flight work when its references become available.

## Verification

Run `go test -race ./...`. The tests cover durable restart, uncertain dispatch
replay with the same key, duplicate and early callbacks, obsolete attempts,
schema/evidence/gate rejection, open issues with finished runs, lost callbacks,
pause/cancel races, continuation after a committed decision, explicit failure
routes, and mixed synchronous/external execution. Paperclip protocol tests use
an HTTP test server; a deployed Paperclip instance is not needed for this suite.
