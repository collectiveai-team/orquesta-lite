# orquestalite — architecture & workflow improvement plan

Status: defined (grilled 2026-06-02) · Branch base: `feat/init-defaults-and-preflight`

Vocabulary follows `LANGUAGE.md` (module / interface / implementation / depth /
seam / adapter / leverage / locality) and the domain in `CONTEXT.md` — including
the two terms added during this grill: **result archive** and **review rubric**.

## Scope

Six improvements: four architectural deepenings (1–4) plus two workflow changes
— how work is tracked (5) and a reviewer prompt change (6).

**Intentionally excluded (the ralph things):** the GitHub-issues Backlog seam
and the ralph prompt borrowings (priority ladder, tracer-bullet wording, commit
message contract, `NO MORE TASKS` marker). The task source stays `tasks.json`.

## Sequencing

1. **Item 4 (config resolve)** + **Item 3 (newLiveDeps)** land together — the
   constructor injects the resolved specs.
2. **Item 1 (RoleInvoker)** — the keystone; Items 2 and 5 live inside it.
3. **Item 2 (AgentRunner)** — inside Item 1; unlocks pure disposition tests.
4. **Item 5 (result archive)** — one call inside `Invoke`.
5. **Item 6 (reviewer rubric)** — independent prompt change; any time.

---

## 1. Collapse the role-invocation pipeline into one deep module — `internal/invoke`

**Strength:** Strong (keystone).

**Problem.** Six methods — `RunParser`/`RunCoder`/`RunTester`/`RunCritic`/
`RunReviewer`/`Decompose` (`internal/commands/runcmd.go:240–791`) — copy the same
skeleton (`prompts.Load → memory.ReadAll → Interpolate → callRole → results.ParseX
→ memory.Append`) around an inline 170-line engine (`callRole`, `runcmd.go:300–470`).
Shallow: each method's interface ≈ its body. `prompts` (23 lines) and `memory`
(38 lines) are public only because that skeleton needs them.

**Design (resolved).**

- New package **`internal/invoke`**.
- A generic **free function** (Go methods can't take type params):
  ```go
  func Role[T MemoryNoting](
      ctx context.Context,
      inv *RoleInvoker,
      role string,
      vars map[string]string,
      rc RunContext,
      parse func(path string) (*T, error),
  ) (*T, error)
  ```
- `Role` owns the whole lifecycle: load + interpolate prompt → run engine
  (chain health-filter → fallback → `classify`, see Item 2) → **archive result**
  (Item 5) → read result file → call the **passed-in** `parse` (keeps
  role-specific validation as a real seam) → extract note → `memory.Append`.
  `prompts` and `memory` become **internals** of this package.
- `MemoryNoting` constraint: every result struct gains a one-line
  `MemoryNote() *string` (returns its `NotesForMemory`).
- **`RunContext{TaskID, Cycle, Attempt}`** passed per call (not mutated state):
  reentrant, trivially testable, and gives Item 5 the `Attempt` for free.
  `Attempt` is sourced from the fix loop's iteration counter.
- `RoleInvoker` holds the engine deps: resolved specs (Item 4), `dir`,
  `fallback.Caller`, `eventlog.Logger`, `agenthealth.Tracker`, `memPath`, and the
  `AgentRunner` (Item 2). `liveDeps` keeps only orchestration glue + an
  `*invoke.RoleInvoker`; its `Run*` methods shrink to one-liners, e.g.
  `invoke.Role(ctx, d.inv, "coder", vars, rc, results.ParseCoder)`.

**Wins.** locality (invocation bugs in one module) · leverage (one interface, six
call sites) · interface shrinks; implementation absorbs `prompts`+`memory` ·
~150 duplicated lines deleted · one test surface.

**Checklist.**
- [ ] Create `internal/invoke` with `RoleInvoker`, `RunContext`, `MemoryNoting`,
      and the generic `Role[T]`.
- [ ] Add `MemoryNote() *string` to each `results.*Result`.
- [ ] Move `callRole`'s body into the engine; fold `prompts`/`memory` calls inside.
- [ ] Rewrite the six `Run*`/`Decompose` methods as one-line `invoke.Role` callers.
- [ ] Thread `RunContext` through `loops.RoleRunner` signatures (fix/task/review).
- [ ] Tests: drive `Role` once via a stub; delete per-role skeleton duplication
      from `runcmd_test.go`.

---

## 2. Put a seam under the subprocess — `AgentRunner` + pure `classify`

**Strength:** Worth exploring (lands inside Item 1).

**Problem.** `runner.RunAgent` (`internal/runner/runner.go:140`) is a package-level
function called inline in `callRole`; the failure-mode classification fused with
it can only be exercised by real shell scripts (`runcmd_test.go`, skipped on
Windows).

**Design (resolved).** Verified: `runner.Result` already carries `RateLimited`,
`TimedOut`, `ResultExists`, `ExitCode` (runner.go:36–50) and the disposition
switch (`runcmd.go:363–383`) is already pure over those four fields — **no
widening needed.**

- Seam at the raw result:
  ```go
  type AgentRunner interface { Run(ctx context.Context, s runner.Spec) (*runner.Result, error) }
  ```
  `execRunner` = today's `RunAgent`; `fakeRunner` returns canned `runner.Result`s.
- Lift the switch into a **pure** function in `internal/invoke`:
  ```go
  func classify(r *runner.Result) (shouldFallback bool, reason string)
  ```
  Precedence preserved: `rate_limit → agent_crashed (timed_out) → result_missing
  → success`. The health/event/`LastAgent` writes stay in the engine as *effects*,
  not classification.
- Inject `AgentRunner` into `RoleInvoker` (default `execRunner`).

**Wins.** test surface: disposition logic with zero subprocesses · tests stop
skipping on Windows · two adapters justify the seam (exec / fake) · locality
(failure-mode rules in one pure function).

**Checklist.**
- [ ] Define `AgentRunner`; make `RunAgent` the `execRunner`.
- [ ] Extract pure `classify` from `runcmd.go:363–383`.
- [ ] Add `fakeRunner`; unit-test classify exhaustively + `Role` end-to-end
      without a shell.

---

## 3. One constructor for the wiring harness — `newLiveDeps`

**Strength:** Worth exploring.

**Problem.** `liveDeps` is assembled twice — `Run` (`runcmd.go:52–94`) and
`PlanWithLiveCaller` (`plancmd.go:21–65`) — with drift (different eventlog opener;
Plan skips task-load, preflight, and `health`). Adding a field means editing both.

**Design (resolved).**
```go
func newLiveDeps(opts liveDepsOptions) (*liveDeps, func() error, error)
// opts: ProjectDir, TeamPath, LogFormat, Roles []string
```
Does: `config.Load` → `config.Resolve` (Item 4) → open log → `fallback.Caller` →
`agenthealth.Tracker` → **scoped static preflight** → build `*invoke.RoleInvoker`
(resolved specs + `execRunner`) → load-or-empty `tasks.json` → assemble
`liveDeps`. Returns a `cleanup` (closes the log) the caller `defer`s. `Run` and
`PlanWithLiveCaller` both reduce to: build opts → `newLiveDeps` → `defer cleanup()`
→ delegate.

**Preflight = option A-scoped.** `runStaticAgentPreflight` takes the **roles the
command will use** and checks only their agents. `Run` passes all roles; `Plan`
passes `["parser"]`. Preflight runs always (no boolean knob), but reports only
agents relevant to the command — Plan gains the visibility net without noise about
the `coder`/`tester`/etc. it never invokes.

**Wins.** locality (wiring + preflight scope in one place) · duplicate init
deleted from `plancmd` · Plan inherits fail-fast visibility, scoped.

**Checklist.**
- [ ] Add `newLiveDeps` + `liveDepsOptions` returning `(*liveDeps, cleanup, error)`.
- [ ] Change `runStaticAgentPreflight` to accept the roles to check.
- [ ] Route `Run` (all roles) and `PlanWithLiveCaller` (`parser`) through it;
      delete duplicated assembly.

---

## 4. Resolve config into typed role specs at startup

**Strength:** Speculative (do with Item 3).

**Problem.** Raw `config.Config` leaks into every `liveDeps` method; role lookup
by string key (`d.cfg.Roles["coder"]`) silently returns a zero `Role{}` on a typo,
failing weirdly mid-run. Providers need a dual switch (`providers/provider.go`
factory + `config.go:104` validation).

**Design (resolved).**
- New value types `RoleSpec` (ordered `[]AgentSpec`, prompt path, result path,
  timeout, escalation ladder, decompose prompt) and `AgentSpec` (provider, model,
  effort, skip-perms, rate-pattern, cmd). `invoke` and the loops depend on these,
  never on `config.*` maps.
- `config.Resolve() (map[string]RoleSpec, error)` validates at load:
  - every orchestrated role (`parser/coder/tester/critic/reviewer`) present;
  - every agent in each `agents` **and `escalation_ladder`** exists in the pool;
  - each agent's `provider` is registered (or a `cmd` is given);
  - prompt/result paths non-empty.
  - **Does not** stat the decompose-prompt file — file existence is a different
    failure class, checked where prompts are loaded (same as role prompts).
- **Provider registry:** replace both switches with a `map[string]func() Provider`
  populated by `init()` in `claude.go`/`codex.go`. `providers.New` and validation
  read the one map.

**Wins.** fail fast (bad `team.json` caught at load) · config shape stops leaking
past startup · new provider self-registers, no dual switch · locality (validation
in one resolver).

**Checklist.**
- [ ] Add `RoleSpec`/`AgentSpec` + `config.Resolve` with the validations above
      (incl. ladder agents; excl. decompose-file stat).
- [ ] Replace `d.cfg.Roles[…]`/`d.cfg.Agents[…]` reaches with resolved specs.
- [ ] Convert `providers.New` to a registry map; `init()` self-registration; drop
      the `config.go` switch.

---

## 5. Result archive — per-task, never overwritten

**Strength:** Strong (operator-facing).

**Problem.** Each role writes one fixed path (`.orquestalite/results/coder.json`)
and `runner.go:147` removes it before every run. **Every coder invocation across
every task and attempt clobbers the previous one** — per-task work survives only
as a `result_snapshot` line in `run.log` (`eventlog.go:191`), not as a durable
artifact.

**Design (resolved).** One archival call inside `invoke.Role`, right after `parse`
succeeds. Keeps the canonical latest file (control flow / ADR-0002 contract
unchanged) and **also** writes an immutable copy.

- **Archive every parseable result, all roles** — including `blocked` coders and
  `rejected` critics (prime debugging value). Attempts that wrote nothing
  (`result_missing`, timeout) have no file to copy and are skipped (their story is
  in `run.log`).
- **Path — everything under `by-task/`:**
  ```
  .orquestalite/results/coder.json                       # canonical latest (unchanged)
  .orquestalite/results/by-task/T003/coder.c1.a2.json    # archived (task roles)
  .orquestalite/results/by-task/T003/decompose.c1.a1.json
  .orquestalite/results/by-task/_plan/parser.c0.a1.json     # synthetic id, non-task
  .orquestalite/results/by-task/_review/reviewer.c1.a1.json # synthetic id, non-task
  ```
  Synthetic ids `_plan` / `_review` can't collide with real ids (`T001`…).
  `RunContext` supplies `TaskID`/`Cycle`/`Attempt`; `Role` builds the path.
- **No GC in v1** — gitignored runtime state, tiny JSON; `orq-lite reset` already
  wipes `.orquestalite/`.

**Wins.** locality (no lost work; one archival point) · per-task history is a real
artifact, not just a log line · zero change to the result contract or fix-loop
control flow.

**Checklist.**
- [ ] Add `results.Archive(dir, role, rc, rawBytes)` (or inline in `Role`) writing
      `by-task/<id>/<role>.c<cycle>.a<attempt>.json`, ids `_plan`/`_review` for
      non-task roles.
- [ ] Call it in `Role` after `parse` succeeds; never on the clobber path in
      `runner.go`.
- [ ] Surface latest-per-task in `orq-lite status`.
- [ ] Test: two tasks → two retained archives; canonical latest still holds the
      most recent; a `rejected` critic is archived.

---

## 6. Reviewer applies the thermo-nuclear review rubric each cycle

**Strength:** Worth exploring (prompt change).

**Problem.** The `reviewer` role has no structured quality rubric, and it only
receives `git log <cycle_start>..HEAD --stat` — file names + line counts, not the
diff. A code-quality rubric can't judge "spaghetti branching" or "file > 1000
lines" from `--stat`.

**Design (resolved).**
- **Wiring A — vendor the rubric (provider-agnostic).** Copy the
  [`thermo-nuclear-code-quality-review`](https://github.com/cursor/plugins/blob/main/cursor-team-kit/skills/thermo-nuclear-code-quality-review/SKILL.md)
  criteria into `prompts/_review-rubric.md` (header notes the upstream URL +
  "derived; re-sync if it changes"), shipped by `orq-lite init`; `prompts/reviewer.md`
  references it. No dependency on `claude`-only skill discovery (keeps ADR-0001's
  provider neutrality).
- **Diff access — agent pulls it itself.** Add a `{{CYCLE_BASE_SHA}}`
  interpolation var (the orchestrator already knows the cycle start). The prompt
  instructs the reviewer to inspect `<base>..HEAD` and open the touched files via
  its own git/file tools — small prompt, real file contents for the 1000-line check.
- **Findings → existing contract (ADR-0002 unchanged).** Each actionable finding
  becomes a `new_tasks` entry with `priority` by severity (structural blocker → 1,
  smell → 2); context-only notes → `notes_for_memory`. **Guard rule:** a structural
  regression forbids `should_stop: true` that cycle (orquestalite's translation of
  the skill's "withhold approval").

**Wins.** reviewer gains a sharp, repeatable, versioned rubric · structural
regressions become remediation tasks · contract + provider neutrality preserved.

**Checklist.**
- [ ] Add `prompts/_review-rubric.md` (from the skill, with origin note); ship via
      `orq-lite init`.
- [ ] Reference the rubric in `prompts/reviewer.md`; add `{{CYCLE_BASE_SHA}}` and
      the "pull the diff yourself" instruction.
- [ ] Plumb `CYCLE_BASE_SHA` from the review loop into the reviewer vars.
- [ ] Encode severity→priority mapping and the structural-regression `should_stop`
      guard in the prompt.
- [ ] (Optional, later) same rubric for the per-task `critic`.

---

## Docs touched

- `CONTEXT.md` — added glossary terms **result archive** and **review rubric**.
- Possible ADR (offered, not yet written): "Reviewer rubric embedded as a prompt,
  not invoked as a live skill" — records the A-over-B trade-off so a future reader
  doesn't re-litigate "why not just use the skill?". See ADR-0001 for the
  provider-neutrality rationale it builds on.
