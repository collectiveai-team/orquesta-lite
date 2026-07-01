package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/lionelchamorro/orquestalite/internal/invoke"
)

// rawResult is the generic result-contract shape used by the engine's agent
// runner. It stores the agent's JSON output as an untyped map so downstream
// steps can address nested fields via `{coder_res.files_changed}` style
// interpolation, while still satisfying invoke.MemoryNoting so invoke.Role's
// memory-append path applies unchanged.
type rawResult struct {
	data map[string]any
	note *string
}

func (r rawResult) MemoryNote() *string { return r.note }

func parseRaw(path string) (*rawResult, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read result %s: %w", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse result %s: %w", path, err)
	}
	var note *string
	if n, ok := m["notes_for_memory"]; ok {
		if s, ok := n.(string); ok && s != "" {
			note = &s
		}
	}
	return &rawResult{data: m, note: note}, nil
}

// LiveAgentRunner is the production AgentRunner: it delegates to a configured
// invoke.RoleInvoker, reusing the existing fallback / rate-limit / health /
// session machinery instead of reimplementing it.
type LiveAgentRunner struct {
	Inv *invoke.RoleInvoker
}

func (l *LiveAgentRunner) RunRole(ctx context.Context, role string, vars map[string]string, rc RunContext) (map[string]any, error) {
	call := invoke.RoleCall{Vars: vars}
	irc := invoke.RunContext{TaskID: rc.TaskID, Cycle: rc.Cycle, Attempt: rc.Attempt}
	res, err := invoke.Role[rawResult](ctx, l.Inv, role, call, irc, parseRaw)
	if err != nil {
		return nil, err
	}
	return res.data, nil
}
