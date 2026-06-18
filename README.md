![orquestalite hero](docs/hero.png)

# orquestalite

Minimalist Go orchestrator for the Ralph technique: a single binary that drives
multiple CLI-based AI agents through parser, coder, tester, critic, and reviewer
roles until a plan is implemented task by task.

The Go module is `github.com/lionelchamorro/orquestalite`. The CLI command is
`orq-lite`, and runtime state lives under `.orquestalite/`.

## What It Does

`orquestalite` turns a free-form plan into structured tasks, runs those tasks
through nested review/fix loops, and uses JSON result files written by each agent
to decide what happens next.

The orchestrator itself does not edit files or run model tool calls. It invokes
configured CLI agents as subprocesses, reads their result contracts, tracks
task state in `.orquestalite/tasks.json`, and commits successful tasks.

## Install

### From a GitHub release (recommended)

Pre-built binaries are published for Linux, macOS, and Windows on `amd64` and
`arm64`. Pick the archive matching your platform from the
[releases page](https://github.com/lionelchamorro/orquestalite/releases) and
extract `orq-lite` onto your `PATH`.

**macOS / Linux** — replace `VERSION`, `OS` (`darwin` or `linux`), and `ARCH`
(`amd64` or `arm64`):

```bash
VERSION=v0.1.0
OS=darwin
ARCH=arm64
curl -L -o orq-lite.tar.gz \
  "https://github.com/lionelchamorro/orquestalite/releases/download/${VERSION}/orq-lite-${VERSION}-${OS}-${ARCH}.tar.gz"

# Optional: verify checksum
curl -L -o orq-lite.tar.gz.sha256 \
  "https://github.com/lionelchamorro/orquestalite/releases/download/${VERSION}/orq-lite-${VERSION}-${OS}-${ARCH}.tar.gz.sha256"
shasum -a 256 -c orq-lite.tar.gz.sha256

tar -xzf orq-lite.tar.gz
sudo mv "orq-lite-${VERSION}-${OS}-${ARCH}/orq-lite" /usr/local/bin/
orq-lite --help
```

On macOS, Gatekeeper may block the unsigned binary on first run. Allow it with:

```bash
xattr -d com.apple.quarantine /usr/local/bin/orq-lite
```

**Windows (PowerShell)**:

```powershell
$Version = "v0.1.0"
$Arch    = "amd64"   # or "arm64"
$Url     = "https://github.com/lionelchamorro/orquestalite/releases/download/$Version/orq-lite-$Version-windows-$Arch.zip"
Invoke-WebRequest -Uri $Url -OutFile orq-lite.zip
Expand-Archive orq-lite.zip -DestinationPath .
# Move orq-lite.exe somewhere on your PATH
```

### From source

```bash
go build -o orq-lite ./cmd/orq-lite
```

## Quick Start

Scaffold project state, prompts, and `team.json`:

```bash
./orq-lite init
```

Create a plan file:

```bash
cat > plan.md <<'EOF'
Add a small feature, update tests, and keep the implementation minimal.
EOF
```

Convert the plan into tasks:

```bash
./orq-lite plan plan.md
```

Run the orchestration loop:

```bash
./orq-lite run
```

Check progress:

```bash
./orq-lite status
./orq-lite status --watch
```

Reset local orchestration state:

```bash
./orq-lite reset
```

Run a whole backlog of features, each on its own branch (factory mode):

```bash
cat > features.md <<'EOF'
## Add login endpoint

POST /login issuing a JWT; reject bad credentials with 401.

## Add health check

GET /healthz returns 200 with build info.
EOF

./orq-lite factory features.md
./orq-lite factory --status
```

Factory mode hosts the live dashboard by default and prints its URL on
startup (`orquestalite dashboard: http://127.0.0.1:4173`), so you can always
tell the run is live. Pass `--serve=false` to suppress it, or `--addr` to
change the bind address. With no path argument and no in-progress queue,
factory also auto-discovers a `feature.md`/`FEATURE.md`/`goal.md` in the
project root:

```bash
./orq-lite factory          # uses ./feature.md if present, dashboard on
```

Plain `orq-lite factory` (no args) resumes the queue but re-plans each feature
it runs — a `done`/`failed` feature is skipped, and a resumed feature starts
from a fresh task list. To continue a feature that stopped partway through
**without** redoing finished tasks, use `--resume`:

```bash
./orq-lite factory --resume   # retry failed features; reuse the existing
                              # task list (skip completed tasks, no re-plan)
```

`--resume` makes `failed` features runnable again and, for the feature that owns
the on-disk `tasks.json`, continues that list so committed tasks are skipped
rather than replanned. A feature that fails again is attempted once and then
passed over (no infinite retry). Features other than the task-list owner still
plan fresh.

The dashboard can also run standalone (e.g. pointed at a container's mount):

```bash
./orq-lite serve   # http://127.0.0.1:4173
```

## Commands

```text
orq-lite init [dir]            scaffold .orquestalite, team.json, prompts/
orq-lite plan <plan.md>        invoke parser, write tasks.json
orq-lite plan <plan.md> --append
orq-lite run                   run review/task/fix loops
orq-lite factory <features.md> develop each '## ' feature on its own branch
orq-lite factory               resume an interrupted queue (--status, --force, --pr, --serve)
orq-lite factory --resume      continue the queue without re-planning: retry failed features and skip already-done tasks
orq-lite serve [--addr A]      web dashboard with live SSE event stream
orq-lite doctor                preflight git/team.json/CLIs/credentials before spending
orq-lite cost                  per-task spend rollup (sessions priced via agtop)
orq-lite status [--watch]      print task status
orq-lite log [--role R]        replay run.log
orq-lite reset                 remove .orquestalite state
orq-lite update [--check]      install the latest release from GitHub
orq-lite version               print the binary version
```

## Docker

Run the whole factory (orq-lite + claude/codex/gemini CLIs) in a container,
with credentials mounted from your host logins:

```bash
docker compose build
export TARGET_PROJECT=$HOME/code/my-app
docker compose run --rm factory factory features.md
docker compose up dashboard   # http://localhost:4173
```

See [docs/docker.md](./docs/docker.md).

### Updating

```bash
orq-lite update --check   # is a newer release available?
orq-lite update           # download, verify sha256, and install in place
```

`update` queries the GitHub Releases API, picks the archive matching your
OS/arch, verifies the published `.sha256`, and atomically replaces the running
binary. It only works for binaries installed from a release (a `dev` build
will always report itself as outdated).

## Configuration

`team.json` defines:

- an agent pool with provider configs (`claude`, `codex`, `gemini`) or legacy
  CLI commands, models, and optional rate-limit patterns
- role bindings for `parser`, `coder`, `tester`, `critic`, `reviewer`, and the
  optional `verifier` (black-box manual verification after critic approval)
- prompt paths and expected result paths
- rate-limit handling (`rate_limit_backoff`): a rate-limited agent is first
  routed around — the next healthy agent in the role's chain is used. Only when
  no healthy fallback exists does the orchestrator **wait for the rate limit to
  lift** instead of failing the role: it parses the reset hint from the agent's
  output (`try again at 4:30 PM`, `retry after 30s`) and sleeps until then
  (falling back to exponential backoff when no hint is given), looping until an
  agent becomes available. The wait is logged as `rate_limit_wait` so a long
  sleep is visible, and Ctrl-C interrupts it. `max_seconds` now bounds a single
  sleep chunk (so other agents' resets are rechecked promptly), not the total
  wait. A role only fails (`all agents failed`) when every agent is
  non-recoverably broken (auth failure, crash) with none merely rate-limited.
  Agents that can't authenticate headless are dropped rather than waited on:
  static preflight skips a provider agent with no usable credential
  (`no_credentials`), and at run time an agent that falls back to an
  interactive auth prompt (e.g. an un-logged-in gemini CLI opening a browser
  OAuth flow) is detected and skipped immediately (`auth-failed`) instead of
  being retried.
- loop limits and rate-limit backoff settings, including
  `verify_tester_command` (the orchestrator re-runs the tester's reported
  command and overrides a false "pass"; on by default) and
  `factory_budget_usd` (stop the queue once recorded spend reaches the
  budget; priced via the `agtop` CLI when installed)
- the full-suite test command (`full_test_command`) — the gate run after each
  task. `orq-lite init` sets a language-appropriate default; for an
  unrecognized repo it is left **empty** (a no-op) rather than a wrong default
  that would fail every task. When empty at run start, orq-lite detects a
  command from the repo layout (Makefile `test:` target, `pyproject.toml`,
  `package.json`, `go.mod`, `Cargo.toml`, …), uses it, and writes it back to
  `team.json`. Leave it empty to skip the gate entirely.
- an optional lint/quality gate (`lint_command`) run inside the fix loop after
  each coder attempt, before the tester. A violation is fed back to the coder
  as feedback and the attempt retries, so lint/format issues are fixed in-loop;
  only if the coder can't get it clean within the iteration budget does the
  task fail (`lint_failed`). Auto-filled when empty only when a linter is
  clearly configured (a `ruff` config → `ruff check .`, an ESLint config →
  `eslint`, `go.mod` → `go vet ./...`). A missing lint binary is skipped, never
  a hard failure, so an unconfigured tool won't block every task. Empty = no
  lint gate.
- `conventions_file` — optional path to a house-style document (see below)

Prompts live in `prompts/` and use `{{VAR}}` interpolation markers.

### Matching your team's style

Set `conventions_file` in `team.json` to a markdown house-style document and
its contents are injected into the coder, critic, and reviewer prompts as
`{{CONVENTIONS}}`, so generated code matches your team's structure, naming,
logging, and idioms instead of generic AI defaults:

```json
{ "conventions_file": "docs/CONVENTIONS.md" }
```

When unset, the agents are told to infer the house style from the surrounding
code and mirror it. `docs/conventions/collectiveai-python.md` (general Python
house style, including the `prek` pre-commit quality gate) and
`docs/conventions/collectiveai-prefect.md` (Prefect workflow patterns) are
worked examples distilled from a real team's repos — point `conventions_file`
at the one that fits the project, or concatenate them. The default prompts already fold in
language-agnostic engineering discipline (explicit signatures, dependency
injection, test-through-the-interface, mock only at boundaries, deletion test
before adding an abstraction, two-axis Standards/Spec review) drawn from Matt
Pocock's [skills collection](https://github.com/mattpocock/skills).

Runtime state lives in `.orquestalite/`, including:

- `tasks.json` for task state
- `results/<role>.json` for agent result contracts
- `run.log` for JSONL event logs
- `memory.md` for cross-iteration notes

## Architecture

The main loop is intentionally small:

1. `parser` turns a plan into atomic tasks.
2. `coder`, `tester`, and `critic` iterate on one task until it passes or fails.
   The orchestrator independently re-runs the tester's command — a tester
   cannot close a task by claiming "pass" on a failing command.
3. End-of-cycle analysis: the optional `verifier` role exercises the running
   software black-box (start the app, hit endpoints, run the CLI) and its
   report feeds the `reviewer`, which converts every failed check into a
   next-cycle task — closing the "tests pass but manual testing fails" gap.
   (`mode: per_task` moves verification inside the fix loop instead.)
4. Successful tasks are committed one at a time.
5. Factory mode wraps all of the above per feature, on per-feature branches.
   When a feature fails mid-task, any uncommitted residue is preserved as a
   labelled `wip(orq-lite): checkpoint …` commit on the feature branch before
   returning to base — so the queue never gets stuck on a dirty tree, the work
   is recoverable (`git checkout <branch>`; `git reset --soft HEAD^`), and
   completed tasks remain as their own commits.

See [CONTEXT.md](./CONTEXT.md) for the full domain model and
[docs/adr/](./docs/adr/) for architecture decisions.

## Development

Run the test suite:

```bash
go test ./...
```
