![orquesta-lite hero](docs/hero.png)

# orquesta-lite

Minimalist Go orchestrator for the Ralph technique: a single binary that drives
multiple CLI-based AI agents through parser, coder, tester, critic, and reviewer
roles until a plan is implemented task by task.

The Go module is `github.com/lionelchamorro/orquesta-lite`. The current CLI
entrypoint and runtime state still use the historical `pyorquesta` name.

## What It Does

`orquesta-lite` turns a free-form plan into structured tasks, runs those tasks
through nested review/fix loops, and uses JSON result files written by each agent
to decide what happens next.

The orchestrator itself does not edit files or run model tool calls. It invokes
configured CLI agents as subprocesses, reads their result contracts, tracks
task state in `.pyorquesta/tasks.json`, and commits successful tasks.

## Quick Start

Build the binary:

```bash
go build -o pyorquesta ./cmd/pyorquesta
```

Scaffold project state, prompts, and `team.json`:

```bash
./pyorquesta init
```

Create a plan file:

```bash
cat > plan.md <<'EOF'
Add a small feature, update tests, and keep the implementation minimal.
EOF
```

Convert the plan into tasks:

```bash
./pyorquesta plan plan.md
```

Run the orchestration loop:

```bash
./pyorquesta run
```

Check progress:

```bash
./pyorquesta status
./pyorquesta status --watch
```

Reset local orchestration state:

```bash
./pyorquesta reset
```

## Commands

```text
pyorquesta init [dir]            scaffold .pyorquesta, team.json, prompts/
pyorquesta plan <plan.md>        invoke parser, write tasks.json
pyorquesta plan <plan.md> --append
pyorquesta run                   run review/task/fix loops
pyorquesta status [--watch]      print task status
pyorquesta reset                 remove .pyorquesta state
```

## Configuration

`team.json` defines:

- an agent pool with CLI commands, models, and optional rate-limit patterns
- role bindings for `parser`, `coder`, `tester`, `critic`, and `reviewer`
- prompt paths and expected result paths
- loop limits and rate-limit backoff settings
- the full-suite test command

Prompts live in `prompts/` and use `{{VAR}}` interpolation markers.

Runtime state lives in `.pyorquesta/`, including:

- `tasks.json` for task state
- `results/<role>.json` for agent result contracts
- `run.log` for JSONL event logs
- `memory.md` for cross-iteration notes

## Architecture

The main loop is intentionally small:

1. `parser` turns a plan into atomic tasks.
2. `coder`, `tester`, and `critic` iterate on one task until it passes or fails.
3. `reviewer` inspects completed work and can append follow-up tasks.
4. Successful tasks are committed one at a time.

See [CONTEXT.md](./CONTEXT.md) for the full domain model and
[docs/adr/](./docs/adr/) for architecture decisions.

## Development

Run the test suite:

```bash
go test ./...
```
