# Agent guide: operating orq-lite in a project

This guide is the operational checklist for an agent—or a human supervising
one—to take a repository from no orchestration to a monitored, recoverable
`orq-lite` run.

Orquesta Lite has one execution architecture: strict `orq.dev/v2` flows and
subflows are compiled from verified packs into pinned IR and executed by the
durable workflow scheduler. The former hardcoded loops and root `flows.json`
interpreter have been removed. There is no engine selection or compatibility
fallback.

The most important operating rule is: **make the deterministic gates green at
HEAD before spending agent time**. A pre-existing lint or test failure poisons
every later review and repair loop.

## 0. Preconditions

Before changing project configuration, verify:

- the project is a Git repository and you understand every current worktree
  change (`git status --short`);
- `orq-lite version` works;
- every configured provider CLI is installed and authenticated without an
  interactive prompt;
- `gh auth status` is green if a flow may publish a PR;
- the language runtime and package manager are pinned;
- the dependency lockfile is current and there is only one authoritative
  package manager per project surface.

Install from a release when possible. Building from source is the fallback:

```bash
go install github.com/lionelchamorro/orquestalite/cmd/orq-lite@latest
orq-lite version
```

Start from a clean commit. Durable state protects workflow progress, but a
clean baseline is still necessary for agents and reviewers to attribute a diff
to the current objective.

### Context optimization (recommended, on by default)

Two external tools cut what each agent invocation carries. Both are **enabled by
default** in `team.json` and both **degrade silently when absent** — a run without
them works, it just costs more. Measured on an end-to-end benchmark run of the
same specification: the compression proxy cut cost 38%, the command filter 25%.

orq-lite does not install or supervise either one. Install them here, in
preconditions, and leave the proxy running.

**1. Compression proxy** — compresses request bodies between the agent and the
provider API, chiefly the tool schemas declared on every invocation (91% of its
measured saving). It is a daemon, so it must be running before the flow starts.

```bash
uv tool install --python 3.13 "headroom-ai[all]"   # NOT "headroom" — see below
headroom proxy --port 8787                          # leave this running
curl -s http://127.0.0.1:8787/stats >/dev/null && echo reachable
```

Leave it running for the duration of the work — in a separate terminal, a
`tmux`/`screen` session, or as a user service. orq-lite probes the address at run
start: reachable means it is used, unreachable means the run proceeds without it
and says so in the log.

> **Install trap.** The PyPI name `headroom` belongs to an unrelated project (a
> command-line AI assistant). Installing it gives you the wrong tool with no
> error. The correct package is **`headroom-ai`**.

**2. Command filter** — rewrites the agent's shell commands so verbose output is
filtered before it becomes a tool result. A one-shot binary, no daemon.

```bash
brew install rtk        # or: cargo install rtk, or a release binary
rtk --version
command -v rtk          # must resolve by name — see below
```

> **Do not run `rtk init -g`.** It writes a hook into your **global**
> `~/.claude/settings.json`, affecting every unrelated Claude session on the
> machine. orq-lite writes the equivalent hook into this project's
> `.claude/settings.json` instead, so the setting is per project and reversible
> by editing one local file. (`rtk init -g` also prompts and answers *no* when
> there is no TTY, so it silently does nothing in automation.)

> **The binary must resolve by name.** The hook rewrites `git status` to
> `rtk git status` with no path. If `rtk` is not on `PATH`, every rewritten
> command dies with exit 127 and the agent retries blind — a failure that looks
> like agent confusion, not a configuration error. orq-lite verifies this before
> enabling the filter and skips it rather than breaking every shell call, but
> installing the binary somewhere on `PATH` is what you actually want.

**3. Confirm what is active.** `orq-lite doctor` reports both:

```
[PASS] compression_proxy      reachable at http://127.0.0.1:8787
[PASS] command_filter         verified: /opt/homebrew/bin/rtk
```

A `[WARN]` on either means the run will proceed without that tool. That is a
deliberate choice, not a failure — but it is also money, so resolve it before a
long run.

**To turn either off**, set it in `team.json`. Omitting the block enables both:

```json
{
  "runtime": {
    "context_optimization": {
      "compression_proxy": { "enabled": false },
      "command_filter":    { "enabled": false }
    }
  }
}
```

Use `url` / `binary` in the same blocks to point at a pinned or vendored install.

## 1. Initialize the project

Run `init` from the project root:

```bash
orq-lite init --lang auto
```

Use `--lang go`, `--lang python`, or `--lang node` when autodetection would be
ambiguous. Initialization creates:

- `team.json`, containing provider-backed agents, dynamic roles, and argv
  quality gates;
- `.orquestalite/packs/development/5`, the exact built-in verified development
  pack embedded in the binary;
- `.orquestalite/results/`, for latest role results and immutable archives;
- ignore rules for local runtime state and language build artifacts.

The first workflow run creates `.orquestalite/workflows.db`. Initialization
does not create project prompts, schemas, or `flows.json`. `team.json` and
`.orquestalite/` are local runtime configuration/state and are ignored by
default; project-owned flows under `flows/` can remain tracked.

When initialization changes `.gitignore`, it commits only that file as
`chore: orq-lite ignore rules`. It does not commit other staged work.
Re-running V5 initialization migrates only canonical
`.orquestalite/packs/development/4/prompts/` references in an existing
`team.json` to V5; custom agents, gates, conventions, and other user settings
are preserved.

Confirm the installed surface:

```bash
orq-lite pack list
orq-lite flow list
orq-lite flow validate development/task-list@1
orq-lite flow inspect development/task-list@1
```

Pack and flow versions are independent. This reference selects flow version 1
from the newest installed matching pack:

```text
development/task-list@1
```

Pin both versions when exact reproducibility matters:

```text
development@5/task-list@1
```

## 2. Prove the baseline is green

Configure and run the exact commands that the workflow will execute. Gates are
argv arrays, not shell strings:

```json
{
  "lint_argv": ["go", "vet", "./..."],
  "test_argv": ["go", "test", "./..."]
}
```

Equivalent examples:

```json
{
  "lint_argv": ["uv", "run", "ruff", "check", "."],
  "test_argv": ["uv", "run", "pytest", "-q"]
}
```

```json
{
  "lint_argv": ["npm", "run", "lint"],
  "test_argv": ["npm", "test", "--silent"]
}
```

Do not place pipelines, redirects, `cd`, or environment assignments in these
arrays. The runtime executes argv directly with shell access disabled. In a
monorepo, use a repository-root command or wrapper that scopes itself to the
correct package.

Prepare the baseline in this order:

1. Fix collection, compilation, and import errors.
2. Install missing test and lint dependencies in the committed dependency set.
3. Remove live-service, secret, ordering, and local `.env` dependencies from
   the deterministic suite.
4. Configure legitimate linter exceptions centrally; do not make every agent
   rediscover the same existing debt.
5. Apply safe automatic fixes, then rerun the complete test gate.
6. Run both commands from a fresh worktree or clone.
7. Deliberately introduce one lint/test failure and confirm each gate returns a
   non-zero exit before reverting the probe.

The last check proves the gate can go red. A command that always exits zero is
more dangerous than a visibly missing gate because later roles treat green as
evidence.

## 3. Configure agents and roles

`team.json` separates execution providers from workflow roles. A compiled flow
resolves only the roles it references; Orquesta Lite has no hardcoded required
role set.

```json
{
  "agents": {
    "primary": {
      "provider": "codex",
      "model": "gpt-5.5",
      "effort": "medium"
    },
    "reviewer": {
      "provider": "claude",
      "model": "claude-opus-4-8",
      "dangerously_skip_permissions": true
    }
  },
  "roles": {
    "coder": {
      "agents": ["primary", "reviewer"],
      "prompt": ".orquestalite/packs/development/5/prompts/coder.md",
      "result_path": ".orquestalite/results/coder.json",
      "timeout_seconds": 1800
    }
  },
  "limits": {"resume_sessions": true},
  "conventions_file": "CONVENTIONS.md",
  "lint_argv": ["go", "vet", "./..."],
  "test_argv": ["go", "test", "./..."]
}
```

For each agent:

- confirm the provider and model names are valid for the installed CLI;
- configure a provider-specific rate-limit pattern;
- grant unattended edit permissions only in an isolated repository/worktree;
- test authentication with a small non-interactive invocation;
- use a second provider in important role chains so a provider failure has a
  real fallback path.

Contract failures get bounded same-agent corrective retries. Timeouts,
provider exits, exhausted quotas, and other operational failures move through
the configured agent chain. Session resume applies only when the same provider
and durable scope are reused.

For every role:

- keep `result_path` rooted at the repository root;
- ensure its parent directory is writable;
- give judgment-heavy roles enough timeout and a strong model;
- preserve strict result schemas and fail-closed fallbacks;
- verify the flow consumes `steps.<role>.output`, rather than merely invoking
  the role and discarding its findings.

Put repository rules in `CONVENTIONS.md`: code style, architectural boundaries,
test strategy, compatibility promises, generated-file rules, security
invariants, and commands needed for browser/API verification. Concrete rules
converge better than asking each role to infer the project from nearby files.

## 4. Choose the development flow

The built-in `development@5` pack contains:

| Flow | Purpose |
|---|---|
| `plan-tickets@1` | Produce or extend a bounded ticket plan. |
| `task-list@1` | Plan, implement, ticket-QA, and run final deterministic gates. |
| `factory-fast@1` | Implement a feature batch with one integrated QA/repair pass. |
| `factory-governed@2` | Implement as a batch by default (or ticket-by-ticket), then always execute integrated governance. |
| `review-existing@1` | Run integrated QA, adversary, critic, repair, and governance over an existing change. |
| `issue-fix@1` | Triage and optionally repair an issue. |
| `pr-review@1` | Review a PR and optionally publish the verdict. |

The familiar CLI commands are aliases to those flows:

```text
orq-lite plan features.md        -> development/plan-tickets@1
orq-lite run                     -> development/task-list@1
orq-lite factory features.md     -> development/factory-governed@2
orq-lite review --pr 123         -> development/pr-review@1
orq-lite intake --issue issue.md -> development/issue-fix@1
```

There is no `--engine` flag and no alternate scheduler behind these aliases.

### Recommended two-loop workflow

For most development work, separate throughput from integrated criticism:

1. Run the delivery phase: the default batch coder maximizes throughput, while
   `--fast=false` uses bounded coder/ticket-QA iterations for larger objectives.
   Deterministic gates protect both paths.
2. After the complete change exists, run an integrated review loop. QA,
   adversary, and critic inspect the assembled system; the integrator converts
   their findings into repairs; governance decides whether another bounded
   iteration is needed.

You can run both phases in one durable workflow:

```bash
orq-lite factory features.md
```

Or run them separately when an external agent is supervising development:

```bash
orq-lite run
orq-lite flow run development/review-existing@1 \
  --source-key=review-existing:<stable-objective-id> \
  features_path=features.md
```

The second form is useful when the supervising agent wants to inspect the first
run, repair operational problems, adjust role/model assignments, and launch a
fresh independent review over the resulting tree.

### What each review role must do

`qa` validates observable behavior. For web changes it should start the real
application, use a browser-capable skill/tool, exercise critical paths, inspect
console/network failures, and attach reproducible evidence. The flow does not
magically provision a browser: the selected agent environment must provide one,
and stable checks should be encoded in Playwright or equivalent tests executed
by `test_argv`.

`adversary` evaluates the product objective and system invariants beyond the
literal ticket wording. It should probe trust boundaries, state transitions,
concurrency, retries, idempotency, partial failure, compatibility, and misuse.
A useful finding includes a reproduction or a concrete falsifiable path.

`critic` reviews the implementation as code: correctness risks,
maintainability, unnecessary complexity, dead code, architecture fit, typing,
logging, error handling, and compliance with `CONVENTIONS.md`.

`gov_reviewer` evaluates the combined evidence. It must fail closed when a role
did not run, returned a fallback, produced an invalid contract, or left a
reproduced finding unresolved.

These roles should not be three differently worded spec reviewers. Their
independent lenses are the reason the second loop finds issues that ticket-level
development misses.

### Implementation mode

`factory-governed@2` defaults to `fast=true`: one `batch_coder` implements the
bounded objective and an initial QA/repair pass checks the assembled change.
The full integrated QA/adversary/critic/governance phase then runs
unconditionally. Fast changes implementation granularity; it never weakens the
shipping gate.

Use `orq-lite factory features.md --fast=false` when the objective is too large
or risky for one batch and needs the coder/ticket-QA loop. Both modes finish
through the same integrated governance. The separate `factory-fast@1` flow is
the lighter, non-governed option and should receive a `review-existing@1` run
before shipping.

## 5. Write an objective that survives ticket decomposition

The features or plan document is not only input to the planner. It is the
stable objective carried through implementation and integrated review.

Write it with:

- a preamble defining the user outcome, non-goals, stack, constraints, and
  cross-feature invariants;
- one `##` heading per independently verifiable vertical slice;
- mechanically checkable acceptance criteria;
- explicit compatibility and failure behavior;
- required automated and manual/browser evidence;
- dependency order;
- small enough slices for one bounded implementation attempt.

Avoid specifying only files or internal steps. The adversary needs the broader
outcome to identify behavior the ticket list forgot to mention.

## 6. Validate before spending

Run:

```bash
orq-lite doctor
orq-lite pack list
orq-lite flow list
orq-lite flow validate development@5/factory-governed@2
orq-lite flow inspect development@5/factory-governed@2
git status --short
```

Resolve every doctor failure. Treat warnings about a dirty tree, missing
conventions, weak/missing providers, credentials, and empty gates as setup work,
not as harmless noise.

Before launch, record:

- the exact flow reference;
- the objective/features path;
- a stable source key for externally delivered work;
- the expected roles and their primary/fallback providers;
- the gate commands already proven green;
- who will monitor approvals and operational failures.

## 7. Launch idempotently

Use a source key whenever the work comes from a stable external object such as
an issue, PR, automation delivery, or objective ID:

```bash
orq-lite flow run development@5/factory-governed@2 \
  --source-key=objective:customer-import-v2 \
  features_path=features.md \
  create_pr=false
```

Repeated delivery of the same source key returns the original run instead of
creating duplicate work. Capture the printed `run_id`; it is the durable handle
for every later operation.

Do not wrap a governed run in a short shell timeout. Long workflows can be
detached from the launching terminal, but the monitor must keep the run ID and
check durable state rather than guessing whether the process completed.

## 8. Monitor and recover

Use public runtime commands rather than querying SQLite directly:

```bash
orq-lite status --watch
orq-lite flow status <run-id>
orq-lite flow events <run-id>
orq-lite log
orq-lite cost
```

For a dashboard:

```bash
orq-lite serve --addr 127.0.0.1:4173
```

When a run stops, inspect status and events before relaunching:

- transient/recoverable interruption: `orq-lite flow resume <run-id>`;
- explicit safety decision: inspect the pending approval, then use
  `orq-lite flow approve <run-id> <approval-id> --decision approve|reject`;
- obsolete work: `orq-lite flow cancel <run-id>`;
- invalid config or unavailable provider: correct `team.json`, verify the
  provider, and resume only when the stored step can safely continue;
- deterministic gate failure: inspect and fix the project; do not weaken the
  gate merely to make the workflow green.

Resume uses the stored compiled IR and verifies pinned pack resources. It does
not silently compile a newer installed flow, and completed steps are not rerun.
This is why resume is normally cheaper and safer than starting another workflow.

## 9. Operate GitHub watch mode

Watch compiles configured flows at startup and emits idempotent durable
triggers:

```bash
orq-lite watch . --issues --prs \
  --issue-flow development/issue-fix@1 \
  --pr-flow development/pr-review@1
```

Before leaving it unattended:

- validate both flow references;
- confirm `gh auth status` and repository permissions;
- test one known issue/PR delivery;
- verify source-key deduplication in workflow status;
- ensure PR publishing is explicitly enabled only where intended;
- monitor provider backoff and cost events.

## 10. Author or modify a pack

Use packs for reusable workflows, subflows, prompts, schemas, and policies. Do
not add a parallel runtime or special-case orchestration in a CLI command.

The canonical example is [`examples/governed-pack/`](./examples/governed-pack/).
After changing any resource in that pack:

```bash
python3 examples/governed-pack/regen-digests.py
orq-lite pack install examples/governed-pack/pack
orq-lite flow validate development@5/factory-governed@2
orq-lite flow inspect development@5/factory-governed@2
```

Review every manifest digest change. Pack installation rejects missing files,
unlisted files, digest mismatches, and symlinks before atomically installing a
version under `.orquestalite/packs/<name>/<version>`.

Local project flows may live under `flows/`, but they must still be strict V2
documents and execute through the same compiler and durable scheduler.

## Final checklist

- [ ] Git baseline understood and clean; toolchain and one package manager pinned.
- [ ] `orq-lite`, provider CLIs, and required `gh` access authenticated headlessly.
- [ ] `orq-lite init` completed; built-in pack visible and target flow validates.
- [ ] `lint_argv` and `test_argv` run from the repository root, pass from a fresh checkout, and have each been proven able to fail.
- [ ] Every referenced role resolves to suitable primary/fallback agents, prompts, result paths, and timeouts.
- [ ] `CONVENTIONS.md` defines style, architecture, compatibility, testing, and verification rules.
- [ ] Web QA has a browser-capable environment and durable browser tests where appropriate.
- [ ] The objective describes the product outcome and invariants beyond individual tickets.
- [ ] `orq-lite doctor` has no unresolved failure; warnings have been consciously handled.
- [ ] The launch uses an exact flow reference, stable source key, and recorded run ID.
- [ ] A monitor is assigned to inspect status/events, handle approvals, and resume instead of duplicating runs.
- [ ] `factory-governed@2` completes integrated governance; output from the lighter `factory-fast@1` receives a separate `review-existing@1` run before shipping.
