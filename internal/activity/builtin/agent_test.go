package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/activity"
	"github.com/lionelchamorro/orquestalite/internal/config"
	"github.com/lionelchamorro/orquestalite/internal/invoke"
)

func reviewValidator(_ string, raw []byte) error {
	var value struct {
		Approved *bool `json:"approved"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if value.Approved == nil {
		return fmt.Errorf("approved is required")
	}
	return nil
}

func TestAgentInvokeReturnsValidatedFallbackOutput(t *testing.T) {
	executor := &AgentExecutor{
		Invoker:  &invoke.RoleInvoker{Specs: map[string]config.RoleSpec{}},
		Validate: reviewValidator,
	}
	inputs := []byte(`{
		"role":"critic",
		"outputSchema":"schema:review-result@1",
		"fallbackOutput":{"approved":false,"summary":"critic unavailable","findings":["use QA findings"]}
	}`)
	result, err := executor.Execute(context.Background(), activity.Request{Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output["approved"] != false || output["summary"] != "critic unavailable" {
		t.Fatalf("fallback output=%s", result.Output)
	}
}

func TestAgentInvokeRejectsInvalidFallbackOutput(t *testing.T) {
	executor := &AgentExecutor{
		Invoker:  &invoke.RoleInvoker{Specs: map[string]config.RoleSpec{}},
		Validate: reviewValidator,
	}
	inputs := []byte(`{
		"role":"critic",
		"outputSchema":"schema:review-result@1",
		"fallbackOutput":{"summary":"missing approved"}
	}`)
	_, err := executor.Execute(context.Background(), activity.Request{Inputs: inputs})
	if activity.Classify(err) != activity.ErrorInvalidContract {
		t.Fatalf("err=%v", err)
	}
}
