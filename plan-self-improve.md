# orquestalite self-improvement plan

This plan is executed by **orquestalite against its own repository**. The working
directory is the `orquestalite` Go module (`github.com/lionelchamorro/orquestalite`).
Full design rationale lives in `plan.md`; this file is the implementation backlog.

## Execution rules (apply to every task)

- **Language:** Go, standard library first. Match existing package style.
- **Green at every step:** each task must leave `go build ./...` and `go vet ./...`
  passing and **all** `go test ./...` green. Configure `team.json.full_test_command`
  to `go test ./...`.
- **Each task ships its own tests** (red→green→refactor). New behavior is not
  "done" until a test exercises it.
- **Do not break locked contracts:** ADR-0001 (CLI subprocess orchestration),
  ADR-0002 (JSON result contracts), ADR-0003 (remediation cascade). The result
  JSON shapes in `internal/results` must keep working unchanged.
- **One concern per task.** Prefer extending/adding over rewriting; only the
  keystone task (Phase 3) touches many files at once.
- Tasks are ordered by dependency. Earlier phases must land before later ones.

---

## Phase 1 — Config foundation (no dependencies)

### Task: Provider registry replaces the dual switch
Replace the provider `switch` in `internal/providers/provider.go` (`New`) and the
parallel known-provider check in `internal/config/config.go` with a single
`map[string]func() providers.Provider`. Each provider self-registers from an
`init()` in `claude.go` and `codex.go`. `providers.New(name)` and config
validation both read this map.
- **Acceptance:** `providers.New("claude")` and `providers.New("codex")` return
  the right adapters; an unknown name returns an error; config validation rejects
  an agent whose `provider` is unregistered; adding a provider requires editing
  only its own file. Existing provider tests still pass; a new test asserts the
  registry rejects an unknown provider.

### Task: Typed RoleSpec/AgentSpec and config.Resolve
Add value types `AgentSpec` (provider, model, effort, skip-perms, rate-pattern,
cmd) and `RoleSpec` (ordered `[]AgentSpec`, prompt path, result path, timeout,
escalation ladder, decompose prompt) to `internal/config`. Add
`Resolve() (map[string]RoleSpec, error)` that validates at load time:
- every orchestrated role (`parser/coder/tester/critic/reviewer`) is present;
- every agent named in each role's `agents` **and** `escalation_ladder` exists in
  the agent pool;
- each agent has a registered `provider` or a non-empty `cmd`;
- prompt and result paths are non-empty.
Resolve **must not** stat the decompose-prompt file.
- **Acceptance:** a valid `team.json` resolves; a missing orchestrated role errors;
  a typo in an `escalation_ladder` agent name errors at resolve time; a config
  whose `decompose_prompt` points at a non-existent file still resolves cleanly.
  This task is additive — no existing caller switches to `RoleSpec` yet, build
  stays green.

---

## Phase 2 — Standalone building blocks for the invoke package (no dependencies)

### Task: AgentRunner seam over RunAgent
Define `type AgentRunner interface { Run(ctx context.Context, s runner.Spec) (*runner.Result, error) }`
and an `execRunner` whose `Run` calls the existing `runner.RunAgent`. Do not change
`RunAgent` itself.
- **Acceptance:** `execRunner` satisfies `AgentRunner`; a test invokes a fake CLI
  through `execRunner` and gets the same `runner.Result` as calling `RunAgent`
  directly.

### Task: Pure classify function for fallback disposition
Create the `internal/invoke` package and add a pure
`classify(r *runner.Result) (shouldFallback bool, reason string)` implementing the
precedence `rate_limit → agent_crashed (timed_out) → result_missing → success`
(lifted verbatim from the switch at `internal/commands/runcmd.go:363–383`). Replace
that inline switch in `callRole` with a call to `classify`. No behavior change.
- **Acceptance:** unit tests cover all four branches from `runner.Result` literals
  (no subprocess); existing `runcmd` / e2e tests still pass.

### Task: results.Archive writes the immutable per-task copy
Add `results.Archive(dir, role string, rc invoke.RunContext, raw []byte) error`
(or an equivalent signature avoiding an import cycle — put `RunContext` in `invoke`
and pass its fields if needed) that writes
`.orquestalite/results/by-task/<id>/<role>.c<cycle>.a<attempt>.json`, using
synthetic ids `_plan` for parser and `_review` for reviewer. Never touches the
canonical latest file.
- **Acceptance:** archiving two attempts for one task produces two distinct files;
  parser archives under `by-task/_plan/`, reviewer under `by-task/_review/`; the
  canonical `results/<role>.json` is untouched. No GC.

### Task: MemoryNote() on every result type
Add a one-line `MemoryNote() *string` method to each `*Result` in
`internal/results` returning its `NotesForMemory`, and a `MemoryNoting` interface
in `internal/invoke` that they satisfy.
- **Acceptance:** all five result types satisfy `MemoryNoting`; a compile-time
  assertion or test confirms it. Additive, build green.

---

## Phase 3 — Keystone: the RoleInvoker (depends on Phases 1–2)

### Task: RoleInvoker and generic Role[T], migrate all six role calls
In `internal/invoke`, add `RoleInvoker` (holding resolved specs, `dir`,
`fallback.Caller`, `eventlog.Logger`, `agenthealth.Tracker`, `memPath`, and an
`AgentRunner`), `RunContext{TaskID, Cycle, Attempt}`, and the generic free
function:
```go
func Role[T MemoryNoting](ctx context.Context, inv *RoleInvoker, role string,
    vars map[string]string, rc RunContext, parse func(path string) (*T, error)) (*T, error)
```
`Role` owns the full lifecycle: load+interpolate prompt, run the engine
(health-filter chain → `fallback` → `classify` via `AgentRunner`), **archive the
result** (`results.Archive`, Phase 2), read the result file, call the passed-in
`parse`, extract the note via `MemoryNote()`, append to memory. Fold the
prompt-load/interpolate and memory read/append logic into `invoke` so
`internal/prompts` and `internal/memory` are no longer imported by
`internal/commands`. Rewrite `liveDeps.RunParser/RunCoder/RunTester/RunCritic/
RunReviewer/Decompose` as one-line `invoke.Role(...)` callers. Thread `RunContext`
through the `loops.RoleRunner`/`TaskDeps`/`ReviewDeps` signatures (Attempt from the
fix loop's iteration counter, Cycle from the review loop).
- **Acceptance:** the six methods are each a single `invoke.Role` call; `callRole`
  no longer exists as an inline closure; `internal/commands` no longer imports
  `internal/prompts` or `internal/memory`; per-role skeleton duplication is gone;
  results are archived per task/attempt during a run; **all existing tests pass**,
  including `internal/loops` stub tests and `internal/commands/e2e_scenarios_test.go`.
- **Note:** this is the largest task. If it cannot be completed atomically, it is a
  valid candidate for orq-lite's auto-decomposition (ADR-0003) into: (a) invoke
  package + RoleInvoker + Role[T] with one role migrated, (b) migrate remaining
  roles, (c) thread RunContext through loops, (d) wire archive + remove prompts/
  memory imports — each kept build-green.

---

## Phase 4 — Wiring constructor (depends on Phase 3)

### Task: Scope the static agent preflight to chosen roles
Change `runStaticAgentPreflight` to accept the list of roles to check and only
verify the agents referenced by those roles (and their escalation ladders).
- **Acceptance:** a test confirms that passing `["parser"]` checks only the
  parser's agents and ignores agents used solely by other roles.

### Task: newLiveDeps constructor; route Run and Plan through it
Add `newLiveDeps(opts liveDepsOptions) (*liveDeps, func() error, error)` where
`opts` carries `ProjectDir, TeamPath, LogFormat, Roles []string`. It performs
`config.Load` → `config.Resolve` → open log → build `fallback.Caller` →
`agenthealth.Tracker` → scoped preflight → build `*invoke.RoleInvoker` (resolved
specs + `execRunner`) → load-or-empty `tasks.json` → assemble `liveDeps`; returns a
cleanup that closes the log. Route `Run` (Roles = all) and `PlanWithLiveCaller`
(Roles = `["parser"]`) through it and delete the duplicated wiring.
- **Acceptance:** `Run` and `Plan` behave as before (e2e tests pass); the wiring
  exists in exactly one place; `PlanWithLiveCaller` no longer re-assembles
  `liveDeps`; the returned cleanup closes the log; preflight in `Plan` reports only
  parser agents.

---

## Phase 5 — Reviewer rubric (independent; can run any time after Phase 1)

### Task: Embed the thermo-nuclear review rubric and ship it via init
Add `prompts/_review-rubric.md` containing the
thermo-nuclear-code-quality-review criteria (structural simplification / code-judo,
file-size smell past ~1000 lines, spaghetti/branching growth, layer discipline,
direct-over-magical, type-contract clarity, approval blockers). Header note: the
upstream URL and "derived; re-sync if it changes". Have `orq-lite init` materialize
this file alongside the other prompts, and reference it from `prompts/reviewer.md`.
- **Acceptance:** `orq-lite init` writes `prompts/_review-rubric.md`;
  `prompts/reviewer.md` references it; `internal/commands/initcmd_test.go` is
  extended to assert the rubric file is created.

### Task: Give the reviewer the cycle diff and the findings rules
Add a `{{CYCLE_BASE_SHA}}` interpolation variable plumbed from the review loop
(the SHA at the cycle start) into the reviewer vars. Update `prompts/reviewer.md`
to instruct the reviewer to inspect `<CYCLE_BASE_SHA>..HEAD` and open touched files
via its own tools, apply the rubric, turn actionable findings into `new_tasks` with
`priority` by severity (structural blocker → 1, smell → 2), and **never set
`should_stop: true` in a cycle where it reported a structural regression**.
- **Acceptance:** the reviewer prompt receives a real `CYCLE_BASE_SHA`; a loop test
  asserts the variable is populated; the `reviewer.json` contract is unchanged
  (findings flow through existing `new_tasks` / `should_stop` fields).

---

## Suggested team.json for the self-run

- `full_test_command`: `go test ./...`
- Roles bound to capable agents (e.g. `coder` → sonnet with opus/codex fallback,
  `critic`/`reviewer` → opus), matching the defaults `orq-lite init` writes.
- The reviewer agent should be a provider with git + file tooling so it can pull
  the diff itself (Phase 5).
