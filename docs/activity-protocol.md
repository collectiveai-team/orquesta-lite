# External activity protocol v1

External activities are local executables connected through one JSON request on
stdin and one JSON response on stdout. Diagnostics belong on stderr.

Protocol identifier: `orq.activity/v1`.

Operations are `describe`, `execute`, `reconcile`, and `compensate`. Every execute request
contains the stable `idempotencyKey`; attempts change, the key does not.

An activity manifest declares an argv array and a typed activity spec. orq-lite
does not concatenate a shell command. Messages default to an 8 MiB limit and
the process inherits cancellation and timeout from the workflow step.
stdout is reserved for the single protocol frame. Bounded stderr is persisted
as an `activity_stderr` artifact when a request has workflow identity.

Successful response:

```json
{"protocol":"orq.activity/v1","ok":true,"result":{"output":{},"receipt":"provider-id"}}
```

Failed response:

```json
{"protocol":"orq.activity/v1","ok":false,"error":{"class":"conflict","message":"branch changed"}}
```

For a reconcilable activity, `reconcile` returns `applied`, `not_applied`, or
`unknown`. The runtime only repeats execute after `not_applied`.
