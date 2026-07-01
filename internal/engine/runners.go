package engine

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// AgentRunner runs a configured role for one prompt and returns the parsed
// result contract as a generic map. Live implementations wrap
// invoke.RoleInvoker + invoke.Role.
type AgentRunner interface {
	RunRole(ctx context.Context, role string, vars map[string]string, rc RunContext) (map[string]any, error)
}

// CommandRunner runs a shell command and returns its stdout. A non-zero exit
// is reported as an error carrying the exit code.
type CommandRunner interface {
	Run(ctx context.Context, dir, command string) (stdout string, exitCode int, err error)
}

// ActionFunc is the escape hatch for native Go logic too complex for a shell
// one-liner (feature extraction, batched-prompt formatting). It receives the
// interpolated step `inputs` and returns a value stored via `outputs`.
type ActionFunc func(ctx context.Context, inputs map[string]any) (any, error)

// runAgent maps an `agent` step onto the AgentRunner.
func (e *Engine) runAgent(ctx context.Context, c *Context, s Step) error {
	vars := make(map[string]string, len(s.Inputs))
	for k, v := range s.Inputs {
		// Strings (the common case) blank out unresolved tokens so a feedback
		// var that has not been produced yet vanishes; non-string literals pass
		// through render unchanged.
		if str, ok := v.(string); ok {
			vars[k] = c.InterpolateBlank(str)
		} else {
			vars[k] = render(v)
		}
	}
	rc := RunContext{Cycle: e.cycle, Attempt: e.attempt}
	if id, ok := vars["TASK_ID"]; ok {
		rc.TaskID = id
	}
	e.log.Event("flow_agent_start", map[string]any{
		"role":    s.Agent,
		"task":    rc.TaskID,
		"attempt": e.attempt,
		"vars":    vars,
	})
	res, err := e.agent.RunRole(ctx, s.Agent, vars, rc)
	if err != nil {
		if s.OnFailure == "continue" {
			e.log.Event("flow_agent_failed_continue", map[string]any{"role": s.Agent, "error": err.Error()})
			return nil
		}
		return fmt.Errorf("agent %q: %w", s.Agent, err)
	}
	derivePass(res)
	applyOutputs(c, s.Outputs, res)
	e.log.Event("flow_agent_done", map[string]any{"role": s.Agent})
	return nil
}

// runCommand executes a `command` step against the shell. The structured result
// stored under `outputs` is `{"stdout":..., "exit":N, "pass":exit==0}`, so a
// downstream eval can gate on `{lint_res.pass}`. A source path of "stdout" or
// "." selects just stdout; "pass" the bool; "exit" the code.
func (e *Engine) runCommand(ctx context.Context, c *Context, s Step) error {
	cmd, err := e.resolveCommand(c, s)
	if err != nil {
		return err
	}
	e.log.Event("flow_command_start", map[string]any{"command": cmd})
	stdout, code, err := e.command.Run(ctx, e.dir, cmd)
	res := map[string]any{
		"stdout": strings.TrimRight(stdout, "\n"),
		"exit":   code,
		"pass":   err == nil,
	}
	if err != nil {
		e.log.Event("flow_command_failed_continue", map[string]any{
			"command": cmd, "exit": code, "error": err.Error(),
		})
		if s.OnFailure != "continue" {
			return fmt.Errorf("command %q exited %d: %w", cmd, code, err)
		}
	}
	applyOutputs(c, s.Outputs, res)
	return nil
}

// resolveCommand builds the shell string for a command step. The argv form
// (s.Args) interpolates and shell-quotes each element independently, so an
// interpolated value containing spaces or quotes cannot word-split or inject;
// the legacy string form (s.Command) is interpolated verbatim for genuine shell
// pipelines. Exactly one of the two must be set.
func (e *Engine) resolveCommand(c *Context, s Step) (string, error) {
	hasArgs := len(s.Args) > 0
	hasCmd := strings.TrimSpace(s.Command) != ""
	switch {
	case hasArgs && hasCmd:
		return "", fmt.Errorf("command: set either `command` or `args`, not both")
	case hasArgs:
		parts := make([]string, len(s.Args))
		for i, a := range s.Args {
			parts[i] = shellQuote(render(c.InterpolateValue(a)))
		}
		return strings.Join(parts, " "), nil
	case hasCmd:
		return c.Interpolate(s.Command), nil
	default:
		return "", fmt.Errorf("command: missing `command` or `args`")
	}
}

// shellQuote wraps s in single quotes, escaping embedded single quotes, so the
// result is a single safe POSIX shell word regardless of its content.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// runAction dispatches a named native action.
func (e *Engine) runAction(ctx context.Context, c *Context, s Step) error {
	fn, ok := e.actions[s.Action]
	if !ok {
		return fmt.Errorf("engine: unknown action %q", s.Action)
	}
	inputs := make(map[string]any, len(s.Inputs))
	for k, v := range s.Inputs {
		inputs[k] = c.InterpolateValue(v)
	}
	out, err := fn(ctx, inputs)
	if err != nil {
		if s.OnFailure == "continue" {
			e.log.Event("flow_action_failed_continue", map[string]any{"action": s.Action, "error": err.Error()})
			return nil
		}
		return fmt.Errorf("action %q: %w", s.Action, err)
	}
	applyOutputs(c, s.Outputs, out)
	return nil
}

// runLoop iterates over a list from the Context, exposing each element under
// `as` and running the body steps for each.
func (e *Engine) runLoop(ctx context.Context, c *Context, s Step) error {
	iterPath := strings.Trim(s.Iterator, "{} ")
	list, ok := c.Get(iterPath)
	if !ok {
		return fmt.Errorf("loop: iterator %q not found in context", s.Iterator)
	}
	seq, err := toSlice(list)
	if err != nil {
		return fmt.Errorf("loop: iterator %q: %w", s.Iterator, err)
	}
	if s.As == "" {
		return fmt.Errorf("loop: missing `as`")
	}
	savedCycle := e.cycle
	for i, el := range seq {
		c.Set(s.As, el)
		c.Set("_index", i)
		c.Set("_count", len(seq))
		e.cycle = i + 1
		e.log.Event("flow_loop_iter", map[string]any{"as": s.As, "index": i, "count": len(seq)})
		if err := e.runSteps(ctx, c, s.Body); err != nil {
			e.cycle = savedCycle
			return fmt.Errorf("loop %s[%d]: %w", s.As, i, err)
		}
	}
	e.cycle = savedCycle
	return nil
}

// runRetry repeats the body until `condition` is true or MaxRetries is hit.
// Each iteration the variables produced by the body are re-evaluated.
func (e *Engine) runRetry(ctx context.Context, c *Context, s Step) error {
	if s.Condition == "" {
		return fmt.Errorf("retry_until: missing `condition`")
	}
	max := s.MaxRetries
	if max <= 0 {
		max = 1
	}
	savedAttempt := e.attempt
	defer func() { e.attempt = savedAttempt }()
	for attempt := 1; attempt <= max; attempt++ {
		e.attempt = attempt
		c.Set("_attempt", attempt)
		if err := e.runSteps(ctx, c, s.Body); err != nil {
			return fmt.Errorf("retry_until attempt %d: %w", attempt, err)
		}
		ok, err := e.evalExpr(c, s.Condition)
		if err != nil {
			return fmt.Errorf("retry_until condition: %w", err)
		}
		e.log.Event("flow_retry_check", map[string]any{
			"attempt":   attempt,
			"condition": s.Condition,
			"passed":    ok,
		})
		if ok {
			return nil
		}
	}
	return fmt.Errorf("retry_until: condition %q not satisfied after %d attempts", s.Condition, max)
}

// toSlice coerces a context value to []any.
func toSlice(v any) ([]any, error) {
	switch x := v.(type) {
	case []any:
		return x, nil
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out, nil
	case []map[string]any:
		out := make([]any, len(x))
		for i, m := range x {
			out[i] = m
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected a list, got %T", v)
	}
}

// derivePass injects a uniform `pass` boolean into an agent result map when it
// carries a `status` field, so flow authors can gate on `{role_res.pass}`
// regardless of the role-specific status vocabulary (pass/approved/completed).
func derivePass(res map[string]any) {
	status, _ := res["status"].(string)
	if status == "" {
		return
	}
	pass := status == "pass" || status == "approved" || status == "completed"
	res["pass"] = pass
}

// ExecCommandRunner is the production CommandRunner: it shells out via `sh -c`.
type ExecCommandRunner struct{}

func (ExecCommandRunner) Run(ctx context.Context, dir, command string) (string, int, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	err := cmd.Run()
	exit := 0
	if cmd.ProcessState != nil {
		exit = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		return stdout.String(), exit, err
	}
	return stdout.String(), 0, nil
}
