# Authoring flows, subflows and packs

Load this when writing or editing flow data. For the command surface, flow
selection and the runtime semantics, see `SKILL.md`.

## Minimum flow

A flow is JSON compiled strictly — unknown fields, unresolved references and
type mismatches all fail before anything executes.

```json
{
  "apiVersion": "orq.dev/v2",
  "kind": "Flow",
  "metadata": { "name": "my-flow", "version": "1" },
  "inputs": { "features_path": { "schema": "schema:path@1" } },
  "steps": [
    { "id": "implement", "uses": "activity:agent.invoke@1",
      "with": { "role": "coder",
                "outputSchema": "schema:ticket-implementation@1",
                "vars": { "FEATURES_PATH": { "$ref": "inputs.features_path" } } } }
  ],
  "outputs": { "result": { "$ref": "steps.implement.output" } }
}
```

`kind: "Subflow"` is the same shape, invoked as `uses: "subflow:name@version"`.

## Activities

| Activity | Effect | Use for |
|---|---|---|
| `agent.invoke@1` | AtMostOnce | run a role; validates output against `outputSchema` |
| `command.run@1` | AtMostOnce | any command; returns `{exitCode, stdout, stderr, durationMs}` |
| `gate.run@1` | Pure | a lint/test gate; **a failure aborts the flow** |
| `gate.assert@1` | Pure | assert a value equals something, with a message |
| `artifact.capture@1` | Idempotent | persist an artifact |
| `human.wait_for_approval@1` | Pure | park the run for a decision |

`agent.invoke@1` inputs: `role`, `outputSchema`, `vars` (flat strings),
`context` (any JSON, marshalled per key), `skills` (array — compiles as both a
literal and a `$ref`), `fallbackOutput` (used when the agent fails; make it
fail-closed for reviewers, fail-open for optional steps).

## Step controls

```json
{ "id": "maybe", "if": "steps.qa.output.approved == true", ... }

{ "id": "loop", "uses": "subflow:develop-ticket@1",
  "while": { "condition": "item.state.status == \"active\"",
             "maxIterations": { "$ref": "item.state.iteration_budget" },
             "initial": { "state": { "$ref": "steps.plan.output" } } },
  "with": { "state": { "$ref": "item.state" } } }
```

Inside a `while`, `item` is the previous iteration's output and `index` is the
counter. The scheduler extends `ScopePath` once per subflow instantiation, which
is what gives each iteration its own session-resume scope — a step nested in a
loop's subflow keeps a constant `StepID` and an empty `ForeachKey`, so anything
keyed on those will collide across iterations.

## Wiring data between steps

`{"$ref": "steps.X.output.field"}` resolves a prior step's output. Nested paths
work: `steps.develop_tickets.output.last.state.status`. Bare string literals are
valid in a `vars` block (`"CURRENT_STATE": "{}"`).

**`command.run` stdout `$ref`s into a later step's input.** This is how you feed
generated data into a prompt without a new activity:

```json
{ "id": "build_map", "uses": "activity:command.run@1",
  "with": { "argv": ["bash", "/path/to/generate-map.sh"] } },
{ "id": "implement", "uses": "activity:agent.invoke@1",
  "with": { "role": "coder",
            "context": { "REPO_MAP": { "$ref": "steps.build_map.output.stdout" } } } }
```

The same trick turns a gate into data instead of a run-ending failure:
`argv: ["sh","-c","<gate cmd>; echo EXIT=$?"]` always exits 0 and puts the real
code in `stdout`, so a reviewer can read it while the run continues.

## Prompts

Prompts are markdown with `{{VAR}}` substitution. Interpolation is a plain
`strings.ReplaceAll` — no conditionals, and an unsupplied key is left
**literally in the prompt text**. When adding a `{{VAR}}` to a prompt shared by
several call sites, supply it everywhere, even as `"n/a"`.

`{{CONVENTIONS}}`, `{{MEMORY}}` and `{{SKILLS}}` are injected by the runtime on
every role invocation (`internal/invoke/role.go`), sourced from
`conventions_file`, `.orquestalite/memory.md` and the step's `skills` input.
Consuming one is adding a placeholder, not adding plumbing.

Remember the cost model: injected context is re-read from cache **every turn**,
so its true cost is size × turns × number of consuming prompts.

## Schemas

`outputSchema` must match what the prompt actually writes. A schema requiring a
field the prompt never emits fails validation on every call and burns the
corrective retry — write a purpose-built schema rather than reusing a
near-miss. Schemas declaring `additionalProperties: false` reject any field you
add to a prompt's declared shape without updating them.

## Packs

A pack directory holds `pack.json` plus `flows/`, `subflows/`, `schemas/`,
`policies/`, `prompts/`. `pack.json` lists every file with its sha256; a
mismatch fails verification at install.

Three traps:

- **`flow validate <path>` resolves `schema:` refs relative to the pack, not the
  project.** A standalone flow referencing pack schemas fails with
  `catalog: schema:path@1 not found`. Put it in a pack and validate the pack ref.
- **The catalog indexes flows by filename, which must equal
  `metadata.name@version`.** `my-flow@1.json` declaring `metadata.name: "other"`
  never appears in `flow list`.
- **`pack install` refuses to overwrite.** Use `--force`, and regenerate the
  digests after editing any file.

Two packs with the same `name` collide on install; rename in `pack.json` when
you need variants installed side by side.

## Policies

A policy caps a run: `maxDurationSeconds`, `maxAttempts`, `maxAgentAttempts`,
`maxCostUSD`, `maxParallelism`, and per-class `retries` for `transient`,
`rate_limited` and `timeout`.

`maxDurationSeconds` is **wall-clock**. A run idling — waiting on a throttle, or
on a sleeping machine — spends it doing nothing and dies with
`workflow duration budget exhausted`.

Policy-level retry cannot rescue `agent.invoke@1`: the scheduler gates retries
to Pure/Idempotent/Reconcilable effects, and `agent.invoke@1` is AtMostOnce. Its
recovery is the bounded same-agent corrective retry inside `internal/invoke`
(for `invalid_contract`/`result_missing`) plus the multi-agent fallback chain.

## Before spending

```bash
orq-lite flow validate <pack>/<flow>@<v>   # compiles without executing
orq-lite doctor                            # packs, roles, prompts, gates, CLIs, credentials
```

Both are cheap. A run that fails validation after ten minutes of agent time
failed for free ten minutes earlier if you validated.
