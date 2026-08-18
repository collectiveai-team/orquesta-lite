package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/invoke"
)

type SchemaValidator func(ref string, raw []byte) error

type AgentExecutor struct {
	Invoker  *invoke.RoleInvoker
	Validate SchemaValidator
}

func (a *AgentExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "agent.invoke", Version: "1", Effect: activity.EffectAtMostOnce}
}

type agentInput struct {
	Role           string            `json:"role"`
	Vars           map[string]string `json:"vars,omitempty"`
	Context        map[string]any    `json:"context,omitempty"`
	OutputSchema   string            `json:"outputSchema"`
	Skills         []string          `json:"skills,omitempty"`
	FallbackOutput json.RawMessage   `json:"fallbackOutput,omitempty"`
}

func (a *AgentExecutor) Execute(ctx context.Context, request activity.Request) (activity.Result, error) {
	var input agentInput
	if err := strictJSON(request.Inputs, &input); err != nil {
		return activity.Result{}, contractError("agent.invoke input", err)
	}
	if input.Role == "" || input.OutputSchema == "" {
		return activity.Result{}, contractError("agent.invoke input", fmt.Errorf("role and outputSchema are required"))
	}
	if a.Invoker == nil || a.Validate == nil {
		return activity.Result{}, &activity.Error{Class: activity.ErrorPermanent, Op: "agent.invoke", Err: fmt.Errorf("executor is not configured")}
	}
	vars := make(map[string]string, len(input.Vars)+len(input.Context))
	for key, value := range input.Vars {
		vars[key] = value
	}
	for key, value := range input.Context {
		if text, ok := value.(string); ok {
			vars[key] = text
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return activity.Result{}, contractError("agent.invoke context", err)
		}
		vars[key] = string(raw)
	}
	// A step nested inside a while-loop's subflow (e.g. one ticket per
	// iteration in factory-governed@N flows) keeps a constant StepID and an
	// empty ForeachKey of its own — the iteration's identity lives only in
	// ScopePath, which the scheduler extends once per subflow instantiation
	// (scheduler.go: child.scope = s.scope + "/" + step.ID + keySuffix(foreachKey))
	// and which then stays fixed across every step inside that instance.
	// Folding ScopePath into the session task ID gives each iteration its own
	// session-resume scope, so ticket 2 doesn't resume ticket 1's session
	// (and inherit its entire accumulated conversation) while retries WITHIN
	// the same iteration (same ScopePath, different Attempt) still resume
	// correctly.
	taskID := request.StepID
	if request.ScopePath != "" {
		taskID = request.ScopePath + "/" + request.StepID
	}
	// The spend is attached to the Result on *every* return path below. The
	// runtime sums attempts.cost_usd into RunUsage.CostUSD, which is what a
	// policy's maxCostUSD is checked against — so an invocation that failed,
	// or that fell back to a canned output, still has to report what its agent
	// chain burned. Dropping it on the failure paths would leave the only
	// spend-shaped brake blind to precisely the runs that need braking.
	outcome, err := invoke.Raw(ctx, a.Invoker, input.Role, invoke.RoleCall{Vars: vars, Skills: input.Skills}, invoke.RunContext{TaskID: taskID, Attempt: request.Attempt}, func(raw []byte) error { return a.Validate(input.OutputSchema, raw) })
	if err != nil {
		if len(input.FallbackOutput) > 0 {
			if validationErr := a.Validate(input.OutputSchema, input.FallbackOutput); validationErr != nil {
				return activity.Result{CostUSD: outcome.CostUSD}, contractError("agent.invoke fallbackOutput", validationErr)
			}
			return activity.Result{Output: input.FallbackOutput, CostUSD: outcome.CostUSD}, nil
		}
		class := activity.ErrorPermanent
		switch {
		case errors.Is(err, invoke.ErrInvalidContract):
			class = activity.ErrorInvalidContract
		case errors.Is(err, invoke.ErrAgentTimeout):
			class = activity.ErrorTimeout
		}
		return activity.Result{CostUSD: outcome.CostUSD}, &activity.Error{Class: class, Op: "agent.invoke", Err: err}
	}
	return activity.Result{Output: json.RawMessage(outcome.Output), CostUSD: outcome.CostUSD}, nil
}
