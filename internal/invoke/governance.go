package invoke

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/config"
	"github.com/collectiveai-team/orquesta-lite/internal/eventlog"
	"github.com/collectiveai-team/orquesta-lite/internal/promptbudget"
	"github.com/collectiveai-team/orquesta-lite/internal/providers"
	"github.com/collectiveai-team/orquesta-lite/internal/runner"
	"github.com/collectiveai-team/orquesta-lite/internal/supervision"
	"github.com/collectiveai-team/orquesta-lite/internal/telemetry"
)

func (inv *RoleInvoker) controlledPrompt(role, template string, vars map[string]string) (string, error) {
	p, enabled := inv.PromptBudgets[role]
	if !enabled {
		return "", nil
	}
	stable := map[string]string{}
	dynamic := map[string]string{}
	for key, value := range vars {
		if key == "SKILLS" || key == "CONVENTIONS" {
			stable[key] = value
		} else {
			dynamic[key] = value
		}
	}
	prompt, m := promptbudget.Assemble(template, stable, dynamic)
	if inv.TelemetryRoot != "" {
		if err := os.MkdirAll(inv.TelemetryRoot, 0700); err != nil {
			return "", err
		}
		raw, _ := json.MarshalIndent(map[string]any{"role": role, "measurement": m, "policy": p}, "", "  ")
		name := fmt.Sprintf("prompt-%x.json", sha256.Sum256([]byte(role+"\x00"+m.PrefixHash)))
		if err := os.WriteFile(filepath.Join(inv.TelemetryRoot, name), raw, 0600); err != nil {
			return "", err
		}
	}
	if inv.Log != nil {
		inv.Log.Log(eventlog.Event{Type: "prompt_budget", Fields: map[string]any{"role": role, "measurement": m, "policy": p}})
	}
	if err := p.Check(m); err != nil {
		return "", err
	}
	return prompt, nil
}

func scopedInvoker(inv *RoleInvoker, call RoleCall) (*RoleInvoker, error) {
	if call.Dir == "" && !call.FreshContext && !call.ReadOnly && !call.SingleAttempt && !call.DisableNativeDelegation && call.MaxTokens == 0 && call.MaxToolCalls == 0 {
		return inv, nil
	}
	local := *inv
	if local.ArchiveDir == "" {
		local.ArchiveDir = absPath(inv.Dir, ".")
	}
	if call.Dir != "" {
		local.Specs = make(map[string]config.RoleSpec, len(inv.Specs))
		for name, spec := range inv.Specs {
			spec.PromptPath = absPath(inv.Dir, spec.PromptPath)
			spec.ResultPath = absPath(inv.Dir, spec.ResultPath)
			local.Specs[name] = spec
		}
		if local.ConventionsPath != "" {
			local.ConventionsPath = absPath(inv.Dir, local.ConventionsPath)
		}
		if local.SkillsDir != "" {
			local.SkillsDir = absPath(inv.Dir, local.SkillsDir)
		} else {
			local.SkillsDir = absPath(inv.Dir, "skills")
		}
		if local.MemPath != "" {
			local.MemPath = absPath(inv.Dir, local.MemPath)
		}
		local.Dir = call.Dir
	}
	if call.FreshContext || call.ReadOnly {
		local.Sessions = nil
		local.ResumeRoles = nil
	}
	local.ReadOnly = call.ReadOnly
	local.SingleAttempt = call.SingleAttempt
	local.DisallowNativeDelegation = inv.DisallowNativeDelegation || call.DisableNativeDelegation
	if call.MaxTokens < 0 || call.MaxToolCalls < 0 {
		return nil, fmt.Errorf("invocation budgets cannot be negative")
	}
	if call.MaxTokens > 0 {
		local.MaxTokens = call.MaxTokens
	}
	if call.MaxToolCalls > 0 {
		local.MaxToolCalls = call.MaxToolCalls
	}
	return &local, nil
}

func acknowledgedInvoker(inv *RoleInvoker, role, task string) (*RoleInvoker, error) {
	epoch, err := supervision.ContextEpoch(inv.TelemetryRoot, task, role)
	if err != nil {
		return nil, err
	}
	if epoch == "" {
		return inv, nil
	}
	local := *inv
	local.SessionNamespace = inv.SessionNamespace + "/audit-" + epoch
	return &local, nil
}

func (inv *RoleInvoker) addSupervisorContext(role, task string, vars map[string]string) error {
	decision, found, err := supervision.LatestDecision(inv.TelemetryRoot, task, role)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	raw, err := json.Marshal(decision)
	if err != nil {
		return err
	}
	vars["SUPERVISOR_DECISION"] = string(raw)
	return nil
}

func appendSupervisorContext(prompt string, vars map[string]string) string {
	if decision := vars["SUPERVISOR_DECISION"]; decision != "" {
		return prompt + "\n\n<supervisor_decision>\n" + decision + "\nApply this recorded decision before continuing. Replan in a bounded scope; delegation requires runtime-authorized workers.\n</supervisor_decision>\n"
	}
	return prompt
}

func (inv *RoleInvoker) validateCapabilities(ag config.AgentSpec) error {
	if inv.ReadOnly && !providers.CapabilitiesFor(ag.Provider).ReadOnlySandbox {
		return fmt.Errorf("provider %q cannot enforce read-only review sandbox", ag.Provider)
	}
	if inv.ReadOnly {
		if len(ag.ExtraArgs) > 0 {
			return fmt.Errorf("read-only role cannot accept unverified extra_args")
		}
	}
	if inv.RequireNativeModelControl {
		return providers.RequireNativeModelControl(ag.Provider)
	}
	return nil
}

func writeReadOnlyResult(path string, r *runner.Result) error {
	raw := []byte(strings.TrimSpace(r.FinalText))
	if !json.Valid(raw) {
		return fmt.Errorf("read-only reviewer must return strict JSON in final response")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		return err
	}
	r.ResultExists = true
	return nil
}

func (inv *RoleInvoker) toolObserver(role string, rc RunContext) func(providers.Event) error {
	if !inv.Supervision.Enabled() && !inv.DisallowNativeDelegation && inv.MaxTokens == 0 && inv.MaxToolCalls == 0 {
		return nil
	}
	var toolCalls, partialTokens, terminalTokens int
	seenTerminal := false
	return func(event providers.Event) error {
		if event.Type == providers.EventPartialUsage {
			partialTokens += usageVolume(event.Usage)
		}
		if event.Type == providers.EventUsage {
			terminalTokens += usageVolume(event.Usage)
			seenTerminal = true
		}
		tokens := partialTokens
		if seenTerminal {
			tokens = terminalTokens
		}
		if inv.MaxTokens > 0 && tokens >= inv.MaxTokens {
			return &InvocationBudgetError{Reason: "observed correction token ceiling reached"}
		}
		if event.Type != providers.EventToolCall {
			return nil
		}
		if inv.DisallowNativeDelegation && nativeDelegation(event.ToolName) {
			return &supervision.CheckpointError{Reason: "native delegation cannot guarantee an explicit child model through this adapter", FreshContext: true}
		}
		toolCalls++
		if err := inv.observeTools(role, &runner.Result{Events: []providers.Event{event}}, rc); err != nil {
			return err
		}
		if inv.MaxToolCalls > 0 && toolCalls >= inv.MaxToolCalls {
			return &InvocationBudgetError{Reason: "observed correction tool ceiling reached"}
		}
		return nil
	}
}

var ErrInvocationBudget = errors.New("invocation budget exhausted")

type InvocationBudgetError struct{ Reason string }

func (e *InvocationBudgetError) Error() string { return ErrInvocationBudget.Error() + ": " + e.Reason }
func (e *InvocationBudgetError) Unwrap() error { return ErrInvocationBudget }

func usageVolume(usage map[string]int) int {
	total := 0
	for _, key := range []string{"input_tokens", "cached_input_tokens", "cache_read_tokens", "cache_creation_input_tokens", "cache_write_tokens", "output_tokens"} {
		total += usage[key]
	}
	return total
}
func measuredUsage(result *runner.Result) bool {
	for _, event := range result.Events {
		if event.Type != providers.EventUsage && event.Type != providers.EventPartialUsage {
			continue
		}
		_, input := event.Usage["input_tokens"]
		_, output := event.Usage["output_tokens"]
		if input && output {
			return true
		}
	}
	return false
}

func nativeDelegation(tool string) bool {
	switch strings.ToLower(tool) {
	case "agent", "task", "spawn_agent", "native_agent", "collab_agent_tool_call", "workflow":
		return true
	}
	return false
}

// recordUsage prefers per-call partial events when present, otherwise keeps
// the provider's observable turn/invocation totals. It never adds both.
func (inv *RoleInvoker) recordUsage(role, agent string, ag config.AgentSpec, spec runner.Spec, r *runner.Result, rc RunContext, artifactDir string) error {
	if inv.TelemetryRoot == "" {
		return nil
	}
	kind := providers.EventUsage
	granularity := "provider_total"
	for _, e := range r.Events {
		if e.Type == providers.EventPartialUsage {
			kind = providers.EventPartialUsage
			granularity = "api_call"
			break
		}
	}
	if kind == providers.EventUsage && ag.Provider == "codex" {
		granularity = "provider_turn"
	}
	// A full invocation hash is conservative: we do not compare prefixes whose
	// equivalence cannot be established from the controlled CLI input.
	controlled := spec.Prompt
	if i := strings.Index(controlled, "\n\n<run_context>\n"); i >= 0 {
		controlled = controlled[:i]
	}
	prefix := fmt.Sprintf("%x", sha256.Sum256([]byte(controlled)))
	base := artifactDir
	if base == "" {
		base = fmt.Sprintf("%s/%s/%s/%d/%d/%d", rc.TaskID, role, agent, rc.Cycle, rc.Attempt, time.Now().UnixNano())
	}
	var calls []telemetry.Call
	for i, e := range r.Events {
		if e.Type != kind {
			continue
		}
		calls = append(calls, telemetry.NewCall(fmt.Sprintf("%s/%d", base, i), rc.TaskID, role, ag.Provider, ag.Model, prefix, granularity, e.Usage))
	}
	if len(calls) == 0 {
		calls = append(calls, telemetry.NewCall(base+"/unavailable", rc.TaskID, role, ag.Provider, ag.Model, prefix, "unavailable", nil))
	}
	return telemetry.Append(inv.TelemetryRoot, calls)
}

var supervisionMu sync.Mutex

func (inv *RoleInvoker) observeTools(role string, r *runner.Result, rc RunContext) error {
	if !inv.Supervision.Enabled() && inv.MaxToolCalls == 0 {
		return nil
	}
	supervisionMu.Lock()
	defer supervisionMu.Unlock()
	var counters supervision.Counters
	path := ""
	if inv.TelemetryRoot != "" {
		id := fmt.Sprintf("%x", sha256.Sum256([]byte(rc.TaskID+"\x00"+role)))
		path = filepath.Join(inv.TelemetryRoot, "supervision-"+id+".json")
		raw, err := os.ReadFile(path)
		if err == nil {
			if err = json.Unmarshal(raw, &counters); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	var checkpoint error
	for _, e := range r.Events {
		if e.Type != providers.EventToolCall {
			continue
		}
		observation := supervision.Observation{Tool: e.ToolName}
		if path != "" {
			if err := supervision.AppendObservation(inv.TelemetryRoot, supervision.Record{Task: rc.TaskID, Role: role, Time: time.Now().UTC(), Observation: observation}); err != nil {
				return err
			}
		}
		if err := inv.Supervision.Observe(&counters, observation); err != nil {
			checkpoint = err
		}
	}
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		raw, _ := json.Marshal(counters)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			return err
		}
	}
	if checkpoint != nil && inv.Log != nil {
		inv.Log.Log(eventlog.Event{Type: "supervision_checkpoint", Fields: map[string]any{"task_id": rc.TaskID, "role": role, "counters": counters, "reason": checkpoint.Error(), "coverage": "streamed_tool_events; exploratory/nonmechanical classification unavailable"}})
	}
	return checkpoint
}

func (inv *RoleInvoker) agentEnvironment() []string {
	if strings.TrimSpace(inv.MemoryProject) == "" {
		return inv.AgentEnv
	}
	env := inv.AgentEnv
	if env == nil {
		env = os.Environ()
	}
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, "ORQ_MEMORY_PROJECT=") {
			out = append(out, entry)
		}
	}
	return append(out, "ORQ_MEMORY_PROJECT="+inv.MemoryProject)
}
