package invoke

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// ErrUsageThreshold marks fallback exhaustion where every eligible agent was
// skipped before execution because its provider subscription is at (or its
// usage could not safely be checked against) a configured threshold.
var ErrUsageThreshold = errors.New("all usable agents exceed provider usage thresholds")

var ErrSingleAttemptExhausted = errors.New("single correction provider attempt exhausted")

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
	if inv != nil {
		if call.Dir != "" && call.PromptPath != "" {
			call.PromptPath = absPath(inv.Dir, call.PromptPath)
		}
		if call.Dir != "" && call.ResultPath != "" {
			call.ResultPath = absPath(inv.Dir, call.ResultPath)
		}
		var scopeErr error
		inv, scopeErr = scopedInvoker(inv, call)
		if scopeErr != nil {
			return outcome, scopeErr
		}
	}
	if inv == nil {
		return outcome, fmt.Errorf("role invoker is nil")
	}
	var acknowledgementErr error
	inv, acknowledgementErr = acknowledgedInvoker(inv, roleName, rc.TaskID)
	if acknowledgementErr != nil {
		return outcome, acknowledgementErr
	}
	spec, ok := inv.Specs[roleName]
	if !ok {
		return outcome, fmt.Errorf("role %q is not configured", roleName)
	}
	roleVars := call.templateVars()
	if err := inv.addSupervisorContext(roleName, rc.TaskID, roleVars); err != nil {
		return outcome, err
	}
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
	// Older pinned templates spell out the configured role result path.
	// Rewrite the template before interpolating untrusted context, so their
	// contract remains readable without sharing writable result files.
	if call.ResultPath != "" && spec.ResultPath != "" {
		template = strings.ReplaceAll(template, spec.ResultPath, "{{RESULT_PATH}}")
	}
	roleVars["RESULT_PATH"] = absPath(inv.Dir, resultPath)
	if inv.ReadOnly {
		template += "\nREAD-ONLY REVIEW: Do not write files. Return the entire result as strict JSON in your final response. The runtime will persist it.\n"
	}
	prompt := prompts.Interpolate(template, roleVars)
	if controlled, err := inv.controlledPrompt(roleName, template, roleVars); err != nil {
		return outcome, err
	} else if controlled != "" {
		prompt = controlled
	} else {
		prompt = appendSupervisorContext(prompt, roleVars)
	}
	resultAbs := absPath(inv.Dir, resultPath)
	if err := os.MkdirAll(filepath.Dir(resultAbs), 0700); err != nil {
		return outcome, err
	}
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
	archiveDir := inv.ArchiveDir
	if archiveDir == "" {
		archiveDir = inv.Dir
	}
	if err := artifacts.ArchiveResult(archiveDir, archiveRole, rc.TaskID, rc.Cycle, rc.Attempt, raw); err != nil {
		return outcome, err
	}
	outcome.Output = raw
	return outcome, nil
}
