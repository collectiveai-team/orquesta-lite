package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/planning"
)

// TicketPreflightExecutor checks the coder's structured report before a commit
// is attempted. The report is still an agent claim; the commit hook and final
// integrated gates provide independent execution evidence.
type TicketPreflightExecutor struct{}

func (TicketPreflightExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "ticket.preflight", Version: "1", Effect: activity.EffectPure}
}

type ticketCheck struct {
	Argv     []string `json:"argv"`
	ExitCode int      `json:"exit_code"`
}

type ticketImplementation struct {
	TicketID     string        `json:"ticket_id"`
	Complete     bool          `json:"complete"`
	Summary      string        `json:"summary"`
	FilesChanged []string      `json:"files_changed"`
	Checks       []ticketCheck `json:"checks"`
	Remaining    []string      `json:"remaining"`
}

type ticketVerdict struct {
	TicketID string   `json:"ticket_id"`
	Approved bool     `json:"approved"`
	Summary  string   `json:"summary"`
	Findings []string `json:"findings"`
	Gates    []string `json:"gates"`
}

func (TicketPreflightExecutor) Execute(_ context.Context, request activity.Request) (activity.Result, error) {
	var input struct {
		State          planning.State       `json:"state"`
		Implementation ticketImplementation `json:"implementation"`
	}
	if err := strictJSON(request.Inputs, &input); err != nil {
		return activity.Result{}, contractError("ticket.preflight input", err)
	}
	if input.State.NextTicket == nil || input.State.Status != "active" {
		return activity.Result{}, contractError("ticket.preflight input", fmt.Errorf("no active ticket"))
	}
	if input.Implementation.TicketID != input.State.NextTicket.ID {
		return activity.Result{}, contractError("ticket.preflight input", fmt.Errorf("coder reported ticket %q, expected %q", input.Implementation.TicketID, input.State.NextTicket.ID))
	}
	verdict := ticketVerdict{TicketID: input.Implementation.TicketID, Summary: input.Implementation.Summary, Findings: []string{}, Gates: []string{}}
	if !input.Implementation.Complete {
		verdict.Findings = append(verdict.Findings, "coder reported incomplete work")
	}
	verdict.Findings = append(verdict.Findings, input.Implementation.Remaining...)
	if len(input.Implementation.Checks) == 0 {
		verdict.Findings = append(verdict.Findings, "no focused test result was reported")
	}
	for _, check := range input.Implementation.Checks {
		if len(check.Argv) == 0 || strings.TrimSpace(check.Argv[0]) == "" {
			verdict.Findings = append(verdict.Findings, "focused test result has no command")
			continue
		}
		verdict.Gates = append(verdict.Gates, fmt.Sprintf("%s: exit %d", strings.Join(check.Argv, " "), check.ExitCode))
		if check.ExitCode != 0 {
			verdict.Findings = append(verdict.Findings, fmt.Sprintf("focused test failed: %s", strings.Join(check.Argv, " ")))
		}
	}
	verdict.Approved = len(verdict.Findings) == 0
	raw, _ := json.Marshal(verdict)
	return activity.Result{Output: raw}, nil
}

// TicketCheckpointExecutor requires a successful Git commit, which runs the
// project's commit hooks, before a ticket can be marked completed.
type TicketCheckpointExecutor struct{}

func (TicketCheckpointExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "ticket.checkpoint", Version: "1", Effect: activity.EffectPure}
}

func (TicketCheckpointExecutor) Execute(_ context.Context, request activity.Request) (activity.Result, error) {
	var input struct {
		Preflight ticketVerdict `json:"preflight"`
		Commit    struct {
			Committed bool   `json:"committed"`
			Blocked   bool   `json:"blocked"`
			Retried   bool   `json:"retried"`
			Reason    string `json:"reason"`
			Message   string `json:"message"`
			ExitCode  int    `json:"exit_code"`
			Stdout    string `json:"stdout"`
			Stderr    string `json:"stderr"`
		} `json:"commit"`
		Retry *struct {
			Committed bool   `json:"committed"`
			Blocked   bool   `json:"blocked"`
			Retried   bool   `json:"retried"`
			Reason    string `json:"reason"`
			Message   string `json:"message"`
			ExitCode  int    `json:"exit_code"`
			Stdout    string `json:"stdout"`
			Stderr    string `json:"stderr"`
		} `json:"retry"`
	}
	if err := strictJSON(request.Inputs, &input); err != nil {
		return activity.Result{}, contractError("ticket.checkpoint input", err)
	}
	verdict := input.Preflight
	verdict.Findings = slices.Clone(verdict.Findings)
	if verdict.Findings == nil {
		verdict.Findings = []string{}
	}
	if verdict.Gates == nil {
		verdict.Gates = []string{}
	}
	committed := input.Commit.Committed || input.Retry != nil && input.Retry.Committed
	if verdict.Approved && !committed {
		verdict.Approved = false
		finding := input.Commit.Reason
		if input.Retry != nil && input.Retry.Reason != "" {
			finding = input.Retry.Reason
		}
		if finding == "" {
			finding = "no commit was created"
		}
		verdict.Findings = append(verdict.Findings, finding)
	}
	raw, _ := json.Marshal(verdict)
	return activity.Result{Output: raw}, nil
}

// PlanAdvanceExecutor uses the planner's state in normal mode. Fast mode picks
// the next dependency-ready ticket mechanically, with one durable checkpoint
// and one agent timeout per ticket.
type PlanAdvanceExecutor struct{}

func (PlanAdvanceExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "plan.advance", Version: "1", Effect: activity.EffectPure}
}

func (PlanAdvanceExecutor) Execute(_ context.Context, request activity.Request) (activity.Result, error) {
	var input struct {
		State        planning.State  `json:"state"`
		Verification ticketVerdict   `json:"verification"`
		Fast         bool            `json:"fast"`
		PlannedState *planning.State `json:"planned_state"`
	}
	if err := strictJSON(request.Inputs, &input); err != nil {
		return activity.Result{}, contractError("plan.advance input", err)
	}
	if _, err := planning.Resolve(input.State); err != nil {
		return activity.Result{}, contractError("plan.advance state", err)
	}
	if input.State.Status != "active" || input.State.NextTicket == nil {
		return activity.Result{}, contractError("plan.advance state", fmt.Errorf("no active ticket"))
	}
	current := input.State.NextTicket.ID
	if input.Verification.TicketID != current {
		return activity.Result{}, contractError("plan.advance verification", fmt.Errorf("checkpoint is for %q, expected %q", input.Verification.TicketID, current))
	}
	if !input.Fast {
		if input.PlannedState == nil {
			return activity.Result{}, contractError("plan.advance input", fmt.Errorf("normal mode requires planner output"))
		}
		if _, err := planning.Resolve(*input.PlannedState); err != nil {
			return activity.Result{}, contractError("plan.advance planned_state", err)
		}
		completed := slices.Contains(input.PlannedState.Completed, current)
		if completed != input.Verification.Approved {
			return activity.Result{}, contractError("plan.advance planned_state", fmt.Errorf("planner completion for %s disagrees with its committed checkpoint", current))
		}
		if !completed && input.PlannedState.Status == "active" && (input.PlannedState.NextTicket == nil || input.PlannedState.NextTicket.ID != current) {
			return activity.Result{}, contractError("plan.advance planned_state", fmt.Errorf("uncommitted ticket %s must remain current before other work can be committed", current))
		}
		raw, _ := json.Marshal(input.PlannedState)
		return activity.Result{Output: raw}, nil
	}
	state := input.State
	state.Completed = slices.Clone(state.Completed)
	state.Pending = slices.Clone(state.Pending)
	state.History = slices.Clone(state.History)
	if input.Verification.Approved {
		state.Completed = append(state.Completed, current)
		pending := make([]planning.Ticket, 0, len(state.Pending))
		for _, ticket := range state.Pending {
			if ticket.ID != current {
				pending = append(pending, ticket)
			}
		}
		state.Pending = pending
		state.NextTicket = nil
		for index, ticket := range state.Pending {
			if slices.Contains(state.Blocked, ticket.ID) {
				continue
			}
			ready := true
			for _, dependency := range ticket.Dependencies {
				if !slices.Contains(state.Completed, dependency) {
					ready = false
					break
				}
			}
			if ready {
				selected := ticket
				state.NextTicket = &selected
				state.Pending = append(state.Pending[:index:index], state.Pending[index+1:]...)
				break
			}
		}
		if state.NextTicket == nil {
			state.Status = "complete"
			if len(state.Pending) > 0 {
				state.Status = "blocked"
			}
		}
	}
	state.Summary = input.Verification.Summary
	if !input.Verification.Approved {
		state.Summary = "ticket " + current + " needs another implementation pass: " + strings.Join(input.Verification.Findings, "; ")
	}
	revision, err := input.State.Revision.Int64()
	if err != nil {
		return activity.Result{}, contractError("plan.advance state", fmt.Errorf("revision must be an integer: %w", err))
	}
	state.Revision = json.Number(fmt.Sprintf("%d", revision+1))
	entry, _ := json.Marshal(map[string]any{"ticket_id": current, "approved": input.Verification.Approved, "summary": state.Summary})
	state.History = append(state.History, entry)
	if _, err := planning.Resolve(state); err != nil {
		return activity.Result{}, contractError("plan.advance result", err)
	}
	raw, _ := json.Marshal(state)
	return activity.Result{Output: raw}, nil
}

// MutationConfigExecutor catches a requested but missing command before any
// agent work begins. The actual mutation command remains an end-of-flow gate.
type MutationConfigExecutor struct{}

func (MutationConfigExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "mutation.config", Version: "1", Effect: activity.EffectPure}
}

func (MutationConfigExecutor) Execute(_ context.Context, request activity.Request) (activity.Result, error) {
	var input struct {
		Enabled bool     `json:"enabled"`
		Argv    []string `json:"argv"`
	}
	if err := strictJSON(request.Inputs, &input); err != nil {
		return activity.Result{}, contractError("mutation.config input", err)
	}
	if input.Enabled {
		if len(input.Argv) == 0 {
			return activity.Result{}, contractError("mutation.config input", fmt.Errorf("mutation_enabled requires a nonempty mutation_argv"))
		}
		for _, item := range input.Argv {
			if strings.TrimSpace(item) == "" {
				return activity.Result{}, contractError("mutation.config input", fmt.Errorf("mutation_argv contains an empty argument"))
			}
		}
	}
	return activity.Result{Output: json.RawMessage(`{"ready":true}`)}, nil
}
