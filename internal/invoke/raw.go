package invoke

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/collectiveai-team/orquesta-lite/internal/artifacts"
	"github.com/collectiveai-team/orquesta-lite/internal/memory"
	"github.com/collectiveai-team/orquesta-lite/internal/prompts"
)

// ErrInvalidContract marks fallback exhaustion where every usable agent wrote
// output that failed the requested runtime contract.
var ErrInvalidContract = errors.New("all agent outputs violated the contract")

// ErrAgentTimeout marks fallback exhaustion where the last usable agent was
// terminated by its configured timeout. Callers use this stable sentinel to
// preserve timeout semantics across the generic agent.invoke boundary.
var ErrAgentTimeout = errors.New("all usable agents timed out")

// ErrAgentAborted marks a run cancelled outside orq-lite — someone aborting the
// agent's session from the opencode TUI. Unlike a timeout or a missing result it
// is never retried: the cancellation was the point. It is a distinct sentinel so
// callers can tell "a human stopped this" apart from "the agent failed".
var ErrAgentAborted = errors.New("agent run was aborted")

// RawValidator validates the exact JSON bytes written by an agent. Returning
// an error classifies that agent attempt as invalid_contract and advances the
// configured fallback chain.
type RawValidator func([]byte) error

// RawOutcome carries an invocation's bytes together with what producing them
// cost. The cost is reported even when the invocation fails, because a chain
// that burned three providers and then gave up is exactly the spend a run-wide
// budget has to see.
type RawOutcome struct {
	Output  []byte
	CostUSD float64
}

// Raw invokes any configured role without a compile-time result type.
func Raw(ctx context.Context, inv *RoleInvoker, roleName string, call RoleCall, rc RunContext, validate RawValidator) (RawOutcome, error) {
	var outcome RawOutcome
	if inv == nil {
		return outcome, fmt.Errorf("role invoker is nil")
	}
	spec, ok := inv.Specs[roleName]
	if !ok {
		return outcome, fmt.Errorf("role %q is not configured", roleName)
	}
	roleVars := call.templateVars()
	promptPath := call.PromptPath
	if promptPath == "" {
		promptPath = spec.PromptPath
	}
	resultPath := call.ResultPath
	if resultPath == "" {
		resultPath = spec.ResultPath
	}
	archiveRole := call.ArchiveRole
	if archiveRole == "" {
		archiveRole = roleName
	}
	mem, _ := memory.ReadAll(inv.MemPath)
	roleVars["MEMORY"] = mem
	roleVars["CONVENTIONS"] = inv.readConventions()
	skillsText, err := inv.skillsFor(call.Skills)
	if err != nil {
		return outcome, err
	}
	roleVars["SKILLS"] = skillsText
	template, err := prompts.Load(absPath(inv.Dir, promptPath))
	if err != nil {
		return outcome, err
	}
	prompt := prompts.Interpolate(template, roleVars)
	resultAbs := absPath(inv.Dir, resultPath)
	validatePath := func(path string) error {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if validate != nil {
			return validate(raw)
		}
		return nil
	}
	spend, err := inv.runValidated(ctx, roleName, spec, call.AgentOverride, prompt, resultPath, resultAbs, rc, validatePath)
	outcome.CostUSD = spend
	if err != nil {
		return outcome, err
	}
	raw, err := os.ReadFile(resultAbs)
	if err != nil {
		return outcome, fmt.Errorf("read role result %s: %w", resultAbs, err)
	}
	if err := artifacts.ArchiveResult(inv.Dir, archiveRole, rc.TaskID, rc.Cycle, rc.Attempt, raw); err != nil {
		return outcome, err
	}
	outcome.Output = raw
	return outcome, nil
}
