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

Watch everything live in the browser:

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
orq-lite factory               resume an interrupted queue (--status, --force)
orq-lite serve [--addr A]      web dashboard with live SSE event stream
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
- loop limits and rate-limit backoff settings, including
  `verify_tester_command` (the orchestrator re-runs the tester's reported
  command and overrides a false "pass"; on by default)
- the full-suite test command

Prompts live in `prompts/` and use `{{VAR}}` interpolation markers.

Runtime state lives in `.orquestalite/`, including:

- `tasks.json` for task state
- `results/<role>.json` for agent result contracts
- `run.log` for JSONL event logs
- `memory.md` for cross-iteration notes

## Architecture

The main loop is intentionally small:

1. `parser` turns a plan into atomic tasks.
2. `coder`, `tester`, and `critic` iterate on one task until it passes or fails.
   The orchestrator independently re-runs the tester's command, and the
   optional `verifier` role exercises the running software black-box (start
   the app, hit endpoints) before a task can close — closing the "tests pass
   but manual testing fails" gap.
3. `reviewer` inspects completed work and can append follow-up tasks.
4. Successful tasks are committed one at a time.
5. Factory mode wraps all of the above per feature, on per-feature branches.

See [CONTEXT.md](./CONTEXT.md) for the full domain model and
[docs/adr/](./docs/adr/) for architecture decisions.

## Development

Run the test suite:

```bash
go test ./...
```
