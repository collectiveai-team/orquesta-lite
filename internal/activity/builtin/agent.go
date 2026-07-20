package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lionelchamorro/orquestalite/internal/activity"
	"github.com/lionelchamorro/orquestalite/internal/invoke"
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
	raw, err := invoke.Raw(ctx, a.Invoker, input.Role, invoke.RoleCall{Vars: vars, Skills: input.Skills}, invoke.RunContext{TaskID: request.StepID, Attempt: request.Attempt}, func(raw []byte) error { return a.Validate(input.OutputSchema, raw) })
	if err != nil {
		if len(input.FallbackOutput) > 0 {
			if validationErr := a.Validate(input.OutputSchema, input.FallbackOutput); validationErr != nil {
				return activity.Result{}, contractError("agent.invoke fallbackOutput", validationErr)
			}
			return activity.Result{Output: input.FallbackOutput}, nil
		}
		class := activity.ErrorPermanent
		switch {
		case errors.Is(err, invoke.ErrInvalidContract):
			class = activity.ErrorInvalidContract
		case errors.Is(err, invoke.ErrAgentTimeout):
			class = activity.ErrorTimeout
		}
		return activity.Result{}, &activity.Error{Class: class, Op: "agent.invoke", Err: err}
	}
	return activity.Result{Output: json.RawMessage(raw)}, nil
}
