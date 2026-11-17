package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/planning"
)

func runTicketActivity(t *testing.T, executor activity.Executor, input any, output any) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), activity.Request{Inputs: raw})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(result.Output, output); err != nil {
		t.Fatal(err)
	}
}

func progressState(t *testing.T, count int) planning.State {
	t.Helper()
	state := planning.State{
		Status: "active", Revision: "1", Summary: "implement tickets", IterationBudget: "100",
		Pending: []planning.Ticket{}, Completed: []string{}, Blocked: []string{},
		Risks: []string{}, History: []json.RawMessage{},
	}
	for index := 1; index <= count; index++ {
		ticket := planning.Ticket{ID: fmt.Sprintf("T%02d", index), Title: "ticket", Objective: "behavior", AcceptanceCriteria: []string{"works"}, Dependencies: []string{}, FilesHint: []string{}}
		if index > 1 {
			ticket.Dependencies = []string{fmt.Sprintf("T%02d", index-1)}
		}
		if index == 1 {
			state.NextTicket = &ticket
		} else {
			state.Pending = append(state.Pending, ticket)
		}
	}
	return state
}

func TestTicketCheckpointRequiresPassingChecksAndCommit(t *testing.T) {
	state := progressState(t, 1)
	implementation := ticketImplementation{TicketID: "T01", Complete: true, Summary: "implemented T01", FilesChanged: []string{"feature.go"}, Checks: []ticketCheck{{Argv: []string{"go", "test", "./feature"}, ExitCode: 0}}, Remaining: []string{}}
	var preflight ticketVerdict
	runTicketActivity(t, TicketPreflightExecutor{}, map[string]any{"state": state, "implementation": implementation}, &preflight)
	if !preflight.Approved {
		t.Fatalf("passing focused check must be ready for commit: %+v", preflight)
	}
	var checkpoint ticketVerdict
	runTicketActivity(t, TicketCheckpointExecutor{}, map[string]any{"preflight": preflight, "commit": map[string]any{"committed": false, "reason": "nothing to commit"}}, &checkpoint)
	if checkpoint.Approved {
		t.Fatal("a test report without a commit must not complete a ticket")
	}
	runTicketActivity(t, TicketCheckpointExecutor{}, map[string]any{"preflight": preflight, "commit": map[string]any{"committed": false, "blocked": true}, "retry": map[string]any{"committed": true}}, &checkpoint)
	if !checkpoint.Approved {
		t.Fatalf("successful commit retry must close the checkpoint: %+v", checkpoint)
	}
	implementation.Checks[0].ExitCode = 1
	runTicketActivity(t, TicketPreflightExecutor{}, map[string]any{"state": state, "implementation": implementation}, &preflight)
	if preflight.Approved || len(preflight.Findings) == 0 {
		t.Fatalf("failing focused check must block commit: %+v", preflight)
	}
}

func TestFastAdvanceDrainsThirtyFiveTicketsWithoutOneBatch(t *testing.T) {
	state := progressState(t, 35)
	for index := 1; index <= 35; index++ {
		if state.NextTicket == nil || state.NextTicket.ID != fmt.Sprintf("T%02d", index) {
			t.Fatalf("pass %d selected %+v", index, state.NextTicket)
		}
		verification := ticketVerdict{TicketID: state.NextTicket.ID, Approved: true, Summary: "committed", Findings: []string{}, Gates: []string{"focused test: exit 0"}}
		runTicketActivity(t, PlanAdvanceExecutor{}, map[string]any{"state": state, "verification": verification, "fast": true, "planned_state": nil}, &state)
	}
	if state.Status != "complete" || len(state.Completed) != 35 || state.NextTicket != nil || len(state.Pending) != 0 {
		t.Fatalf("35 checkpoint loop did not complete: %+v", state)
	}
}

func TestFastAdvanceRetriesIncompleteTicketAndPreservesBlockedWork(t *testing.T) {
	state := progressState(t, 3)
	state.Blocked = []string{"T03"}
	verification := ticketVerdict{TicketID: "T01", Approved: false, Summary: "incomplete", Findings: []string{"test failed"}, Gates: []string{}}
	runTicketActivity(t, PlanAdvanceExecutor{}, map[string]any{"state": state, "verification": verification, "fast": true}, &state)
	if state.NextTicket == nil || state.NextTicket.ID != "T01" || len(state.Completed) != 0 || len(state.History) != 1 {
		t.Fatalf("incomplete ticket lost its retry scope: %+v", state)
	}
	for _, id := range []string{"T01", "T02"} {
		runTicketActivity(t, PlanAdvanceExecutor{}, map[string]any{"state": state, "verification": ticketVerdict{TicketID: id, Approved: true, Summary: "committed"}, "fast": true}, &state)
	}
	if state.Status != "blocked" || len(state.Completed) != 2 || len(state.Pending) != 1 || state.Pending[0].ID != "T03" {
		t.Fatalf("blocked ticket was marked complete or dropped: %+v", state)
	}
}

func TestNormalAdvanceCannotAcceptPlannerCompletionWithoutCheckpoint(t *testing.T) {
	state := progressState(t, 1)
	planned := state
	planned.Status = "complete"
	planned.NextTicket = nil
	planned.Completed = []string{"T01"}
	raw, _ := json.Marshal(map[string]any{"state": state, "verification": ticketVerdict{TicketID: "T01", Approved: false, Summary: "not committed"}, "fast": false, "planned_state": planned})
	if _, err := (PlanAdvanceExecutor{}).Execute(context.Background(), activity.Request{Inputs: raw}); err == nil {
		t.Fatal("planner completion without a committed checkpoint must fail")
	}
}

func TestNormalAdvanceKeepsAnUncommittedTicketCurrent(t *testing.T) {
	state := progressState(t, 2)
	state.Pending[0].Dependencies = []string{}
	planned := state
	other := state.Pending[0]
	planned.NextTicket = &other
	planned.Pending = []planning.Ticket{*state.NextTicket}
	raw, _ := json.Marshal(map[string]any{"state": state, "verification": ticketVerdict{TicketID: "T01", Approved: false, Summary: "focused test failed", Findings: []string{"test failed"}}, "fast": false, "planned_state": planned})
	if _, err := (PlanAdvanceExecutor{}).Execute(context.Background(), activity.Request{Inputs: raw}); err == nil {
		t.Fatal("planner moved to another ticket while partial work was uncommitted")
	}
}
