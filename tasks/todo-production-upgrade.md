# Production upgrade — feat/production-factory

Goal: production-level orquestalite — better loop quality (close the "tests pass
but manual testing fails" gap), gemini provider, Docker packaging with mounted
credentials, multi-feature factory mode, and an embedded web dashboard.

## Phase 1 — Gemini provider
- [ ] `internal/providers/gemini.go`: `gemini --output-format stream-json --yolo -m <model>`,
      prompt on stdin. Parse JSONL events: `init` (session_id), `message`
      (role=assistant, content, delta), `tool_use`, `error`, `result`
      (status success|error, error.message).
- [ ] Tests mirroring `claude_test.go` / `codex_test.go`.
- [ ] Add gemini agent to `internal/commands/assets/team.json` + root `team.json`.

## Phase 2 — Loop quality
- [ ] **Verified testing**: after tester reports `pass`, orchestrator re-runs
      `command_run` itself in the workdir; non-zero exit ⇒ treat as tester fail
      with the real output as feedback. Configurable `limits.verify_tester_command`
      (default true).
- [ ] **Tester contract hardening**: prompt rewrite — must run a real test
      command, never a no-op; report real output evidence.
- [ ] **Verifier role (manual-test gap)**: optional 6th role. After critic
      approves, verifier black-box-verifies the change like a human would
      (start the app / hit endpoints / run the CLI / check actual behavior).
      `verifier.json`: `{status: pass|fail, checks: [{name, action, expected,
      actual, passed}], notes_for_memory}`. Fail ⇒ feedback to coder, loop
      continues. Wired only when `roles.verifier` exists in team.json.
- [ ] **should_stop fix**: reviewer `should_stop=true` ends the run immediately
      (still logs remaining pending count).
- [ ] **Empty-suite handling**: pytest exit code 5 ("no tests collected")
      treated as pass-with-warning in the full-suite gate.
- [ ] **Decomposition depth cap**: `decomposition_depth` on Task; max 2.

## Phase 3 — Factory mode
- [ ] `orq-lite factory <features.md>`: parse feature list (markdown headings
      or `---` separated blocks), queue in `.orquestalite/factory.json`.
- [ ] Per feature: create branch `factory/NNN-slug` from base, run plan+loops,
      record result (done/failed, commits, duration), checkout base, next.
- [ ] `orq-lite factory --status` view; resume interrupted queue.

## Phase 4 — Web dashboard
- [ ] `orq-lite serve [--addr]`: net/http + go:embed static UI.
- [ ] `GET /api/tasks`, `GET /api/factory`, `GET /api/events` (SSE tail of
      run.log), `GET /api/log?n=`.
- [ ] Single-page dashboard: factory queue, task table with live status,
      event stream, per-agent activity.

## Phase 5 — Docker
- [ ] `Dockerfile`: build stage (golang) → runtime (node slim + git +
      claude/codex/gemini CLIs + orq-lite).
- [ ] `docker-compose.yml` + `docker/run.sh`: mount `~/.claude`, `~/.codex`,
      `~/.gemini`, project dir at `/workspace`; serve port exposed.
- [ ] `docs/docker.md`.

## Phase 6 — Docs & verification
- [ ] README + CONTEXT.md updates (new roles, commands, contracts).
- [ ] `go vet ./...` + `go test ./...` green; build binary; smoke `serve`.

## Review
(filled at the end)
