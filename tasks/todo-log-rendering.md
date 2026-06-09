# Improve orq-lite log output rendering

## Context
`orq-lite plan` / `orq-lite run` dump noisy `verbose` key=value lines to the terminal.
Data is already structured (JSONL `.orquestalite/run.log`). Problem is purely the renderer.
A `human` format already exists but is not the default and drops the agent's summary narrative.

## Plan (scope 1-4, approved)

### 1. TTY-aware default format
- [ ] `newLiveDeps` (runcmd.go): when `LogFormat` is "" or "auto", pick `human` on a TTY, `verbose` when piped. Covers both `plan` and `run`.
- [ ] `main.go`: change `run --log-format` default to `auto`; update usage text.

### 2. Enrich `human` agent_run with a summary line
- [ ] eventlog: add exported `AgentSummaryLine(fields)` — clean one-line preview from `final_text_tail`, falling back to `stdout_tail`; first line only, bounded (~200 chars). Empty if neither.
- [ ] `formatAgentRun` appends an indented continuation line when a summary exists.
- [ ] Update `TestHumanFormat_SummarisesAgentRun` to the new contract (summary shown, full tail never dumped — bounded preview only).

### 3. Fix `verbose` renderer defects
- [ ] `summariseFields`: deterministic key order (priority keys first, then alpha), skip empty-string values, keep tails last.
- [ ] Add a test asserting stable order + empty-field elision.

### 4. `orq-lite log` viewer (post-hoc parser over run.log JSONL)
- [ ] eventlog: export `HumanLine(Event) string`.
- [ ] `internal/commands/logcmd.go`: `Log(projectDir, w, opts)` reads run.log, renders a numbered timeline reusing HumanLine + AgentSummaryLine; flags `--role`, `--event`, `--expand N` (full tails), `--full`.
- [ ] Wire `log` subcommand + usage into main.go.
- [ ] Test logcmd parsing/filtering against a synthetic run.log.

### Verify
- [ ] `go build ./...`
- [ ] `go test ./...`
- [ ] Manual: run against a sample run.log; confirm readable output.

## Known limitation
`agent_run` events carry no `task_id` (roles like parser/reviewer aren't task-scoped),
so the viewer cannot reliably group agent runs under a task. Surfacing `task_id` only
on events that already have it (task_done, etc.). Threading task_id into agent_run is a
possible follow-up.

## Review

Done & verified (`go build ./...`, `go test ./...` — 16/16 packages ok, gofmt/vet clean):

1. **TTY-aware default** — `resolveLogFormat`/`isTerminal` in `runcmd.go`; `newLiveDeps` resolves
   ""/"auto" → human on a TTY, verbose when piped. Covers both `plan` and `run`. `run --log-format`
   default changed to `auto`.
2. **Enriched human agent_run** — `AgentSummaryLine` (prefers final_text_tail, falls back to
   stdout_tail, first line, ≤200 bytes) + indented `↳` continuation in `formatAgentRun`.
3. **Verbose fixes** — `summariseFields` now deterministic (role/agent first, tails last) and drops
   empty-string fields (keeps zero/false scalars for pipe consumers).
4. **`orq-lite log` viewer** — `internal/commands/logcmd.go`: numbered timeline reusing
   `eventlog.HumanLine` + `AgentSummaryLine`; `--role`, `--event`, `--expand N`, `--full`. Wired into
   main + usage. Tests in `logcmd_test.go`.

Bonus (user-reported): **`update`/`update --check` were 404ing** — `updateRepoName` was `orquestalite`
but the GitHub repo is `orquesta-lite`. Fixed; full update flow verified end-to-end against v0.1.3.

Not done (offered as follow-up): threading `task_id` into `agent_run` so the viewer can group runs
under a task.
</content>
</invoke>
