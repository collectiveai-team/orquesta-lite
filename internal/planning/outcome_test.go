package planning

import (
	"encoding/json"
	"slices"
	"testing"
)

func planState(status string) State {
	return State{Status: status, Revision: json.Number("1"), IterationBudget: json.Number("8"), Summary: "Pending credentials", Pending: []Ticket{}, Completed: []string{"T1"}, Blocked: []string{}}
}
func TestBlockedOutcomePreservesPendingAndTransitiveBlockers(t *testing.T) {
	for _, status := range []string{"active", "blocked"} {
		t.Run(status, func(t *testing.T) {
			state := planState(status)
			state.Pending = []Ticket{{ID: "T2", Dependencies: []string{"T1"}}, {ID: "T3", Dependencies: []string{"T2"}}}
			state.Blocked = []string{"T2"}
			out, err := Resolve(state)
			if err != nil {
				t.Fatal(err)
			}
			if out.Status != "blocked" || out.Complete || len(out.Pending) != 2 || !slices.Equal(out.Blocked, []string{"T2", "T3"}) || !slices.Equal(out.State.Completed, []string{"T1"}) {
				t.Fatalf("lost blocked work: %+v", out)
			}
			if state.Status != status {
				t.Fatal("mutated caller state")
			}
		})
	}
}
func TestBlockedOutcomeDoesNotHideInvalidDependencyGraph(t *testing.T) {
	for _, test := range []struct {
		name    string
		pending []Ticket
		blocked []string
	}{
		{"runnable", []Ticket{{ID: "T2"}}, []string{}},
		{"dangling", []Ticket{{ID: "T2", Dependencies: []string{"missing"}}}, []string{"T2"}},
		{"cycle", []Ticket{{ID: "T2", Dependencies: []string{"T3"}}, {ID: "T3", Dependencies: []string{"T2"}}}, []string{"T2"}},
		{"stale_blocker", []Ticket{{ID: "T2"}}, []string{"T1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := planState("blocked")
			state.Pending = test.pending
			state.Blocked = test.blocked
			if _, err := Resolve(state); err == nil {
				t.Fatal("invalid blocked plan accepted")
			}
		})
	}
}
func TestRunnableBudgetExhaustionIsIncomplete(t *testing.T) {
	state := planState("active")
	state.NextTicket = &Ticket{ID: "T2"}
	out, err := Resolve(state)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "incomplete" || out.Complete {
		t.Fatal("unfinished runnable ticket reported complete")
	}
}
func TestCompleteCannotCarryPendingWork(t *testing.T) {
	state := planState("complete")
	if out, err := Resolve(state); err != nil || !out.Complete {
		t.Fatalf("complete rejected: %+v %v", out, err)
	}
	state.Pending = []Ticket{{ID: "T2"}}
	if _, err := Resolve(state); err == nil {
		t.Fatal("false completion accepted")
	}
}

func TestOutcomeAcceptsSchemaWholeNumberBudgets(t *testing.T) {
	for _, budget := range []string{"8", "8.0", "2.0e1"} {
		state := planState("complete")
		state.IterationBudget = json.Number(budget)
		raw, _ := json.Marshal(state)
		var decoded State
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if _, err := Resolve(decoded); err != nil {
			t.Fatalf("schema-valid budget %s rejected: %v", budget, err)
		}
	}
	for _, budget := range []string{"0", "8.5", "201", "2.1e-1"} {
		state := planState("complete")
		state.IterationBudget = json.Number(budget)
		if _, err := Resolve(state); err == nil {
			t.Fatalf("invalid budget %s accepted", budget)
		}
	}
}
