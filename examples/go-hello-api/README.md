# Example: `go-hello-api`

A minimal Go (standard-library) HTTP API built by the **`factory_fast`** flow, which ships
in the repo's root [`flows.json`](../../flows.json).

## What it builds

One feature (`features.md`): a `GET /hello` endpoint returning `{"message":"hello"}` with a
table-driven `httptest` test, in a module named `demoapi`.

## Flow: `factory_fast`

Per feature, on its own branch: `parser → coder → ruff/vet → tester → critic → go test →
commit → reviewer → push/PR`, then a single global `verifier`. Tasks are batched into one
coder call (no per-task retry) — the "fast" lane.

```mermaid
flowchart TD
  A["action: extract features → features_queue"] --> LOOP
  subgraph LOOP["loop: per feature (own branch)"]
    direction TB
    B["cmd: git checkout -b feature branch"] --> P["agent: parser → tasks"]
    P --> FB["action: format batch prompt"]
    FB --> C["agent: coder → coder_res"]
    C --> V["cmd: go vet — on_failure continue"]
    V --> T["agent: tester"]
    T --> CR["agent: critic"]
    CR --> GT["cmd: go test"]
    GT --> CM["cmd: git add + commit"]
    CM --> RV["agent: reviewer"]
    RV --> PUSH["cmd: git push — continue"]
    PUSH --> PRC["cmd: gh pr create — continue"]
  end
  LOOP --> VER["agent: verifier — global review"]
```

## Files

| File | Purpose |
|------|---------|
| `team.json` | Haiku agents; roles `parser, coder, tester, critic, reviewer, verifier`. Test gate `go test ./...`, lint gate `go vet ./...`. |
| `features.md` | The single "Hello JSON API" feature. |

## Prerequisites

- `go` on PATH
- `claude` CLI authenticated

## Run it

Copy this config next to the repo's `prompts/` and root `flows.json` in a fresh Go module,
then:

```sh
orq-lite flow run factory_fast features_path=features.md base_branch=main --log-format verbose
```

The repo's `prompts/` directory supplies the `parser/coder/tester/critic/reviewer/verifier`
prompt templates referenced by `team.json`.
