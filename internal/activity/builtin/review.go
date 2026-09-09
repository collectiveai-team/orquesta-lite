package builtin

import (
	"context"
	"encoding/json"
	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/review"
)

// ReviewAggregateExecutor separates confirmed repair work from review coverage.
type ReviewAggregateExecutor struct{}

func (ReviewAggregateExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "review.aggregate", Version: "1", Effect: activity.EffectPure}
}
func (ReviewAggregateExecutor) Execute(_ context.Context, request activity.Request) (activity.Result, error) {
	var input struct {
		Reviews []review.Result `json:"reviews"`
	}
	if err := strictJSON(request.Inputs, &input); err != nil {
		return activity.Result{}, contractError("review.aggregate", err)
	}
	findings, ready, err := review.Aggregate(input.Reviews)
	if err != nil {
		return activity.Result{}, contractError("review.aggregate", err)
	}
	repairs := []review.Finding{}
	for _, f := range findings {
		if f.State == "open" && f.Severity != "low" {
			repairs = append(repairs, f)
		}
	}
	raw, _ := json.Marshal(map[string]any{"ready": ready, "findings": findings, "repair_findings": repairs, "needs_repair": len(repairs) > 0})
	return activity.Result{Output: raw}, nil
}
