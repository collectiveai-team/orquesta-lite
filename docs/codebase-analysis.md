# orquestalite — Deep Codebase Analysis

Audit date: 2026-06-26
Scope: ~18,400 lines of Go (~55% tests) over `cmd/`, `internal/` (commands, loops, runner, invoke, providers, fallback, agenthealth, sessions, cost, eventlog, results, tasks, memory, gitx, preflight, factory, config, web, prompts, handoff).
Reference spec: `CONTEXT.md`.

This document is a critical review: what is implemented, whether it follows good development patterns, structural quality, over-engineering, dead code, and features implemented wrong or incompletely. Findings are grouped and cite `file:line` references. It makes no code changes.

---

## 0. Executive summary

The codebase is **substantially larger than the spec**. `CONTEXT.md` describes a focused single-binary CLI orchestrator with a 7-command surface and a small set of roles. The implementation adds, undocumented: task decomposition, escalation ladders, agent-health auto-skip, session resume, memory compaction, setup/generic "squad" lanes + a `generalist` role, preflight task validation, a lint gate, factory repair loops, PR publishing, cost tracking via `agtop`, a self-update subsystem, and a cosmetic React "gameboard" dashboard. Several of these are good engineering judgment; several are scope creep that quietly rots.

The strongest parts of the codebase are the **`factory`** package (clean queue machine with a tight `Deps` interface), the **`eventlog`** dual-output logger with 50 MB rotation, the **`results`** typed-contract layer, and **`gitx`** rollback semantics. The weakest parts are:

- **`internal/invoke`** is a god-package (~8 responsibilities).
- **`internal/commands/runcmd.go`** (979 lines) is a god-file holding a `liveDeps` god-struct that implements two god-interfaces (`TaskDeps`, `ReviewDeps`) with ~20 methods.
- **Two sources of truth for assets** (`internal/commands/assets/` vs repo-root `team.json`/`prompts/`/`schemas/`) plus an inconsistent `.gitignore` that **silently breaks a fresh clone**.
- **Drift between code and `CONTEXT.md`**: rate-limit exhaustion no longer fails tasks (`ReasonRateLimitExhausted` is dead), the reviewer is not gated against open verifier failures, the `lint_failed` failure reason is undocumented, the `coder.blocked` status is unhandled by the loop, and the `run plan.md` AFK mode is unimplemented.

---

## 1. Architecture & development patterns

### 1.1 Package boundaries

| Package | Verdict |
|---|---|
| `factory` (engine, factory) | **Good.** Queue machine with a focused `Deps` interface; no leakage of git/agent types. The best-bounded package. |
| `eventlog` | **Good.** Mutex-protected JSONL + human output, 50 MB rotation (`eventlog.go:17,98`), forward-compat unknown-event fallback (`eventlog.go:126`). |
| `results` | **Good.** Typed contracts with per-role validators that enforce CONTEXT invariants (tester requires `command_run` `results.go:280`, verifier requires ≥1 check + failed check on `fail` `results.go:308-323`). |
| `gitx` | **Good.** Rollback preserves pre-existing untracked files correctly (`gitx.go:99-120`), matches the spec. |
| `runner` | **Mostly good.** Clean subprocess launcher; provider knowledge leaks in via `parseCodexHeader` (`runner.go:91-102`) and per-call regex compilation (`runner.go:297`). |
| `invoke` | **Poor — god-package.** One package does: prompt assembly, agent selection, fallback/backoff driving, agent-health updates, session resume, memory injection, conventions injection, result archiving, classification, logging. See §1.3. |
| `commands` (esp. `runcmd.go`) | **Poor — god-file/struct.** See §1.4. |
| `loops` | **Mixed.** Generic loop logic is correct but the loop package is **silent** on the lifecycle events the spec attributes to it (`cycle_start`/`cycle_end`/`task_start`/`task_done`/`task_failed` are emitted ad-hoc by the concrete `liveDeps` adapter, not the loops). |
| `config` | **Mixed.** Clean defaults/getters, but `Validate()` and `resolve*Spec()` double-check the same role/agent invariants — see §1.5. |

### 1.2 Interface design

- `provider.Provider` (`provider.go:43`) is a tight 3-method surface + `registerProvider` registry. Good.
- `loops.TaskDeps` (`task.go:46-72`) is a **god-interface**: 11 methods spanning fix, commit, rollback, persistence, decomposition, preflight, single-role, role-presence. Every new feature adds a method here and a branch to `liveDeps`. Recommend splitting into `TaskRunner` / `TaskRepo` / `GitOps` / `TaskRecovery` / `TaskGuard`.
- `invoke` has `ExecRunner` + `AgentRunner` (`role.go:23-40`) with exactly one production impl and test fakes. A function field `func(ctx, Spec) (*Result, error)` would remove the interface, the `ExecRunner` struct, and the `runner()` method (`role.go:342`). Light over-engineering. Same for `SessionStore` (`role.go:30-34`).
- `invoke.Role[T MemoryNoting]` (`role.go:79`) is generic over a one-method interface solely to type the `MemoryNote()` accessor. The type parameter flows nowhere else — marginal value for the complexity; a runtime interface check would be simpler.

### 1.3 `internal/invoke` is a god-package

Responsibilities packed in: prompt assembly + interpolation, agent selection + override, fallback + backoff loop, agent-health updates, session resume, memory read/append, conventions injection, result archiving, output classification, structured logging. `CONTEXT.md` treats "invoke a role" as a single concept; the package has accreted ~8. Suggested split: `prompt` (assembly + interpolation + conventions), `agentcall` (selection + fallback + health + sessions), and move `classify` next to `runner`.

Symptom: `Role[T]` (`role.go:79`) vs the non-generic `RunOnce` (`role.go:157`) duplicate the prompt-build/load/interpolate/run sequence almost line-for-line (`role.go:94-120` ≈ `role.go:165-187`). A shared `buildPrompt` helper would collapse them.

`RoleInvoker` (`role.go:42-69`) is a 15-field wiring hub; several fields (`OnAgentSuccess`, `SessionNamespace`, `ResumeRoles`, `AgentHealthThreshold`) are optional mutable knobs that could be configured behaviors instead.

### 1.4 `internal/commands/runcmd.go` — god-file & god-struct

`liveDeps` (`runcmd.go:294-313`) holds 15 fields and implements **both** `TaskDeps` and `ReviewDeps`, plus concrete commit/rollback/full-suite/handoff/preflight/decompose/run-single. One type owns almost every integration responsibility. Split targets: `liveRunner` (fix-loop role runners, `runcmd.go:847-979`), `liveGit` (commit/rollback/cycle-base, `runcmd.go:439-507`), `liveReview` (parser/cycle-verify/reviewer, `runcmd.go:511-606`), `liveTask` (decompose/run-single/preflight, `runcmd.go:638-752`), plus `newLiveDeps` wiring (`runcmd.go:107-226`).

### 1.5 Duplicated validation

- `config.Validate()` (`config.go:245-313`) and `resolveRoleSpec()`/`resolveAgentSpec()` (`config.go:315-342`) recheck the same role/agent invariants. `Load()` always `Validate()`s (`config.go:176`), then callers `Resolve()` — **double validation**, and the two passes can drift (e.g. `Validate` checks the `Mode` enum at `config.go:271-275` but `resolveRoleSpec` does not).
- `doctorcmd.usedProviders` / `runDoctorChecks` (`doctorcmd.go:103-164,189-204`) and `runStaticAgentPreflight` (`runcmd.go:234-291`) both walk role agents + escalation ladders to check binaries/credentials with no shared helper.

## 2. Duplicated logic (concrete)

1. **Prompt-var map assembly** is copy-pasted across 8 sites in `runcmd.go`: `RunCoder` (`865-875`), `RunTester` (`890-896`), `RunCritic` (`962-968`), `RunVerifier` (`937-943`), `RunSingle` (`657-668`), `Decompose` (`721-728`), `CycleVerification` (`538-545`), `RunReviewer` (`595-601`). Each builds `map[string]string{TASK_ID, TASK_TITLE, TASK_DESCRIPTION, ATTEMPT_NUMBER, …}` with overlapping keys. No shared `defaultVars(rc, task, extras)` helper. Single biggest duplication.
2. **`currentTask` lookup-by-ID** loop appears in `RunFix` (`runcmd.go:341-346`) and `RunSingle` (`runcmd.go:644-649`). `loops.findTaskIdx` (`task.go:101`) exists but is unexported, so `commands` reinvents it.
3. **`gitx.LogStat(cycleBaseSHA)`** called twice per cycle — in `CycleVerification` (`runcmd.go:527`) and `RunReviewer` (`runcmd.go:589`). Should be cached on `liveDeps` once per cycle.
4. **Two sha256 helpers**: `sha256OfFailures` (`runcmd.go:755`) and `sha256Of` (`runcmd.go:761`).
5. **Shell-command execution split personality**: `FullSuite` uses `strings.Fields` + `exec.Command(parts...)` (`runcmd.go:368`) — breaks on any quoted/compound `full_test_command`. `runShell`/trust-but-verify uses `sh -c` (`runcmd.go:775`, `runcmd.go:909-924`). `lintGateOutcome` (`runcmd.go:398-423`) is a third independent `exec.CommandContext` site. A shared `runCmd(ctx, cmd, timeout)` helper would dedupe all three and bring `FullSuite` to `sh -c` parity.
6. **`run.log` reading** is implemented three independent times: `web/tail.go`, `logcmd.go`, `statuscmd.go`. No shared scanner.
7. **`cost.Rollup`** payload assembly duplicated in `costcmd.go:29` and `server.go:270`.
8. **Atomic write**: `writeFileAtomic` exists in `runcmd.go:839` but `factory.Save` (`factory.go:138-148`) writes `factory.json` non-atomically — and `factory.json` is the *resume* state, so a mid-write crash is expensive.

## 3. Dead code

- `internal/commands/agent_runner.go` — **fully dead**. Defines `AgentRunner` + `execRunner`; nothing imports it (the live runner interface is `invoke.RoleRunner`/`AgentRunner`). Delete.
- `commands.Init` (`initcmd.go:26-28`) — unused; `main.go` calls `InitWithOptions`. Delete.
- `fallback.ErrRateLimitExhausted` (`fallback.go:71`) + `tasks.ReasonRateLimitExhausted` (`tasks.go:36`) — exported, **never returned by any production path**. Rate limits now wait out cooldowns instead of abandoning; the const is retained only for stale doc references. Either restore the fail path or remove.
- `agenthealth.ReasonRepeatedFailures` (`agenthealth.go:18`) and `Tracker.Skipped()` (`agenthealth.go:111-119`) — defined but only used in tests. Production only ever sets `ReasonResultMissing`/`ReasonAuth`/`ReasonUnreachable`/`ReasonNoCredentials`.
- `tasks.FailureDetails.{ConfigSuspect, ModelSuspect}` (`tasks.go:57-58`) and `FailureDetails.AgentChain` (`tasks.go:56`) — never populated outside tests. Consequently the handoff report renders "Config suspect: no / Model suspect: no" (`handoff.go:50-51`) and "(no agent run data captured)" (`handoff.go:55-64`) literally always — **decorative features in production**.
- `schemas/*.json` (both copies) — no runtime consumer. `results.ParseX` hard-codes validation in Go (`results.go:234-334`). The schemas are pure maintenance overhead and a second source of truth that can drift (and silently does — see §5.2).
- `opencode.go:39` `if model != ""` is dead — `model` is always defaulted three lines above.
- `cost.Session.Duration` (`cost.go:35`) — parsed, never aggregated.
- `runner.Event.ToolArgs` — populated by all four providers but only a tool-call *count* is ever read into `run.log`; the args themselves are unused attribution.
- `invoke.Classify` (`classify.go:6`) — exported but only called inside its own package; the exported/unexported `Classify`/`classify` pair is needless indirection (cf. `runner.TailString`/`tailString`).

## 4. Over-engineering / speculative generality

- `invoke.Role[T MemoryNoting]` generic — flows nowhere. (§1.2)
- `invoke.ExecRunner`/`AgentRunner`/`SessionStore` interfaces with single production impls — replaceable with function fields / concrete types. (§1.2)
- `results.ReviewerResult.ShouldStop *bool` — actually justified (distinguishes absent-vs-false); not over-engineered.
- The **undocumented feature accretion** (decomposition, escalation ladders, agent-health, session resume, memory compaction, squads, preflight, lint gate, factory repair, PR publishing, cost tracking, codex header parsing, visual verification) each adds config knobs and code paths beyond CONTEXT.md. Individually defensible; collectively they make `TaskDeps`/`RoleInvoker`/`team.json` moving targets and the spec stale. Either update CONTEXT or prune.
- `internal/commands/updatecmd.go` (~336 lines incl. zip/tar/sha256/atomic-replace, Windows `.old` dance) — a full self-update subsystem with no mention in CONTEXT.md and a release-asset naming scheme nothing in this repo produces (no release workflow shown). Largest out-of-spec subsystem and the most likely to rot. Consider a separate binary or an ADR.
- React vendoring (~142 KB) solely for the cosmetic `/gameboard` view (§5.4) — scope creep.

## 5. Features implemented wrong or incomplete vs CONTEXT.md

### 5.1 `run plan.md` AFK mode is not implemented
CONTEXT.md: *"`run plan.md` — plan + run in one call (AFK mode)"* and *"`orq-lite run plan.md` with prior state asks for confirmation (or `--force`)."* The `run` case (`main.go:48-62`) defines only `--log-format`/`--serve`/`--addr` and **never inspects `fs.NArg()`**; a positional `plan.md` is silently ignored and the loop runs over the existing `tasks.json`. Clear spec gap.

### 5.2 Two asset sources of truth + a breaking `.gitignore`
- `internal/commands/assets/` (embedded via `initcmd.go:14` `//go:embed`) is the fresh-scaffold source.
- Repo-root `team.json` / `prompts/` / `schemas/` are the run-on-self source, written by `init` only `writeIfMissing`.
- `.gitignore` ignores `prompts/`, `schemas/`, `team.json`. Nine prompts were force-added but **`factory-planner.md`, `factory-visual-verify.md`, `memory-compactor.md` are untracked** — and the root `team.json` references all three. **A fresh `git clone` of this repo cannot run `orq-lite factory` / visual-verify / compaction against its own config.** This is an actively leaking bug.
- Root `team.json` declares **no** `generalist` role, while `assets/team.json` does (`:97`) and the code dispatches `SquadGeneric` to `RunSingle("generalist")` (`loops/task.go:140-146`, gated by `runcmd.go:629 HasRole`). The repo's own config silently disables a squad lane.

### 5.3 Reviewer not gated against verifier failures
CONTEXT.md §verifier: the reviewer *"may not set `should_stop` while failures remain"* and must convert each FAIL check into a priority-1 task. `loops/review.go:71-73` trusts `*rev.ShouldStop` unconditionally — the orchestrator does **not** enforce that a non-empty verifier FAIL list prevents stopping. An agent that ignores the rule can end the run prematurely.

### 5.4 `coder.blocked` is unhandled by the fix loop
The coder contract has `status: "completed" | "blocked"`, but `loops/fix.go:148` proceeds to the tester regardless of `blocked`. A blocked coder wastes a tester invocation and the loop never escalates `blocked` specially.

### 5.5 `lint_failed` is not in the `failure_reason` enum
`fix.go:130` returns `Reason: "lint_failed"` on final-iteration lint-gate failure. The CONTEXT enum is `{max_iterations, agent_repeated_failure, rate_limit_exhausted, commit_rejected, full_suite_failed, agent_crashed, invalid_result_json}`. The reviewer reads this field to decide remediation; an undocumented value is introduced silently.

### 5.6 Setup/generic squads skip the full-suite gate
`loops/task.go:148-167` routes `SquadSetup`/`SquadGeneric` via `RunSingle` → `commitTask` **without** calling `FullSuite`. CONTEXT §Test scope: *"before commit: the orchestrator runs the full test suite."* The lean lanes intentionally bypass the regression gate (undocumented divergence).

### 5.7 `FullSuite` can't run shell pipelines
`runcmd.go:367-391` parses `full_test_command` with `strings.Fields` and `exec.Command(parts...)`. Quoted args / `&&` pipelines break. The spec example `uv run pytest` works; anything more complex fails. Trust-but-verify (correctly) uses `sh -c` — inconsistent.

### 5.8 Spec lifecycle events owned by the wrong layer
`cycle_start`/`cycle_end`/`task_start`/`task_done`/`task_failed` (CONTEXT §Event types) are emitted ad-hoc by the concrete `liveDeps` adapter (`runcmd.go:473 task_done`, `:573 cycle_verification`), not by the generic `loops` package — which the spec treats as the natural owner. Events are scattered across the adapter.

### 5.9 Factory residues left dangling on merge conflict
`factory/engine.go` calls `CheckpointResidue` on the gate-fail path (`:220`) but not on the merge-conflict path (`:165-180`), where a conflict sets `StatusFailed` and returns to base — leaving uncommitted agent residue on the feature branch. Asymmetric.

### 5.10 `Decompose` overloads a sentinel
`Decompose` returns `ErrNoDecomposer` both when no decomposer is configured **and** when the agent returns a subtask count of 0 or >5 (`runcmd.go:734-740`). `loops/task.go:214` only checks `!errors.Is(ErrNoDecomposer)`, so a malformed agent result is indistinguishable from "feature absent." A distinct sentinel (e.g. `ErrDecomposeInvalidCount`) is warranted.

## 6. Latent bugs / subtle correctness

- **`liveDeps.currentTask` hidden-state coupling** (`runcmd.go:341-346, 644-649`): `RunFix`/`RunSingle` mutate `d.currentTask` as a side effect; `currentTaskTitle()`/`currentTaskDescription()` then read it in the role runners. There is **no task-not-found guard**: if the lookup loop misses, `d.currentTask` retains the *previous* task's pointer and the coder silently gets the wrong task's title/description. Add a not-found error path.
- **`fallback.OnWait` attributes waits to the wrong agent** (`fallback.go:131,172-174`): `lastAgent` is updated only when an agent is actually invoked. If the rate-limited agent was earlier in the chain and a different agent ran afterward, `OnWait` reports the cooldown against the wrong agent — a logging-correctness bug.
- **Escalation-ladder reset is subtle** (`fix.go:165-179`): `sameHashCount` resets to 1 on a *different* failure with a new `prevHash`, so `[identical, different, identical]` reaches escalation on the third. Logic works but the comment ("fire after 2") vs the actual reset semantics make it hard to verify.
- **`fb` reused without full reset** (`fix.go:37-45`): `Tester/Critic/Verifier/Lint` feedback fields are cleared piecewise per branch. A branch that early-`continue`s can leave stale fields. Zero `fb` then set only the active ones for safety.
- **`NextPending` returns a slice-alias pointer** (`tasks.go:158-172`): `&tl.Tasks[pending[0]]` is a pointer into the backing array; the rest of the code carefully re-acquires by index after `Append`/`Decompose`. Fragile — a `NextPendingIndex() int` would remove the dance.
- **`factory.Save` non-atomic** (`factory.go:138`) vs `writeFileAtomic` elsewhere — corrupt resume state on crash.
- **`preflight.go:58`** uses `fmt.Sprintf("%s/%s", workdir, m)` instead of `filepath.Join` — Windows portability smell.
- **Provider option-dropping**: `codex.Build` ignores `DangerouslySkipPerms` (always emits `--dangerously-bypass-approvals-and-sandbox`, `codex.go:33`); `gemini.Build` ignores `Effort` and `ForkSession` (`gemini.go`). Silent flag loss.
- **Per-run mutable provider state** (`Gemini`, `OpenCode` hold `strings.Builder`/`partOrder` accumulators) relies on `runner.buildLaunch` calling `providers.New` per invocation (`runner.go:217`) — documented per-impl but not enforced at the interface; a future re-user would silently corrupt accumulated text.

## 7. Error-handling quality

The codebase over-uses `_ =` and silent drops:

- `loops`: `d.SaveTasks(...)` and `d.Rollback(...)` errors ignored at ~12 sites (`task.go:127,134,157-158,163,207-208,232,239,251-252,263,269-270,273`, `review.go:68-69`). Defensible for "keep going" but **unlogged** — and `TaskDeps` exposes no `Log` method, so the loop has no way to surface a disk-full SaveTasks that diverges in-memory `tasks.json` from disk (the durable resume state).
- `invoke`: `memory.ReadAll` (`role.go:109,176`), `memory.Append` (`role.go:141`), `sessions.Set`/`Delete` (`role.go:304,306`) — all swallowed silently. A corrupt memory file silently yields empty memory; a session write failure is invisible.
- `runcmd.RunReviewer`: `tasksRaw, _ := os.ReadFile(d.tasksPath)` (`runcmd.go:585`) — empty `{{TASKS_JSON}}` injection on missing file.
- `runner.RunAgent`: `_ = err` on `cmd.Wait()` (`runner.go:208`) — documented contract.
- `eventlog.rotateLocked`: swallows all rotation errors (`eventlog.go:103-122`); a persistent open failure silently makes subsequent `Log` writes no-ops.
- `sessions.Load`: corrupt JSON resets *all* sessions to fresh (`sessions.go:35`).
- `testdetect.persistConfigString`: silently returns nil when the placeholder line isn't found (`testdetect.go:126`) — a hand-edited `team.json` silently skips persistence while working in-memory, confusing "my edit reverted" UX.
- `config.Load` double-validates (§1.5).

Recommendation: give `TaskDeps`/`RoleInvoker` a `Log` method (or accept a logger) and route silent drops through it. `doctor` (`doctorcmd.go`) is the gold standard for error UX — PASS/WARN/FAIL table with fix hints; the rest of the surface should approach that bar.

## 8. Sci-spec surface: implemented vs documented

CONTEXT.md CLI surface vs reality:

| Command | Status |
|---|---|
| `init` | Present (`--lang`, optional dir). Unconditional codex-not-found warning on stdout (`initcmd.go:51-53`) — pollutes machine output. |
| `plan [--append]` | Present, correct. |
| `run` | Present but **`run plan.md` AFK + `--force` missing** (§5.1). |
| `factory [--status|--force|--resume|--replan|--pr]` | Present; resume-by-default implemented (`factorycmd.go:88-104`). Reasonable superset. |
| `serve` | Present, real SSE dashboard (§8). |
| `status [--watch]` | Present. |
| `reset` | Present. |

Extra commands (not in spec):
- `doctor` — **legit ops helper** (preflight before spending money). Keep.
- `cost` + `/api/cost` — **legit ops helper** (supports the factory budget cap which *is* in config). Keep, consolidate with web handler.
- `log` — **legit ops helper** (`run.log` is canonical history). Keep.
- `update` / `upgrade` — **scope creep** (§4). Largest out-of-spec subsystem.
- `gameboard` (`/gameboard` + React vendor ~142 KB) — **scope creep / cosmetic**. The plain `index.html` dashboard already covers the spec's "tasks + factory queue + SSE events."
- `version` — trivial, fine.

## 9. Web dashboard (`internal/web`)

It is a **real** read-only dashboard, not a stub:

- Routes (`server.go:59-77`): `/api/tasks`, `/api/factory`, `/api/events` (SSE), `/api/cost`, `/api/result/{role}`, `/api/diff/{task}`, `/api/tasks/{feature}` + static server.
- SSE: `handleEvents` (`server.go:292-340`) sets `text/event-stream`, replays the last 100 log lines, polls a 500 ms ticker, 15 s `: ping` heartbeat. `tail.go` does offset-based incremental reads with rotation/partial-line handling. Genuine.
- Two frontends: `static/index.html` (vanilla JS dashboard, ~16 KB) and `static/gameboard.html` + `static/gameboard.js` (~43 KB, React) served at `/gameboard`. React is vendored **only** for the novelty gameboard — not the dashboard.
- Security hygiene present: `resultRoles` whitelist (`server.go:53-56`), `taskIDRe`/`shaRe` gating git input (`server.go:29-30`), half-written-JSON guards (`server.go:238-243`).
- Duplication: `/api/tasks` reads `tasks.json` directly (`server.go:218`); `handleCost` reimplements the `costcmd` rollup; `findTaskCommit` (`server.go:179-214`) is a bespoke log scan overlapping `logcmd.go`. Log rotation at 50 MB *is* implemented (`eventlog.go:17`), correcting an earlier uncertainty.

## 10. Race & shared-state notes

No data races identified in the audited paths:

- `runner.scanStdout`/`scanStderr` write disjoint `Result` fields from two goroutines; the main goroutine reads `Events`/`FinalText` only after `<-stdoutDone`/`<-stderrDone` (`runner.go:181-183`) — happens-after satisfied.
- `fallback.Caller.cooldown`, `agenthealth.Tracker`, `sessions.Store`, `eventlog.Logger`, `internal/memory` — all mutex-protected or single-threaded. (Note: `memory` has *no* mutex and relies on the single-threaded `invoke.Role` call sequence; if concurrency ever lands there, `memory.ReadAll`/`Append` and `readConventions` will race silently.)
- `liveDeps.currentTask` mutates from `OnAgentSuccess` (`runcmd.go:189-193`) but the fix loop is sequential — safe today.

## 11. Concrete remediation priorities

**P0 (correctness / ship-blockers)**
1. Fix the asset dual-source-of-truth + `.gitignore` mismatch so a fresh clone runs (track the referenced prompts, or require `init`; pick one asset source). (§5.2)
2. Implement `run plan.md` AFK + `--force`, or update CONTEXT to drop it. (§5.1)
3. Add the `currentTask` not-found guard so `RunCoder` can't silently get the wrong task. (§6)
4. Gate the reviewer's `should_stop` on the verifier report being all-pass (spec invariant). (§5.3)

**P1 (consistency / correctness-vs-spec)**
5. Handle `coder.blocked` in the fix loop (skip tester, escalate). (§5.4)
6. Make `lint_failed` part of the documented enum or map it to `max_iterations`. (§5.5)
7. Run `FullSuite` (or document the exception) before setup/generic commits; unify `FullSuite` on `sh -c`. (§5.6, §5.7)
8. Have `loops` emit `cycle_start`/`cycle_end`/`task_start`/`task_done`/`task_failed` so the spec-owned lifecycle isn't scattered across the adapter. (§5.8)
9. Restore the rate-limit-abandon path or remove `ErrRateLimitExhausted`/`ReasonRateLimitExhausted` and update CONTEXT. (§3)
10. Call `CheckpointResidue` on the merge-conflict exit. (§5.9)
11. Distinguish `ErrDecomposeInvalidCount` from `ErrNoDecomposer`. (§5.10)
12. Fix `fallback.OnWait` agent attribution. (§6)

**P2 (structure / hygiene)**
13. Split `runcmd.go`'s `liveDeps`; break `TaskDeps` into focused interfaces. (§1.4, §1.2)
14. Split `internal/invoke` into `prompt` + `agentcall`; collapse `Role[T]` dup. (§1.3)
15. Extract `defaultPromptVars`, `runCmd`, `currentTaskByID`, shared `run.log` reader. (§2)
16. Delete dead code: `agent_runner.go`, `commands.Init`, unreferenced `schemas/`, unused `agenthealth` symbols, unused `FailureDetails` fields or wire them. (§3)
17. Make `factory.Save` atomic; unify double `config` validation; `filepath.Join` in `preflight.go:58`. (§6, §1.5)
18. Route silent `_ =` drops through a logger. (§7)
19. Decide fate of `update` (ADR / separate binary) and the gameboard/React vendoring (justify or strip). (§4, §8)

---

*No source files were modified in the production of this report. All line references reflect the tree at audit time (HEAD `02a3022`).*