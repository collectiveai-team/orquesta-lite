package builtin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/planning"
	"github.com/collectiveai-team/orquesta-lite/internal/review"
)

func stateForOutcome() planning.State {
	return planning.State{Status: "active", Revision: json.Number("1"), Summary: "Work", IterationBudget: json.Number("8"), NextTicket: &planning.Ticket{ID: "T1"}, Pending: []planning.Ticket{{ID: "T2"}}, Completed: []string{}, Blocked: []string{}}
}
func approvedPlanReview() review.Result {
	return review.Result{Version: 2, Status: "complete", Decision: "approve", Approved: true, Summary: "Implemented behavior reviewed", Findings: []review.Finding{}, Limitations: []string{}, ReviewedRevision: "candidate", Evidence: []string{"runtime"}}
}
func TestPlanOutcomeBatchRequiresImplementationAndQA(t *testing.T) {
	for _, test := range []struct {
		name      string
		complete  bool
		remaining []string
		qa        review.Result
		want      string
	}{
		{"verified", true, []string{}, approvedPlanReview(), "complete"},
		{"unfinished", false, []string{"T2"}, approvedPlanReview(), "incomplete"},
		{"contradictory_remaining", true, []string{"T2"}, approvedPlanReview(), "incomplete"},
		{"unavailable_qa", true, []string{}, review.Result{Version: 2, Status: "unavailable", Decision: "inconclusive", Approved: false, Summary: "No review", Findings: []review.Finding{}, Limitations: []string{"offline"}, Evidence: []string{}}, "incomplete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := map[string]any{"initial": stateForOutcome(), "iterations": nil, "fast": true, "batch": map[string]any{"implementation": map[string]any{"complete": test.complete, "remaining": test.remaining}, "qa": test.qa}}
			raw, _ := json.Marshal(input)
			result, err := (PlanOutcomeExecutor{}).Execute(context.Background(), activity.Request{Inputs: raw})
			if err != nil {
				t.Fatal(err)
			}
			var out planning.Outcome
			if err := json.Unmarshal(result.Output, &out); err != nil {
				t.Fatal(err)
			}
			if out.Status != test.want {
				t.Fatalf("%+v", out)
			}
		})
	}
}
func TestPlanReviewWithholdsApprovalForBlockedScope(t *testing.T) {
	state := stateForOutcome()
	state.NextTicket = nil
	state.Blocked = []string{"T2"}
	outcome, err := planning.Resolve(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, visual := range []*review.Visual{nil, {Requested: "not_applicable", Executed: "not_applicable", Reason: "No UI", Required: []string{}, Checked: []string{}, Pending: []string{}}} {
		source := approvedPlanReview()
		if visual != nil {
			source.Status = "not_applicable"
			source.Limitations = []string{"No UI"}
			source.Visual = visual
		}
		raw, _ := json.Marshal(map[string]any{"plan": outcome, "review": source})
		result, err := (PlanReviewExecutor{}).Execute(context.Background(), activity.Request{Inputs: raw})
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Approved     bool          `json:"approved"`
			Review       review.Result `json:"review"`
			SourceReview review.Result `json:"source_review"`
		}
		if err := json.Unmarshal(result.Output, &out); err != nil {
			t.Fatal(err)
		}
		if out.Approved || out.Review.Approved || out.Review.Decision != "inconclusive" || len(out.Review.Limitations) == 0 {
			t.Fatalf("false approval: %+v", out)
		}
		if err := out.Review.Validate(); err != nil {
			t.Fatal(err)
		}
		if out.SourceReview.Approved != source.Approved {
			t.Fatal("lost source review")
		}
	}
}

func TestFastOutcomeRetainsBlockedWorkAndDeduplicatesCurrentTicket(t *testing.T) {
	state := stateForOutcome()
	state.Pending = []planning.Ticket{*state.NextTicket, {ID: "T2"}, {ID: "T3", Dependencies: []string{"T2"}}}
	state.Blocked = []string{"T2"}
	raw, _ := json.Marshal(map[string]any{"initial": state, "fast": true, "batch": map[string]any{"implementation": map[string]any{"complete": true, "remaining": []string{}}, "qa": approvedPlanReview()}})
	result, err := (PlanOutcomeExecutor{}).Execute(context.Background(), activity.Request{Inputs: raw})
	if err != nil {
		t.Fatal(err)
	}
	var out planning.Outcome
	if err := json.Unmarshal(result.Output, &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "blocked" || out.Complete || len(out.Pending) != 2 || len(out.Blocked) != 2 || len(out.State.Completed) != 1 || out.State.Completed[0] != "T1" {
		t.Fatalf("false completion or duplicate ticket: %+v", out)
	}
}

func TestFastOutcomeUsesVerifiedRepairReview(t *testing.T) {
	initial := approvedPlanReview()
	initial.Decision = "warn"
	initial.Approved = false
	initial.Findings = []review.Finding{{ID: "F1", Category: "behavior", Severity: "low", State: "open", Location: "file:1", Trigger: "input", Impact: "minor harm", Evidence: []string{"probe"}, Origins: []string{"qa"}, Resolution: ""}}
	final := approvedPlanReview()
	final.Findings = append([]review.Finding{}, initial.Findings...)
	final.Findings[0].State = "resolved"
	final.Findings[0].Resolution = "Verified repaired behavior"
	raw, _ := json.Marshal(map[string]any{"initial": stateForOutcome(), "fast": true, "batch": map[string]any{"implementation": map[string]any{"complete": true, "remaining": []string{}}, "qa": initial, "final_qa": final}})
	result, err := (PlanOutcomeExecutor{}).Execute(context.Background(), activity.Request{Inputs: raw})
	if err != nil {
		t.Fatal(err)
	}
	var out planning.Outcome
	if err := json.Unmarshal(result.Output, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Complete || out.Status != "complete" {
		t.Fatalf("stale review overrode verified repair: %+v", out)
	}
}

func TestPlanReviewRejectsFalseCompleteOutcome(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"plan": map[string]any{"status": "complete", "complete": true, "pending": []planning.Ticket{{ID: "T2"}}, "blocked": []string{"T2"}}, "review": approvedPlanReview()})
	if _, err := (PlanReviewExecutor{}).Execute(context.Background(), activity.Request{Inputs: raw}); err == nil {
		t.Fatal("approved unfinished plan")
	}
}
