package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/lionelchamorro/orquestalite/internal/engine"
	"github.com/lionelchamorro/orquestalite/internal/eventlog"
)

// FlowOptions configures the generic flow engine runner.
type FlowOptions struct {
	ProjectDir string
	TeamPath   string
	FlowsPath  string
	FlowName   string
	InputArgs  []string // raw "key=value" overrides
	LogFormat  eventlog.Format
}

// Run executes a named flow from flows.json, wiring the engine's agent runner
// to the same RoleInvoker (fallback / rate-limit / health / sessions / memory)
// used by the live `run`/`factory` commands, so flows.json gets the production
// behaviour without reimplementing it.
func RunFlow(ctx context.Context, opts FlowOptions) error {
	if opts.FlowName == "" {
		return fmt.Errorf("flow: a flow name is required")
	}
	if opts.FlowsPath == "" {
		opts.FlowsPath = "flows.json"
	}
	flows, err := engine.LoadFlows(opts.FlowsPath)
	if err != nil {
		return err
	}
	flow, ok := flows.Flows[opts.FlowName]
	if !ok {
		available := availableFlowNames(flows)
		return fmt.Errorf("flow %q not found in %s (available: %s)", opts.FlowName, opts.FlowsPath, strings.Join(available, ", "))
	}

	deps, cleanup, err := newLiveDeps(liveDepsOptions{
		ProjectDir: opts.ProjectDir,
		TeamPath:   opts.TeamPath,
		LogFormat:  opts.LogFormat,
		Roles:      flowReferencedRoles(&flow),
	})
	if err != nil {
		return err
	}
	defer cleanup()

	inputs, err := parseInputArgs(opts.InputArgs)
	if err != nil {
		return err
	}

	log := &flowEventLog{log: deps.log}
	eng := engine.New(&flow, &engine.LiveAgentRunner{Inv: deps.inv}, &engine.ExecCommandRunner{},
		engine.WithLog(log), engine.WithDir(deps.dir))
	return eng.Run(ctx, inputs)
}

func availableFlowNames(flows *engine.Flows) []string {
	names := make([]string, 0, len(flows.Flows))
	for n := range flows.Flows {
		names = append(names, n)
	}
	return names
}

// ListFlowNames returns the flow names declared in a flows.json file, or nil if
// it cannot be read or parsed. Used by the CLI's `flow` help output.
func ListFlowNames(flowsPath string) []string {
	flows, err := engine.LoadFlows(flowsPath)
	if err != nil {
		return nil
	}
	return availableFlowNames(flows)
}

// flowReferencedRoles collects every `agent` step's role name (across nested
// bodies) so newLiveDeps' static preflight covers the agents the flow uses.
func flowReferencedRoles(flow *engine.Flow) []string {
	seen := map[string]bool{}
	var walk func(steps []engine.Step)
	walk = func(steps []engine.Step) {
		for _, s := range steps {
			if s.Agent != "" {
				seen[s.Agent] = true
			}
			if len(s.Body) > 0 {
				walk(s.Body)
			}
		}
	}
	walk(flow.Steps)
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	return out
}

func parseInputArgs(args []string) (map[string]any, error) {
	out := map[string]any{}
	for _, a := range args {
		if i := strings.IndexByte(a, '='); i > 0 {
			out[a[:i]] = a[i+1:]
		} else {
			return nil, fmt.Errorf("invalid input %q (expected key=value)", a)
		}
	}
	return out, nil
}

// flowEventLog adapts the project's *eventlog.Logger to the engine's Log
// interface.
type flowEventLog struct{ log *eventlog.Logger }

func (f *flowEventLog) Event(name string, fields map[string]any) {
	if f.log == nil {
		return
	}
	f.log.Log(eventlog.Event{Type: name, Fields: fields})
}
