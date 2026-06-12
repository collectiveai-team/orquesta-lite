# Production upgrade — feat/production-factory

Goal: production-level orquestalite — better loop quality (close the "tests pass
but manual testing fails" gap), gemini provider, Docker packaging with mounted
credentials, multi-feature factory mode, and an embedded web dashboard.

## Phase 1 — Gemini provider
- [x] `internal/providers/gemini.go`: `gemini --output-format stream-json [--yolo] -m <model>`,
      prompt on stdin. Parses JSONL events `init`/`message`/`tool_use`/`error`/`result`
      (event shapes extracted from the gemini-cli 0.43 bundle source).
- [x] Tests mirroring `claude_test.go` / `codex_test.go`.
- [x] gemini agent in `internal/commands/assets/team.json` + root `team.json`
      (tester fallback chain).

## Phase 2 — Loop quality
- [x] **Verified testing**: orchestrator re-runs the tester's `command_run`
      (`sh -c`, project dir, tester timeout) after a reported pass; non-zero
      exit overrides the pass with the real output as coder feedback.
      `limits.verify_tester_command`, default true. Event: `tester_verification_failed`.
- [x] **Tester prompt hardening**: real test-framework command required; "no
      tests cover the change" must be reported as fail; warns the command is
      re-run independently.
- [x] **Verifier role**: optional 6th role after critic approval; black-box
      checks with evidence (`verifier.json`: status + checks[]). Fail loops
      back to the coder via `{{VERIFIER_FEEDBACK}}`. Wired only when
      `roles.verifier` exists. Prompt, schema, contract parser, fix-loop
      integration, init assets.
- [x] **should_stop**: already fixed in current code (`review.go:57` returns
      immediately) — findings doc described an older version. No change needed.
- [x] **Empty-suite handling**: pytest exit code 5 → pass-with-warning
      (`full_suite_empty` event).
- [x] **Decomposition depth cap**: `tasks.decomposition_depth`, max 2
      generations, then handoff.
- [x] **Bonus bug fix**: handoff documents were deleted by the subsequent
      Rollback (untracked file created after the task-start snapshot);
      rollback now runs before the handoff is written.

## Phase 3 — Factory mode
- [x] `internal/factory`: ParseFeatures (one feature per `## ` heading),
      queue state in `.orquestalite/factory.json`, engine with injected deps.
- [x] Per feature: branch `factory/NNN-slug` from base, fresh tasks.json
      (old archived), plan + run, counts recorded, back to base; failures
      recorded and the queue continues.
- [x] `orq-lite factory <features.md>` / resume (no args) / `--status` /
      `--force`; requires git repo + clean tree.

## Phase 4 — Web dashboard
- [x] `orq-lite serve [--addr]` (default 127.0.0.1:4173), go:embed static UI.
- [x] `GET /api/tasks`, `GET /api/factory`, `GET /api/events` (SSE tail of
      run.log with replay, rotation and partial-write tolerance).
- [x] Dashboard: factory queue, live task table, color-coded event stream.

## Phase 5 — Docker
- [x] Multi-stage `Dockerfile`: static orq-lite + node:22-slim runtime with
      claude/codex/gemini CLIs (versions pinnable via build args) + git.
- [x] `docker-compose.yml`: factory service (TARGET_PROJECT mount,
      ~/.claude, ~/.claude.json, ~/.codex, ~/.gemini mounted rw) +
      read-only dashboard service on :4173.
- [x] `docs/docker.md`.
- [ ] Image build not verified locally (docker daemon not running on this
      machine) — verify with `docker compose build` when the daemon is up.

## Phase 6 — Docs & verification
- [x] README (factory/serve/docker sections) + CONTEXT.md (verifier role,
      contracts, factory, CLI surface, test scope) updated.
- [x] `go vet ./...` + `go test ./...` green at every commit; binary built
      and `serve` smoke-tested against real .orquestalite state with curl
      (tasks API, index, SSE replay all verified).

## Iteration 2 (user feedback)
- [x] Verifier placement mode: `per_cycle` (new default) | `per_task` | `both`.
      Per-cycle pass uses prompts/verifier-cycle.md, runs after the task loop
      drains, and its report is injected into the reviewer as
      {{VERIFICATION_REPORT}}; reviewer converts FAIL checks into priority-1
      tasks and cannot should_stop while failures remain.
- [x] One-command mode: `orq-lite run --serve` / `orq-lite factory f.md --serve`
      host the dashboard alongside the loops.

## Iteration 3 (user-selected follow-ups)
- [x] **Per-feature PRs** (`factory --pr`): push branch + `gh pr create` per
      done feature; URL in factory.json, --status, dashboard. Failures are
      logged, never fail the feature.
- [x] **Cost/budget via agtop**: internal/cost joins run.log agent runs
      (agent_run events now carry task_id/cycle/attempt) with `agtop --json`
      per-session pricing. `orq-lite cost` table; per-feature cost_usd;
      `limits.factory_budget_usd` queue cap (resumable); dashboard total
      (/api/cost, 60s cache). Verified live: $32.45 priced from this repo's
      own run history.
- [x] **orq-lite doctor**: git state, team.json load+resolve, prompt files,
      provider CLIs on PATH, credentials (~/.claude.json, ~/.codex/auth.json,
      ~/.gemini/oauth_creds.json or API-key envs), full_test_command binary,
      agtop/gh availability. FAIL exits 1. Verified live (caught the real
      gemini auth gap on this machine).
- [x] **Browser-real verification**: verifier prompts prefer headless
      chromium via playwright for web apps (fail on console errors), with
      explicit curl-fallback disclosure; Dockerfile INSTALL_PLAYWRIGHT=1
      build arg.
- Skipped per user: parallel worktree workers, notifications.

## Review

Six commits on `feat/production-factory`:

1. gemini provider + verifier role + verified testing + loop hardening
2. factory mode + handoff/rollback bug fix
3. web dashboard with SSE
4. docker packaging
5. docs

Design decisions taken autonomously (per request):
- Interface = embedded web dashboard (single binary, works in Docker/remote);
  a VSCode extension can wrap the same HTTP API later.
- The "manual test fails" problem is attacked at TWO layers: mechanically
  (orchestrator re-runs the tester's command) and semantically (verifier role
  exercises the running app black-box before a task may close).
- Factory mode is sequential by design: the single-working-directory
  architecture (locked in CONTEXT.md) makes concurrent runs unsafe; per-feature
  branches give isolation without that risk.

Known follow-ups:
- Verify the Docker image build (daemon was down here).
- Gemini provider's stream format was derived from the installed CLI bundle
  (v0.43) and unit-tested against those shapes, but not exercised against a
  live authenticated gemini run.
- The dashboard is read-only by design; controlling runs from the UI would
  need an auth story first.
