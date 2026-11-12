package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/planning"
	"github.com/collectiveai-team/orquesta-lite/internal/review"
)

type PlanOutcomeExecutor struct{}

func (PlanOutcomeExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "plan.outcome", Version: "1", Effect: activity.EffectPure}
}
func (PlanOutcomeExecutor) Execute(_ context.Context, request activity.Request) (activity.Result, error) {
	var input struct {
		Initial    planning.State `json:"initial"`
		Iterations []struct {
			State          *planning.State `json:"state"`
			Implementation json.RawMessage `json:"implementation,omitempty"`
			Verification   json.RawMessage `json:"verification,omitempty"`
			Commit         json.RawMessage `json:"commit,omitempty"`
		} `json:"iterations,omitempty"`
		Fast  bool `json:"fast,omitempty"`
		Batch *struct {
			Implementation struct {
				TicketID     string   `json:"ticket_id"`
				Complete     bool     `json:"complete"`
				Summary      string   `json:"summary"`
				FilesChanged []string `json:"files_changed"`
				Gates        []string `json:"gates"`
				Remaining    []string `json:"remaining"`
			} `json:"implementation"`
			QA      review.Result  `json:"qa"`
			FinalQA *review.Result `json:"final_qa,omitempty"`
		} `json:"batch,omitempty"`
	}
	if err := strictJSON(request.Inputs, &input); err != nil {
		return activity.Result{}, contractError("plan.outcome", err)
	}
	state := input.Initial
	if len(input.Iterations) > 0 && input.Iterations[len(input.Iterations)-1].State != nil {
		state = *input.Iterations[len(input.Iterations)-1].State
	}
	initialOutcome, err := planning.Resolve(state)
	if err != nil {
		return activity.Result{}, contractError("plan.outcome state", err)
	}
	if input.Fast && input.Batch != nil {
		qa := input.Batch.QA
		if input.Batch.FinalQA != nil {
			qa = *input.Batch.FinalQA
		}
		if err := qa.Validate(); err != nil {
			return activity.Result{}, contractError("plan.outcome batch review", err)
		}
		if input.Batch.Implementation.Complete && len(input.Batch.Implementation.Remaining) == 0 && qa.Approved {
			state.Completed = slices.Clone(state.Completed)
			remaining := []planning.Ticket{}
			for _, ticket := range state.Pending {
				if slices.Contains(initialOutcome.Blocked, ticket.ID) {
					remaining = append(remaining, ticket)
					continue
				}
				if !slices.Contains(state.Completed, ticket.ID) {
					state.Completed = append(state.Completed, ticket.ID)
				}
			}
			if state.NextTicket != nil && !slices.Contains(state.Completed, state.NextTicket.ID) {
				state.Completed = append(state.Completed, state.NextTicket.ID)
			}
			state.Pending = remaining
			state.NextTicket = nil
			state.Status = "complete"
			if len(remaining) > 0 {
				state.Status = "blocked"
			}
		}
	}
	outcome, err := planning.Resolve(state)
	if err != nil {
		return activity.Result{}, contractError("plan.outcome", err)
	}
	raw, err := json.Marshal(outcome)
	return activity.Result{Output: raw}, err
}

type PlanReviewExecutor struct{}

func (PlanReviewExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "plan.review", Version: "1", Effect: activity.EffectPure}
}
func (PlanReviewExecutor) Execute(_ context.Context, request activity.Request) (activity.Result, error) {
	var input struct {
		Plan   json.RawMessage `json:"plan"`
		Review review.Result   `json:"review"`
	}
	if err := strictJSON(request.Inputs, &input); err != nil {
		return activity.Result{}, contractError("plan.review", err)
	}
	if err := input.Review.Validate(); err != nil {
		return activity.Result{}, contractError("plan.review", err)
	}
	outcome := planning.Outcome{Complete: true, Status: "complete", Blocked: []string{}, Pending: []planning.Ticket{}}
	if len(input.Plan) > 0 && string(input.Plan) != "null" && string(input.Plan) != "{}" {
		if err := strictJSON(input.Plan, &outcome); err != nil {
			return activity.Result{}, contractError("plan.review", err)
		}
		if outcome.Status != "complete" && outcome.Status != "blocked" && outcome.Status != "incomplete" {
			return activity.Result{}, contractError("plan.review", fmt.Errorf("invalid plan outcome"))
		}
	}
	if outcome.Complete != (outcome.Status == "complete") || outcome.Complete && (len(outcome.Pending) > 0 || len(outcome.Blocked) > 0) || outcome.Status == "blocked" && (len(outcome.Blocked) == 0 || len(outcome.Pending) == 0) {
		return activity.Result{}, contractError("plan.review", fmt.Errorf("inconsistent plan outcome"))
	}
	if outcome.State.Status != "" {
		actual, err := planning.Resolve(outcome.State)
		if err != nil || actual.Status != outcome.Status || actual.Complete != outcome.Complete || !slices.Equal(actual.Blocked, outcome.Blocked) {
			return activity.Result{}, contractError("plan.review", fmt.Errorf("outcome differs from its durable state"))
		}
	}
	adjusted := input.Review
	if !outcome.Complete {
		adjusted.Approved = false
		if adjusted.Status == "complete" || adjusted.Status == "not_applicable" {
			adjusted.Status = "partial"
		}
		if adjusted.Decision != "block" {
			adjusted.Decision = "inconclusive"
		}
		reason := "Unfinished plan work"
		if len(outcome.Blocked) > 0 {
			reason = "Externally blocked tickets remain pending: " + strings.Join(outcome.Blocked, ", ")
		}
		adjusted.Summary += "; " + reason
		adjusted.Limitations = append(append([]string{}, adjusted.Limitations...), reason)
		// Original visual scope remains in source_review; global incompleteness
		// must not turn a valid no-UI visual report into a contradictory contract.
		if adjusted.Visual != nil && adjusted.Visual.Executed == "not_applicable" {
			adjusted.Visual = nil
		}
	}
	if err := adjusted.Validate(); err != nil {
		return activity.Result{}, contractError("plan.review adjusted", err)
	}
	raw, _ := json.Marshal(map[string]any{"approved": adjusted.Approved && outcome.Complete, "review": adjusted, "source_review": input.Review, "blocked": outcome.Blocked, "complete": outcome.Complete})
	return activity.Result{Output: raw}, nil
}
