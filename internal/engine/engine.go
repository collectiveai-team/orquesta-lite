// Package engine implements a generic, configuration-driven flow engine for
// orquesta-lite. A flow is a JSON document (flows.json) describing a sequence of
// steps (agent invocations, shell commands, native Go actions, loops, and
// retry-until control blocks) that operate over a shared Context.
//
// The engine replaces the hardcoded run/factory/review pipelines with a
// data-driven interpreter. Behaviour previously encoded in
// internal/loops and internal/commands can now be expressed as a flows.json
// entry and exercised without touching Go source.
//
// # Interpolation
//
// Strings referenced by step `inputs` and `command` fields are interpolated
// against the Context using single-brace tokens: `{a.b.c}` resolves
// `ctx["a"].(map)["b"].(map)["c"]`. Tokens that do not resolve are left intact
// so literal braces (e.g. shell `${VAR}`, JSON bodies) survive untouched.
//
// # Outputs
//
// Each producing step (agent / command / action / eval) stores its result in
// the Context under the keys named in `outputs`. The `outputs` value is the
// source path into the produced value: `"."` is the whole value; otherwise a
// dotted path. Downstream steps read these keys via the same interpolation.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Flows is the top-level document parsed from flows.json.
type Flows struct {
	Flows map[string]Flow `json:"flows"`
}

// Flow is one named, executable pipeline.
type Flow struct {
	Description string               `json:"description,omitempty"`
	Inputs      map[string]InputSpec `json:"inputs,omitempty"`
	Steps       []Step               `json:"steps"`
}

// InputSpec declares a flow input parameter.
type InputSpec struct {
	Type    string `json:"type,omitempty"`
	Default any    `json:"default,omitempty"`
	// HasDefault records whether the flow declared a "default" key at all
	// ("default": null counts as declared). The engine seeds undeclared
	// inputs with nil either way; the flow catalog endpoint uses this to
	// mark inputs required.
	HasDefault bool `json:"-"`
}

func (s *InputSpec) UnmarshalJSON(b []byte) error {
	var aux struct {
		Type    string          `json:"type"`
		Default json.RawMessage `json:"default"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	s.Type = aux.Type
	s.HasDefault = aux.Default != nil
	s.Default = nil
	if aux.Default != nil {
		if err := json.Unmarshal(aux.Default, &s.Default); err != nil {
			return err
		}
	}
	return nil
}

// Step is one execution block. Only the fields relevant to `Type` are used.
type Step struct {
	Type    string `json:"type"`
	Agent   string `json:"agent,omitempty"`
	Command string `json:"command,omitempty"`
	// Args is the argv form of a command step: each element is interpolated and
	// shell-quoted independently, then the call is run via `sh -c` of the joined,
	// safely-quoted string. Use Args (not Command) whenever an interpolated value
	// may contain spaces, quotes, or shell metacharacters (commit messages, PR
	// bodies, branch names) — it prevents word-splitting and shell injection.
	// Command remains for genuine shell pipelines (`a && b`, globs, redirects).
	Args       []string          `json:"args,omitempty"`
	Action     string            `json:"action,omitempty"`
	Inputs     map[string]any    `json:"inputs,omitempty"`
	Outputs    map[string]string `json:"outputs,omitempty"`
	Iterator   string            `json:"iterator,omitempty"`
	As         string            `json:"as,omitempty"`
	Body       []Step            `json:"body,omitempty"`
	Condition  string            `json:"condition,omitempty"`
	MaxRetries int               `json:"max_retries,omitempty"`
	Expression string            `json:"expression,omitempty"`
	// OnFailure governs step error handling. The default ("" / "abort")
	// aborts the flow. "continue" logs and proceeds — useful for `go vet`
	// gates or best-effort commits that may be no-ops.
	OnFailure string `json:"on_failure,omitempty"`
}

// Context is a thread-safe key-value store with dotted-path access for nested
// maps. Slices are addressed by integer index. Unknown segments are left
// untouched by interpolation.
type Context struct {
	mu   sync.RWMutex
	data map[string]any
}

// NewContext returns an empty Context.
func NewContext() *Context { return &Context{data: map[string]any{}} }

// Set stores a value at a dotted path, creating intermediate maps as needed.
// Numeric segments target slice indices when the parent is a slice.
func (c *Context) Set(path string, v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setLocked(strings.Split(path, "."), v)
}

func (c *Context) setLocked(segs []string, v any) {
	if len(segs) == 0 {
		return
	}
	if len(segs) == 1 {
		c.data[segs[0]] = v
		return
	}
	head, tail := segs[0], segs[1:]
	cur, ok := c.data[head]
	if !ok {
		next := map[string]any{}
		c.data[head] = next
		cur = next
	}
	m, ok := cur.(map[string]any)
	if !ok {
		next := map[string]any{}
		c.data[head] = next
		m = next
	}
	sub := &Context{data: m}
	sub.setLocked(tail, v)
}

// Get resolves a dotted path. Returns (value, true) when every segment
// resolves; (nil, false) otherwise. Slice indices must be numeric strings.
func (c *Context) Get(path string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.getLocked(strings.Split(path, "."))
}

func (c *Context) getLocked(segs []string) (any, bool) {
	if len(segs) == 0 {
		return c.data, true
	}
	cur, ok := c.data[segs[0]]
	if !ok {
		return nil, false
	}
	for _, seg := range segs[1:] {
		switch m := cur.(type) {
		case map[string]any:
			v, ok := m[seg]
			if !ok {
				return nil, false
			}
			cur = v
		default:
			return nil, false
		}
	}
	return cur, true
}

var tokenRe = regexp.MustCompile(`\{([a-zA-Z_][a-zA-Z0-9_.]*)\}`)

// Interpolate replaces `{dotted.path}` tokens that resolve in the Context with
// their rendered value. Unresolved tokens are left intact.
func (c *Context) Interpolate(s string) string {
	return tokenRe.ReplaceAllStringFunc(s, func(tok string) string {
		inner := tok[1 : len(tok)-1]
		v, ok := c.Get(inner)
		if !ok {
			return tok
		}
		return render(v)
	})
}

// InterpolateBlank is like Interpolate but renders unresolved `{path}` tokens
// as the empty string instead of leaving them intact. It is used for agent
// `inputs`, where a not-yet-produced feedback variable (e.g. `{tester_res.failures}`
// on the first retry attempt) must vanish rather than leak a literal brace token
// into the prompt.
func (c *Context) InterpolateBlank(s string) string {
	return tokenRe.ReplaceAllStringFunc(s, func(tok string) string {
		inner := tok[1 : len(tok)-1]
		v, ok := c.Get(inner)
		if !ok {
			return ""
		}
		return render(v)
	})
}

// InterpolateValue resolves a value that may be a string (interpolated) or any
// literal JSON value (numbers, bools, arrays, objects) — passed through. When
// a string is exactly one interpolation token (`{path}`), the raw underlying
// value is returned so actions and agent inputs receive structured values
// (lists, maps, bools) instead of their string rendering.
func (c *Context) InterpolateValue(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	if loc := tokenRe.FindStringSubmatchIndex(s); loc != nil && loc[0] == 0 && loc[1] == len(s) {
		if raw, ok := c.Get(s[1 : len(s)-1]); ok {
			return raw
		}
	}
	return c.Interpolate(s)
}

// render formats a context value for interpolation into a string.
func render(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = render(e)
		}
		return strings.Join(parts, ", ")
	case []string:
		return strings.Join(x, ", ")
	default:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", v)
	}
}

// Engine interprets a Flow against its dependencies.
type Engine struct {
	flow    *Flow
	agent   AgentRunner
	command CommandRunner
	actions map[string]ActionFunc
	log     Log
	dir     string
	// cycle and attempt track the current loop iteration (1-based) and
	// retry_until attempt (1-based) so agent calls carry an invoke.RunContext
	// that distinguishes per-attempt result archives and provider sessions.
	// Execution is strictly sequential, so plain fields are sufficient.
	cycle   int
	attempt int
}

// Log is the minimal event surface the engine needs. Implementations may wrap
// eventlog.Logger or collect events in tests.
type Log interface {
	Event(name string, fields map[string]any)
}

// noopLog drops events.
type noopLog struct{}

func (noopLog) Event(string, map[string]any) {}

// Option configures an Engine.
type Option func(*Engine)

// WithLog sets a structured logger.
func WithLog(l Log) Option {
	return func(e *Engine) { e.log = l }
}

// WithDir sets the project working directory used by command steps.
func WithDir(dir string) Option {
	return func(e *Engine) { e.dir = dir }
}

// New builds an Interpreter for the given flow. agent and command runners are
// required; defaults (noopLog, BuiltinActions) fill the rest.
func New(flow *Flow, agent AgentRunner, command CommandRunner, opts ...Option) *Engine {
	e := &Engine{
		flow:    flow,
		agent:   agent,
		command: command,
		actions: BuiltinActions(),
		log:     noopLog{},
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Run executes the flow with the given input overrides. Inputs declared in the
// flow are seeded under the "inputs" namespace (e.g. `{inputs.features_path}`).
func (e *Engine) Run(ctx context.Context, inputs map[string]any) error {
	c := NewContext()
	merged := map[string]any{}
	for k, spec := range e.flow.Inputs {
		merged[k] = spec.Default
	}
	for k, v := range inputs {
		merged[k] = v
	}
	for k, v := range merged {
		c.Set("inputs."+k, v)
	}
	return e.runSteps(ctx, c, e.flow.Steps)
}

// runSteps executes a sequence of steps sharing the given Context.
func (e *Engine) runSteps(ctx context.Context, c *Context, steps []Step) error {
	for i := range steps {
		if err := e.runStep(ctx, c, steps[i]); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) runStep(ctx context.Context, c *Context, s Step) error {
	switch s.Type {
	case "agent":
		return e.runAgent(ctx, c, s)
	case "command":
		return e.runCommand(ctx, c, s)
	case "action":
		return e.runAction(ctx, c, s)
	case "loop":
		return e.runLoop(ctx, c, s)
	case "retry_until":
		return e.runRetry(ctx, c, s)
	case "eval":
		return e.runEval(ctx, c, s)
	default:
		return fmt.Errorf("engine: unknown step type %q", s.Type)
	}
}

// RunContext mirrors invoke.RunContext for steps that produce agent calls.
type RunContext struct {
	TaskID  string
	Cycle   int
	Attempt int
}

// applyOutputs stores a produced value into the Context according to an
// `outputs` map of {contextKey: sourcePath}. "." stores the whole value.
func applyOutputs(c *Context, outputs map[string]string, produced any) {
	for key, src := range outputs {
		v := produced
		if src != "" && src != "." {
			sub, ok := dig(produced, src)
			if !ok {
				continue
			}
			v = sub
		}
		c.Set(key, v)
	}
}

// dig resolves a dotted path into a map[string]any / []any / map value.
func dig(v any, path string) (any, bool) {
	cur := v
	for _, seg := range strings.Split(path, ".") {
		switch m := cur.(type) {
		case map[string]any:
			nv, ok := m[seg]
			if !ok {
				return nil, false
			}
			cur = nv
		default:
			return nil, false
		}
	}
	return cur, true
}

// ReferencedRoles returns the sorted set of agent roles referenced by any
// agent step, recursively through loop/retry_until bodies. Shared by the
// flow-run preflight and the GET /api/flows catalog.
func (f *Flow) ReferencedRoles() []string {
	seen := map[string]bool{}
	var walk func(steps []Step)
	walk = func(steps []Step) {
		for _, s := range steps {
			if s.Agent != "" {
				seen[s.Agent] = true
			}
			if len(s.Body) > 0 {
				walk(s.Body)
			}
		}
	}
	walk(f.Steps)
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// LoadFlows reads and parses a flows.json file.
func LoadFlows(path string) (*Flows, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load flows %s: %w", path, err)
	}
	var f Flows
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse flows %s: %w", path, err)
	}
	return &f, nil
}
