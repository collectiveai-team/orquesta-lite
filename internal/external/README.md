# Paperclip adapter contract

`Adapter` is independent of HTTP callbacks and MCP. The host receives a webhook,
stores an `Event` through the workflow inbox, and asks the engine to reconcile.
An event is a notification, never a result or transition command.

`Paperclip` uses the company-scoped REST API. Its connection requires read access
to company agents, issues, execution projections, heartbeat runs, and result
documents; dispatch requires issue creation/assignment. Run cancellation is a
board-only Paperclip operation. Cancellation results record whether Paperclip
confirmed cancellation of the task and, when active, its run.

The adapter requires Paperclip's issue-create **body** `idempotencyKey` contract.
It does not rely on an `Idempotency-Key` header. Current upstream implements this
with a transactional advisory lock and company/key uniqueness. Older Paperclip
versions that silently discard this field are incompatible. The upstream key
retention is seven days, so an uncertain dispatch must not be replayed after that
window; it requires intervention. A recorded issue ID can be inspected for any
length of time without another create operation.

Employees resolve once to an ID within the configured company. Inspection reads
the issue, discovers its latest execution through `/issues/{id}/execution`, and
reads that heartbeat run and its responsible agent. Issue assignment and producing
run ownership must agree. A completed heartbeat with an open issue remains an
open task; the adapter reports both statuses separately.

Agents may publish an issue document with key `orq-result`. Its body is a JSON
object such as:

```json
{"deliverables":{"review":"approved"},"evidence":["https://example.test/review/123"]}
```

The adapter returns the `deliverables` JSON and evidence references, adding the
persisted document revision and nonfailed work-product references. Without that
document, the run's `resultJson` is the deliverable. Evidence references are
reported facts: the engine owns output schema checks, completion gates, and any
additional evidence validation. The adapter never downloads arbitrary evidence
URLs. Missing or invalid output cannot itself cause a successful transition.

API requests have a bounded timeout and response size. Redirects are disabled,
and response bodies and connection tokens are never included in error messages.
Transient transport/HTTP errors return `UnavailableError`.

Upstream contracts checked for this implementation:

- [Issue REST API](https://github.com/paperclipai/paperclip/blob/master/docs/api/issues.md)
- [Agent and cancellation API](https://github.com/paperclipai/paperclip/blob/master/docs/api/agents.md)
- [Issue create validator](https://github.com/paperclipai/paperclip/blob/master/packages/shared/src/validators/issue.ts)
- [Issue create idempotency and retention](https://github.com/paperclipai/paperclip/blob/master/server/src/services/issues.ts)
- [Execution lookup and heartbeat run routes](https://github.com/paperclipai/paperclip/blob/master/server/src/routes/agents.ts)
- [Heartbeat result and usage types](https://github.com/paperclipai/paperclip/blob/master/packages/shared/src/types/heartbeat.ts)
