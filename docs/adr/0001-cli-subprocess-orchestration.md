# ADR-0001: Orchestrate via CLI subprocesses, not API + in-process tool-use

Date: 2026-05-03
Status: Accepted

## Context

orquestalite drives multiple AI agents (coder, tester, critic, reviewer, parser)
in nested loops to autonomously implement a plan. Each agent needs to read and
write files, run tests, execute commands, and (for the coder) interact with
git.

There are two viable architectures:

**A. CLI subprocess orchestration.** The orchestrator builds a prompt string,
invokes an existing CLI tool (`claude`, `codex`, `gemini`, etc.) as a
subprocess, and reads the result. The CLI tool already implements file
editing, command execution, MCP servers, and other tool-use machinery.

**B. API + in-process tool-use.** The orchestrator calls model APIs directly
(Anthropic SDK, OpenAI SDK, etc.) and implements its own tool-use loop:
file editing, sandboxed command execution, test runners, git operations.

## Decision

We orchestrate via **CLI subprocesses (option A)**. Every agent is invoked
as a process; orquestalite itself does not implement tool-use, file editing,
or command execution.

The role-to-agent binding is declared in `team.json`. Preferred agents use a
first-class provider config, e.g. `{"provider":"claude","model":"claude-sonnet-4-6"}`,
which builds a stable non-interactive CLI command and passes the prompt on
stdin. Legacy agents may still use an explicit `cmd` template, e.g.
`["custom-agent", "{{PROMPT}}"]`.

## Consequences

**Positive:**

- orquestalite stays minimalist. The orchestrator is essentially a loop driver:
  build prompt → exec subprocess → read result.json → decide next step.
- We inherit, for free, the entire tool-use surface of mature CLIs (file
  editing, git integration, MCP servers, sandboxing, telemetry).
- Claude and Codex CLI quirks live behind small providers, while unsupported
  CLIs can still be wired through raw `cmd` entries.
- Per-task model swaps (sonnet for coder, opus for critic) are trivial.

**Negative:**

- We cede control over subprocess output formatting. CLIs print free-form
  reasoning to stdout that we must ignore for control-flow purposes (this
  is what motivated ADR-0002).
- We cannot pass structured tool-use messages between roles; each role's
  output to the next must be reduced to text in a prompt.
- We are still at the mercy of CLI flag stability, but provider updates can
  repair the default Claude/Codex command shapes without editing every
  `team.json` in the wild.
- We cannot reuse a single API client / connection across calls; each
  subprocess pays its own startup cost.

## Alternatives considered

**Option B (API + tool-use)** would give us full control: structured
agent-to-agent communication, deterministic outputs, custom sandboxing,
shared connection pools. But it requires re-implementing the tool-use
loop that mature CLIs already provide, which is the engineering bulk of
products like Claude Code. For a minimalist Ralph-style orchestrator,
this is disproportionate.

**Hybrid (some agents via CLI, some via API)** was considered: the coder
naturally wants CLI tooling, while the tester/critic could be pure API
calls returning JSON. We rejected this because it doubles the engineering
surface (two execution paths, two error models) for marginal benefit —
the JSON contract design (ADR-0002) lets API-only agents and CLI agents
coexist behind the same orchestrator interface anyway, when their roles
demand it.
