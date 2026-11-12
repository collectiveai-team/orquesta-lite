// Package planning resolves ticket-loop outcomes without treating blocked work
// as completed product scope.
package planning

import (
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"slices"
	"strings"
)

type Ticket struct {
	ID                 string   `json:"id"`
	Title              string   `json:"title"`
	Objective          string   `json:"objective"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	Dependencies       []string `json:"dependencies"`
	FilesHint          []string `json:"files_hint"`
}

type State struct {
	Status          string            `json:"status"`
	Revision        json.Number       `json:"revision"`
	Summary         string            `json:"summary"`
	IterationBudget json.Number       `json:"iteration_budget"`
	NextTicket      *Ticket           `json:"next_ticket"`
	Pending         []Ticket          `json:"pending"`
	Completed       []string          `json:"completed"`
	Blocked         []string          `json:"blocked"`
	Risks           []string          `json:"risks"`
	History         []json.RawMessage `json:"history"`
}

type Outcome struct {
	Status   string   `json:"status"`
	Complete bool     `json:"complete"`
	Blocked  []string `json:"blocked"`
	Pending  []Ticket `json:"pending"`
	Summary  string   `json:"summary"`
	State    State    `json:"state"`
}

// Resolve checks the dependency graph even when the planner claims blocked:
// cycles, dangling edges and an unexplained empty frontier are errors.
func Resolve(state State) (Outcome, error) {
	bound, ok := new(big.Rat).SetString(string(state.IterationBudget))
	if !ok || !bound.IsInt() || bound.Sign() <= 0 || bound.Cmp(big.NewRat(200, 1)) > 0 {
		return Outcome{}, fmt.Errorf("plan iteration_budget must be a whole number in 1..200")
	}
	if !slices.Contains([]string{"active", "complete", "blocked"}, state.Status) {
		return Outcome{}, fmt.Errorf("invalid plan status %q", state.Status)
	}
	if state.Pending == nil || state.Completed == nil || state.Blocked == nil {
		return Outcome{}, fmt.Errorf("plan requires explicit pending/completed/blocked arrays")
	}
	tickets := map[string]Ticket{}
	completed := map[string]bool{}
	direct := map[string]bool{}
	for _, id := range state.Completed {
		if id == "" || completed[id] {
			return Outcome{}, fmt.Errorf("invalid/duplicate completed ticket %q", id)
		}
		completed[id] = true
	}
	all := slices.Clone(state.Pending)
	for _, ticket := range all {
		if strings.TrimSpace(ticket.ID) == "" || completed[ticket.ID] {
			return Outcome{}, fmt.Errorf("unfinished ticket %q is empty or completed", ticket.ID)
		}
		if _, ok := tickets[ticket.ID]; ok {
			return Outcome{}, fmt.Errorf("duplicate unfinished ticket %s", ticket.ID)
		}
		tickets[ticket.ID] = ticket
	}
	if state.NextTicket != nil {
		next := *state.NextTicket
		if next.ID == "" || completed[next.ID] {
			return Outcome{}, fmt.Errorf("invalid next ticket %s", next.ID)
		}
		if existing, ok := tickets[next.ID]; ok {
			if !reflect.DeepEqual(existing, next) {
				return Outcome{}, fmt.Errorf("next ticket %s conflicts with its pending definition", next.ID)
			}
		} else {
			tickets[next.ID] = next
			all = append(all, next)
		}
	}
	for _, id := range state.Blocked {
		if _, ok := tickets[id]; !ok {
			return Outcome{}, fmt.Errorf("blocked ticket %s is not unfinished", id)
		}
		if direct[id] {
			return Outcome{}, fmt.Errorf("duplicate blocked ticket %s", id)
		}
		direct[id] = true
	}
	for _, ticket := range all {
		for _, id := range ticket.Dependencies {
			if !completed[id] {
				if _, ok := tickets[id]; !ok {
					return Outcome{}, fmt.Errorf("ticket %s has missing dependency %s", ticket.ID, id)
				}
			}
		}
	}
	visiting := map[string]bool{}
	resolved := map[string]bool{}
	blocked := map[string]bool{}
	var visit func(string) (bool, error)
	visit = func(id string) (bool, error) {
		if completed[id] {
			return false, nil
		}
		if visiting[id] {
			return false, fmt.Errorf("ticket dependency cycle includes %s", id)
		}
		if resolved[id] {
			return blocked[id], nil
		}
		visiting[id] = true
		blocked[id] = direct[id]
		for _, dependency := range tickets[id].Dependencies {
			value, err := visit(dependency)
			if err != nil {
				return false, err
			}
			blocked[id] = blocked[id] || value
		}
		visiting[id] = false
		resolved[id] = true
		return blocked[id], nil
	}
	blockedIDs := []string{}
	for _, ticket := range all {
		value, err := visit(ticket.ID)
		if err != nil {
			return Outcome{}, err
		}
		if value {
			blockedIDs = append(blockedIDs, ticket.ID)
		}
	}
	slices.Sort(blockedIDs)
	out := Outcome{Status: state.Status, Complete: false, Blocked: blockedIDs, Pending: slices.Clone(state.Pending), Summary: state.Summary, State: state}
	if state.Status == "complete" {
		if state.NextTicket != nil || len(state.Pending) > 0 || len(state.Blocked) > 0 {
			return Outcome{}, fmt.Errorf("complete plan still has unfinished work")
		}
		out.Complete = true
		return out, nil
	}
	if state.NextTicket != nil {
		if state.Status == "blocked" || blocked[state.NextTicket.ID] {
			return Outcome{}, fmt.Errorf("plan selected a blocked next ticket")
		}
		for _, id := range state.NextTicket.Dependencies {
			if !completed[id] {
				return Outcome{}, fmt.Errorf("next ticket %s depends on unfinished ticket %s", state.NextTicket.ID, id)
			}
		}
		out.Status = "incomplete"
		return out, nil
	}
	if len(state.Pending) == 0 {
		return Outcome{}, fmt.Errorf("non-complete plan has no next ticket or pending work")
	}
	if len(blockedIDs) != len(state.Pending) {
		return Outcome{}, fmt.Errorf("empty ticket frontier has runnable or unexplained work; repair the plan")
	}
	out.Status = "blocked"
	out.State.Status = "blocked"
	out.Summary = state.Summary + "; pending blocked work: " + strings.Join(blockedIDs, ", ")
	return out, nil
}
