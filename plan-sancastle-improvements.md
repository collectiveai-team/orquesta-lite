# Plan: Provider-Based Agent Launch and Output Capture

This plan adapts the useful parts of Sandcastle's agent provider model into
orquesta-lite without adopting Sandcastle's worktree, sandbox, or parallel
execution model.

Scope:

- Launch Claude Code and Codex with stable non-interactive commands.
- Pass large prompts through stdin instead of argv.
- Capture stdout/stderr and parse JSONL stream events.
- Preserve orquesta-lite's existing role loop, fallback chain, result JSON
  files, and sequential single-directory execution.

Out of scope for this plan:

- Worktrees.
- Docker/sandbox providers.
- Parallel planner/implementer/reviewer execution.
- Branch-per-task execution.

## Sandcastle References

Relevant Sandcastle files in the temporary clone:

- `/tmp/codex-repos/sandcastle/src/AgentProvider.ts`
- `/tmp/codex-repos/sandcastle/src/Orchestrator.ts`

The important Sandcastle ideas are:

1. An agent provider builds a command plus optional stdin.
2. The runner streams stdout line-by-line.
3. Each provider parses its own JSONL format into common events.
4. The orchestrator accumulates final result text, tool calls, session IDs, and
   usage independently from the subprocess exit code.

Sandcastle's provider interface is centered on:

```ts
interface AgentProvider {
  name: string;
  buildPrintCommand(options): { command: string; stdin?: string };
  parseStreamLine(line: string): ParsedStreamEvent[];
}
```

orquesta-lite can implement the same shape in Go while keeping `team.json` as
the user-facing configuration.

## Current orquesta-lite Limitation

Today, `internal/runner/runner.go` replaces `{{PROMPT}}` in argv:

```go
args := make([]string, len(s.Cmd))
for i, tok := range s.Cmd {
    args[i] = strings.ReplaceAll(tok, "{{PROMPT}}", s.Prompt)
}

cmd := exec.CommandContext(cctx, args[0], args[1:]...)
```

This works for small prompts, but it has three problems:

- Large prompts can exceed OS argv limits.
- Provider-specific flags are duplicated in `team.json`.
- stdout is captured only after process exit, so orquesta-lite cannot log
  useful live events such as tool calls, session IDs, or token usage.

## Target Shape

Add a provider layer under `internal/providers`.

```go
package providers

import "context"

type EventType string

const (
    EventText      EventType = "text"
    EventResult    EventType = "result"
    EventToolCall  EventType = "tool_call"
    EventSessionID EventType = "session_id"
    EventUsage     EventType = "usage"
    EventError     EventType = "error"
)

type Event struct {
    Type      EventType      `json:"type"`
    Text      string         `json:"text,omitempty"`
    Result    string         `json:"result,omitempty"`
    ToolName  string         `json:"tool_name,omitempty"`
    ToolArgs  string         `json:"tool_args,omitempty"`
    SessionID string         `json:"session_id,omitempty"`
    Usage     map[string]int `json:"usage,omitempty"`
}

type Launch struct {
    Args  []string
    Stdin string
}

type Provider interface {
    Name() string
    Build(ctx context.Context, prompt string, opts Options) (Launch, error)
    ParseLine(line string) []Event
}

type Options struct {
    Model                   string
    Effort                  string
    DangerouslySkipPerms    bool
    ResumeSessionID         string
    ForkSession             bool
}
```

This keeps the concrete agent quirks out of `runner.RunAgent`.

## Claude Code Launch

Sandcastle launches Claude Code in print mode, asks for stream JSON, and sends
the prompt through stdin. The important command shape is:

```sh
claude --print --verbose \
  --dangerously-skip-permissions \
  --output-format stream-json \
  --model claude-sonnet-4-6 \
  -p -
```

The final `-p -` is the key part: Claude reads the prompt from stdin.

Go provider sample:

```go
package providers

import "context"

type Claude struct{}

func (Claude) Name() string { return "claude" }

func (Claude) Build(ctx context.Context, prompt string, opts Options) (Launch, error) {
    model := opts.Model
    if model == "" {
        model = "claude-sonnet-4-6"
    }

    args := []string{
        "claude",
        "--print",
        "--verbose",
        "--output-format", "stream-json",
        "--model", model,
        "-p", "-",
    }

    if opts.DangerouslySkipPerms {
        args = insertAfter(args, "--verbose", "--dangerously-skip-permissions")
    }
    if opts.Effort != "" {
        args = append(args[:len(args)-2], "--effort", opts.Effort, "-p", "-")
    }
    if opts.ResumeSessionID != "" {
        args = append(args[:len(args)-2], "--resume", opts.ResumeSessionID, "-p", "-")
        if opts.ForkSession {
            args = append(args[:len(args)-2], "--fork-session", "-p", "-")
        }
    }

    return Launch{Args: args, Stdin: prompt}, nil
}
```

Use a small helper to insert optional flags cleanly:

```go
func insertAfter(args []string, after string, values ...string) []string {
    out := make([]string, 0, len(args)+len(values))
    for _, arg := range args {
        out = append(out, arg)
        if arg == after {
            out = append(out, values...)
        }
    }
    return out
}
```

Claude stream parser sample:

```go
func (Claude) ParseLine(line string) []Event {
    if line == "" || line[0] != '{' {
        return nil
    }

    var obj map[string]any
    if err := json.Unmarshal([]byte(line), &obj); err != nil {
        return nil
    }

    if obj["type"] == "system" && obj["subtype"] == "init" {
        if id, ok := obj["session_id"].(string); ok {
            return []Event{{Type: EventSessionID, SessionID: id}}
        }
    }

    if obj["type"] == "result" {
        if result, ok := obj["result"].(string); ok {
            return []Event{{Type: EventResult, Result: result}}
        }
    }

    if obj["type"] == "assistant" {
        return parseClaudeAssistantMessage(obj)
    }

    return nil
}
```

The `parseClaudeAssistantMessage` helper should inspect
`message.content[]`. For blocks with `type == "text"`, emit `EventText`. For
blocks with `type == "tool_use"`, emit `EventToolCall` when the tool has a
friendly display argument such as:

```go
var toolArgFields = map[string]string{
    "Bash":     "command",
    "WebSearch": "query",
    "WebFetch":  "url",
    "Agent":     "description",
}
```

## Codex Launch

Sandcastle launches Codex with `codex exec`, JSON output, approval bypass, and
stdin prompt.

Fresh run command shape:

```sh
codex exec \
  --json \
  --dangerously-bypass-approvals-and-sandbox \
  -m gpt-5
```

Resume command shape:

```sh
codex exec resume <session-id> - \
  --json \
  --dangerously-bypass-approvals-and-sandbox \
  -m gpt-5
```

Fork command shape:

```sh
codex exec fork <session-id> - \
  --json \
  --dangerously-bypass-approvals-and-sandbox \
  -m gpt-5
```

Go provider sample:

```go
type Codex struct{}

func (Codex) Name() string { return "codex" }

func (Codex) Build(ctx context.Context, prompt string, opts Options) (Launch, error) {
    model := opts.Model
    if model == "" {
        model = "gpt-5"
    }

    args := []string{"codex", "exec"}
    if opts.ResumeSessionID != "" {
        if opts.ForkSession {
            args = append(args, "fork", opts.ResumeSessionID, "-")
        } else {
            args = append(args, "resume", opts.ResumeSessionID, "-")
        }
    }

    args = append(args,
        "--json",
        "--dangerously-bypass-approvals-and-sandbox",
        "-m", model,
    )

    if opts.Effort != "" {
        args = append(args, "-c", `model_reasoning_effort="`+opts.Effort+`"`)
    }

    return Launch{Args: args, Stdin: prompt}, nil
}
```

Codex stream parser sample:

```go
func (Codex) ParseLine(line string) []Event {
    if line == "" || line[0] != '{' {
        return nil
    }

    var obj map[string]any
    if err := json.Unmarshal([]byte(line), &obj); err != nil {
        return nil
    }

    switch obj["type"] {
    case "thread.started":
        if id, ok := obj["thread_id"].(string); ok {
            return []Event{{Type: EventSessionID, SessionID: id}}
        }

    case "item.completed":
        item, _ := obj["item"].(map[string]any)
        if item["type"] == "agent_message" {
            if text, ok := item["text"].(string); ok {
                return []Event{
                    {Type: EventText, Text: text},
                    {Type: EventResult, Result: text},
                }
            }
        }

    case "item.started":
        item, _ := obj["item"].(map[string]any)
        if item["type"] == "command_execution" {
            if command, ok := item["command"].(string); ok {
                return []Event{{Type: EventToolCall, ToolName: "Bash", ToolArgs: command}}
            }
        }

    case "turn.completed":
        if usage, ok := parseCodexUsage(obj["usage"]); ok {
            return []Event{{Type: EventUsage, Usage: usage}}
        }

    case "error":
        if msg := extractErrorMessage(obj); msg != "" {
            return []Event{{Type: EventError, Result: msg}}
        }
    }

    return nil
}
```

Usage parser sample:

```go
func parseCodexUsage(raw any) (map[string]int, bool) {
    obj, ok := raw.(map[string]any)
    if !ok {
        return nil, false
    }

    input, ok1 := numberAsInt(obj["input_tokens"])
    cached, ok2 := numberAsInt(obj["cached_input_tokens"])
    output, ok3 := numberAsInt(obj["output_tokens"])
    if !ok1 || !ok2 || !ok3 {
        return nil, false
    }

    return map[string]int{
        "input_tokens":        input - cached,
        "cached_input_tokens": cached,
        "output_tokens":       output,
    }, true
}
```

## Runner Changes

Change `runner.Spec` to allow either legacy `Cmd` or a provider-driven launch.

```go
type Spec struct {
    Cmd              []string
    Provider         string
    Model            string
    Effort           string
    Prompt           string
    ResultPath       string
    Timeout          time.Duration
    RateLimitPattern string
}
```

Change `runner.Result` to include provider events:

```go
type Result struct {
    Stdout       string
    Stderr       string
    TimedOut     bool
    RateLimited  bool
    ResultExists bool
    ExitCode     int
    Duration     time.Duration
    SessionID    string
    FinalText    string
    Events       []providers.Event
}
```

New execution flow:

```go
func RunAgent(ctx context.Context, s Spec) (*Result, error) {
    launch, provider, err := buildLaunch(ctx, s)
    if err != nil {
        return nil, err
    }

    _ = os.Remove(s.ResultPath)

    cctx, cancel := context.WithTimeout(ctx, s.Timeout)
    defer cancel()

    cmd := exec.CommandContext(cctx, launch.Args[0], launch.Args[1:]...)
    if launch.Stdin != "" {
        cmd.Stdin = strings.NewReader(launch.Stdin)
    }

    stdout, _ := cmd.StdoutPipe()
    stderr, _ := cmd.StderrPipe()

    if err := cmd.Start(); err != nil {
        return nil, err
    }

    res := &Result{}
    stdoutDone := make(chan struct{})
    stderrDone := make(chan struct{})

    go scanStdout(stdout, provider, res, stdoutDone)
    go scanStderr(stderr, res, stderrDone)

    err = cmd.Wait()
    <-stdoutDone
    <-stderrDone

    if cmd.ProcessState != nil {
        res.ExitCode = cmd.ProcessState.ExitCode()
    }
    if errors.Is(cctx.Err(), context.DeadlineExceeded) {
        res.TimedOut = true
    }
    if _, statErr := os.Stat(s.ResultPath); statErr == nil {
        res.ResultExists = true
    }
    res.RateLimited = detectRateLimit(s.RateLimitPattern, res.Stdout, res.Stderr)

    _ = err
    return res, nil
}
```

Stdout scanner:

```go
func scanStdout(r io.Reader, p providers.Provider, res *Result, done chan<- struct{}) {
    defer close(done)

    scanner := bufio.NewScanner(r)
    scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

    for scanner.Scan() {
        line := scanner.Text()
        res.Stdout += line + "\n"

        for _, ev := range p.ParseLine(line) {
            res.Events = append(res.Events, ev)

            switch ev.Type {
            case providers.EventText:
                // Optional: append a compact text event to run.log later.
            case providers.EventResult:
                res.FinalText = ev.Result
            case providers.EventSessionID:
                res.SessionID = ev.SessionID
            }
        }
    }
}
```

For the first implementation, keep event logging simple: store event summaries
in `agent_run` events rather than logging every token-sized text delta.

## Config Changes

Keep existing raw command support for compatibility, but allow provider-based
agents:

```json
{
  "agents": {
    "claude_sonnet": {
      "provider": "claude",
      "model": "claude-sonnet-4-6",
      "dangerously_skip_permissions": true,
      "rate_limit_pattern": "rate_?limit|429|quota"
    },
    "codex_gpt5": {
      "provider": "codex",
      "model": "gpt-5",
      "effort": "medium"
    }
  }
}
```

Updated config type:

```go
type Agent struct {
    Cmd                       []string `json:"cmd,omitempty"`
    Provider                  string   `json:"provider,omitempty"`
    Model                     string   `json:"model,omitempty"`
    Effort                    string   `json:"effort,omitempty"`
    DangerouslySkipPermissions bool    `json:"dangerously_skip_permissions,omitempty"`
    RateLimitPattern          string   `json:"rate_limit_pattern,omitempty"`
}
```

Validation rule:

- If `cmd` is set, preserve the current `{{PROMPT}}` requirement.
- If `provider` is set, require `provider in {"claude","codex"}` and do not
  require `{{PROMPT}}`.
- Reject configs that set both `cmd` and `provider`.

## Role Invocation Changes

In `internal/commands/runcmd.go`, map config fields into `runner.Spec`:

```go
spec := runner.Spec{
    Cmd:              ag.Cmd,
    Provider:         ag.Provider,
    Model:            ag.Model,
    Effort:           ag.Effort,
    Prompt:           prompt,
    ResultPath:       filepath.Join(d.dir, role.ResultPath),
    Timeout:          time.Duration(role.TimeoutSeconds) * time.Second,
    RateLimitPattern: pattern,
}
```

Enhance the `agent_run` event:

```go
d.log.Log(eventlog.Event{Type: "agent_run", Fields: map[string]any{
    "role":          roleName,
    "agent":         agentName,
    "provider":      ag.Provider,
    "model":         ag.Model,
    "duration_s":    int(r.Duration.Seconds()),
    "timed_out":     r.TimedOut,
    "rate_limited":  r.RateLimited,
    "result_exists": r.ResultExists,
    "session_id":    r.SessionID,
    "final_text_tail": tailString(r.FinalText, 1000),
}})
```

Do not use `FinalText` as the role contract yet. The role contract remains:

```text
.orquestalite/results/<role>.json
```

The stream data is observability and future resume metadata.

## Error Handling

Copy Sandcastle's practical fallback order for failed subprocesses:

1. Prefer stderr.
2. If stderr is empty, use parsed provider error/result text.
3. If still empty, use the last non-empty stdout lines.

Go helper:

```go
func errorDetail(res *runner.Result) string {
    if strings.TrimSpace(res.Stderr) != "" {
        return res.Stderr
    }
    if strings.TrimSpace(res.FinalText) != "" {
        return res.FinalText
    }
    return lastNonEmptyLines(res.Stdout, 20)
}
```

This matters because Codex and other CLIs often emit structured API errors on
stdout rather than stderr.

## Tests

Add focused unit tests before changing command behavior:

1. `internal/providers/claude_test.go`
   - `Build` includes `-p -`.
   - `Build` places prompt in `Launch.Stdin`.
   - `ParseLine` extracts `session_id` from Claude init event.
   - `ParseLine` extracts final result text.
   - `ParseLine` extracts tool calls from assistant message blocks.

2. `internal/providers/codex_test.go`
   - fresh `Build` starts with `codex exec`.
   - resume `Build` uses `codex exec resume <id> -`.
   - fork `Build` uses `codex exec fork <id> -`.
   - `ParseLine` extracts `thread_id`.
   - `ParseLine` extracts command execution tool calls.
   - `ParseLine` extracts `turn.completed` usage.

3. `internal/runner/runner_test.go`
   - fake provider command receives prompt through stdin.
   - stdout JSONL is parsed into events while still captured as raw stdout.
   - legacy `cmd` mode still works.
   - stale result file is removed before invocation.
   - missing result file still reports `ResultExists == false`.

4. `internal/config/config_test.go`
   - provider-based agents validate without `{{PROMPT}}`.
   - legacy cmd agents still require `{{PROMPT}}`.
   - config cannot specify both `cmd` and `provider`.
   - unknown provider is rejected.

## Migration Strategy

Step 1: Add provider package.

- Add `internal/providers/provider.go`.
- Add `internal/providers/claude.go`.
- Add `internal/providers/codex.go`.
- Add unit tests.

Step 2: Extend config.

- Add optional `provider`, `model`, `effort`, and
  `dangerously_skip_permissions`.
- Keep existing `cmd` configs valid.

Step 3: Extend runner.

- Add provider launch path.
- Preserve legacy command path.
- Switch from `cmd.Run()` with full buffers to `StdoutPipe`/`StderrPipe`
  scanning.

Step 4: Log useful stream metadata.

- Add `session_id`, `provider`, `model`, `final_text_tail`, and maybe
  `tool_calls_count` to `agent_run`.
- Avoid logging every text delta initially.

Step 5: Update scaffolded `team.json`.

- Prefer provider agents in the default scaffold.
- Keep examples for raw `cmd` in docs for unsupported tools.

Suggested default:

```json
{
  "agents": {
    "claude_sonnet": {
      "provider": "claude",
      "model": "claude-sonnet-4-6",
      "dangerously_skip_permissions": true,
      "rate_limit_pattern": "rate_?limit|429|quota"
    },
    "claude_opus": {
      "provider": "claude",
      "model": "claude-opus-4-7",
      "dangerously_skip_permissions": true
    },
    "codex_gpt5": {
      "provider": "codex",
      "model": "gpt-5",
      "effort": "medium"
    }
  }
}
```

## Acceptance Criteria

- Claude and Codex prompts are passed through stdin.
- Existing role loop behavior is unchanged.
- Existing result JSON contracts remain the source of truth.
- Legacy `cmd` agents continue to work.
- Provider-based agents expose session IDs in `run.log` when available.
- Provider stream errors are visible in failure diagnostics.
- Tests cover provider command construction, stream parsing, runner stdin, and
  config validation.

## Non-Goals To Revisit Later

- Session resume/fork as user-facing orquesta-lite commands.
- Persisting provider session files.
- Parallel task execution.
- Branch or worktree isolation.
- Sandbox/container execution.
