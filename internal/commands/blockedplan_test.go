package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/activity/builtin"
	"github.com/collectiveai-team/orquesta-lite/internal/flow"
	"github.com/collectiveai-team/orquesta-lite/internal/review"
	"github.com/collectiveai-team/orquesta-lite/internal/workflow"
)

type blockedPlanFixture struct {
	initial      map[string]any
	final        map[string]any
	schemas      map[string]*flow.Schema
	roles        map[string]int
	ticketScopes []string
	publication  []string
}

type blockedPlanExecutor struct {
	spec    activity.Spec
	fixture *blockedPlanFixture
}

func (e *blockedPlanExecutor) Spec() activity.Spec { return e.spec }

func (e *blockedPlanExecutor) Execute(_ context.Context, request activity.Request) (activity.Result, error) {
	var input map[string]any
	if err := json.Unmarshal(request.Inputs, &input); err != nil {
		return activity.Result{}, err
	}
	f := e.fixture
	var output any
	switch e.spec.Name {
	case "agent.invoke":
		role, _ := input["role"].(string)
		f.roles[role]++
		context, _ := input["context"].(map[string]any)
		vars, _ := input["vars"].(map[string]any)
		switch role {
		case "ticket_planner":
			mode, _ := context["MODE"].(string)
			if mode == "" {
				mode, _ = vars["MODE"].(string)
			}
			if mode == "advance" {
				output = f.final
			} else {
				output = f.initial
			}
		case "coder", "ticket_qa":
			ticket, ok := context["TICKET"].(map[string]any)
			if !ok || ticket["id"] == nil {
				return activity.Result{}, fmt.Errorf("%s invoked without a ticket", role)
			}
			if role == "coder" {
				output = map[string]any{"ticket_id": ticket["id"], "complete": true, "summary": "Implemented workable ticket", "files_changed": []string{}, "gates": []string{}, "remaining": []string{}}
			} else {
				output = map[string]any{"ticket_id": ticket["id"], "approved": true, "summary": "Workable ticket verified", "findings": []string{}, "gates": []string{}}
			}
		case "intake":
			output = map[string]any{"actionable": true, "summary": "Issue fixture", "plan": "Implement workable tickets", "missing_info": []string{}}
		case "qa", "adversary", "critic", "visual_verifier", "gov_reviewer":
			// Deliberately approve: the runtime must still withhold whole-plan
			// approval when its durable ticket frontier is externally blocked.
			output = review.Result{Version: 2, Status: "complete", Decision: "approve", Approved: true, Summary: "Implemented work reviewed", Findings: []review.Finding{}, Limitations: []string{}, ReviewedRevision: "fixture-revision", Evidence: []string{"fixture"}}
		case "integrator":
			return activity.Result{}, fmt.Errorf("blocked external work must not launch repair")
		default:
			return activity.Result{}, fmt.Errorf("unexpected agent role %q", role)
		}
		ref, _ := input["outputSchema"].(string)
		if schema := f.schemas[ref]; schema != nil {
			raw, _ := json.Marshal(output)
			if err := schema.ValidateJSON(raw); err != nil {
				return activity.Result{}, fmt.Errorf("fixture %s violates %s: %w", role, ref, err)
			}
		}
	case "git.commit":
		scope, _ := input["scope"].(string)
		if scope == "" {
			return activity.Result{}, fmt.Errorf("commit attempted without ticket or review scope")
		}
		if scope != "review" {
			f.ticketScopes = append(f.ticketScopes, scope)
		}
		output = map[string]any{"committed": true, "blocked": false, "retried": false, "exit_code": 0}
	case "gate.run":
		output = map[string]any{"passed": true, "exit_code": 0}
	case "command.run":
		argv, _ := input["argv"].([]any)
		if len(argv) >= 3 && argv[0] == "sh" {
			command, _ := argv[2].(string)
			if strings.Contains(command, "gh pr create") {
				f.publication = append(f.publication, command)
			}
		}
		output = map[string]any{"exitCode": 0, "stdout": "Issue fixture", "stderr": "", "durationMs": 0}
	default:
		return activity.Result{}, fmt.Errorf("unexpected fake activity %q", e.spec.Name)
	}
	raw, err := json.Marshal(output)
	return activity.Result{Output: raw}, err
}

func blockedPlanTicket(id string, dependencies ...string) map[string]any {
	if dependencies == nil {
		dependencies = []string{}
	}
	return map[string]any{"id": id, "title": "Ticket " + id, "objective": "Objective " + id, "acceptance_criteria": []string{"Expected behavior"}, "dependencies": dependencies, "files_hint": []string{}}
}

func blockedPlanState(status string, next any, pending []map[string]any, blocked []string, budget int) map[string]any {
	return map[string]any{"status": status, "revision": 1, "summary": "External calendar and credentials gate remaining work", "iteration_budget": budget, "next_ticket": next, "pending": pending, "completed": []string{}, "blocked": blocked, "risks": []string{}, "history": []string{"T02 requires external access; T03 depends on T02"}}
}

func runBlockedPlanFlow(t *testing.T, name string, initial, final map[string]any, inputs map[string]any) (*workflow.Run, []workflow.StepRun, *blockedPlanFixture, error) {
	t.Helper()
	root := governedPackRoot(t)
	version := "1"
	if name == "factory-governed" {
		version = "2"
	}
	var doc *flow.Document
	var err error
	if name == "develop-ticket" {
		doc, err = flow.Decode(strings.NewReader(`{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"direct-develop-ticket","version":"1"},"inputs":{"state":{"schema":"schema:workflow-state@2"}},"steps":[{"id":"develop","uses":"subflow:develop-ticket@1","with":{"features_path":"features.md","state":{"$ref":"inputs.state"}}}],"outputs":{"state":{"$ref":"steps.develop.output.state"}}}`))
	} else {
		doc, err = flow.Load(filepath.Join(root, "flows", name+"@"+version+".json"))
	}
	if err != nil {
		t.Fatal(err)
	}
	catalog := flow.NewDirectoryCatalog(root, builtinSpecs())
	ir, diagnostics := flow.Compile(doc, catalog)
	if diagnostics.HasErrors() {
		t.Fatalf("flow does not compile: %+v", diagnostics)
	}
	fixture := &blockedPlanFixture{initial: initial, final: final, schemas: ir.Schemas, roles: map[string]int{}}
	registry := activity.NewRegistry()
	for _, executor := range []activity.Executor{builtin.GateAssertExecutor{}, builtin.ReviewAggregateExecutor{}, builtin.PlanOutcomeExecutor{}, builtin.PlanReviewExecutor{}} {
		if err := registry.Register(executor); err != nil {
			t.Fatal(err)
		}
	}
	for _, spec := range builtin.Specs() {
		if slices.Contains([]string{"agent.invoke", "git.commit", "gate.run", "command.run"}, spec.Name) {
			if err := registry.Register(&blockedPlanExecutor{spec: spec, fixture: fixture}); err != nil {
				t.Fatal(err)
			}
		}
	}
	store, err := workflow.Open(filepath.Join(t.TempDir(), "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := &workflow.Runtime{Store: store, Activities: registry, Catalog: catalog, Config: map[string]any{"lint_argv": []any{"fixture-lint"}, "test_argv": []any{"fixture-tests"}}}
	run, runErr := runtime.Start(context.Background(), ir, workflow.StartOptions{RunID: "blocked-plan-fixture", Inputs: inputs, Policy: workflow.Policy{MaxAttempts: 100}})
	steps, err := store.ListSteps(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run, steps, fixture, runErr
}

func assertBlockedPlanOutcome(t *testing.T, steps []workflow.StepRun) {
	t.Helper()
	found := false
	for _, step := range steps {
		if step.StepID != "plan_outcome" {
			continue
		}
		found = true
		var out struct {
			Status   string           `json:"status"`
			Complete bool             `json:"complete"`
			Blocked  []string         `json:"blocked"`
			Pending  []map[string]any `json:"pending"`
		}
		if err := json.Unmarshal(step.Output, &out); err != nil {
			t.Fatal(err)
		}
		if out.Status != "blocked" || out.Complete || !slices.Equal(out.Blocked, []string{"T02", "T03"}) || len(out.Pending) != 2 {
			t.Fatalf("blocked work was lost or marked complete: %s", step.Output)
		}
	}
	if !found {
		t.Fatal("no durable normalized plan outcome")
	}
}

func TestBlockedPlanStillRunsIntegratedReview(t *testing.T) {
	for _, name := range []string{"factory-governed", "issue-fix"} {
		for _, terminal := range []string{"active", "blocked"} {
			t.Run(name+"/"+terminal, func(t *testing.T) {
				workable := blockedPlanTicket("T01")
				external := blockedPlanTicket("T02")
				dependent := blockedPlanTicket("T03", "T02")
				initial := blockedPlanState("active", workable, []map[string]any{workable, external, dependent}, []string{"T02", "T03"}, 3)
				final := blockedPlanState(terminal, nil, []map[string]any{external, dependent}, []string{"T02", "T03"}, 3)
				final["completed"] = []string{"T01"}
				inputs := map[string]any{"features_path": "features.md", "fast": false, "create_pr": true, "strict_governance": true}
				if name == "issue-fix" {
					inputs = map[string]any{"issue_path": "issue.md", "run": true, "strict_governance": true}
				}
				run, steps, f, err := runBlockedPlanFlow(t, name, initial, final, inputs)
				if err != nil || run.Status != workflow.RunSucceeded {
					t.Fatalf("blocked plan prevented final review: run=%+v err=%v", run, err)
				}
				if f.roles["coder"] != 1 || f.roles["ticket_qa"] != 1 || !slices.Equal(f.ticketScopes, []string{"T01"}) {
					t.Fatalf("empty ticket invoked/committed: roles=%v scopes=%v", f.roles, f.ticketScopes)
				}
				for _, role := range []string{"qa", "adversary", "critic", "visual_verifier", "gov_reviewer"} {
					if f.roles[role] != 1 {
						t.Fatalf("final reviewer %s calls=%d", role, f.roles[role])
					}
				}
				assertBlockedPlanOutcome(t, steps)
				for _, step := range steps {
					if step.StepID == "integrated_review" {
						var result map[string]any
						if err := json.Unmarshal(step.Output, &result); err != nil {
							t.Fatal(err)
						}
						if result["approved"] != false {
							t.Fatalf("blocked plan was approved: %s", step.Output)
						}
					}
				}
				if name == "factory-governed" {
					if len(f.publication) != 1 || !strings.Contains(f.publication[0], "--draft") {
						t.Fatalf("externally blocked plan published approved PR: %v", f.publication)
					}
				}
			})
		}
	}
}

func TestInitiallyBlockedPlanNeverInvokesEmptyTicket(t *testing.T) {
	for _, name := range []string{"factory-governed", "task-list", "issue-fix"} {
		t.Run(name, func(t *testing.T) {
			state := blockedPlanState("active", nil, []map[string]any{blockedPlanTicket("T02"), blockedPlanTicket("T03", "T02")}, []string{"T02", "T03"}, 3)
			inputs := map[string]any{"features_path": "features.md", "fast": false}
			if name == "issue-fix" {
				inputs = map[string]any{"issue_path": "issue.md", "run": true}
			}
			run, steps, f, err := runBlockedPlanFlow(t, name, state, state, inputs)
			if err != nil || run.Status != workflow.RunSucceeded {
				t.Fatalf("initial empty frontier failed: run=%+v err=%v", run, err)
			}
			if f.roles["coder"] != 0 || f.roles["ticket_qa"] != 0 || len(f.ticketScopes) != 0 {
				t.Fatalf("empty frontier invoked ticket work: roles=%v commits=%v", f.roles, f.ticketScopes)
			}
			assertBlockedPlanOutcome(t, steps)
			if name != "task-list" && f.roles["gov_reviewer"] != 1 {
				t.Fatal("initially blocked plan skipped integrated review")
			}
		})
	}
}

func TestBlockedPlanFixDoesNotHideIterationExhaustion(t *testing.T) {
	for _, name := range []string{"factory-governed", "task-list", "issue-fix"} {
		t.Run(name, func(t *testing.T) {
			ticket := blockedPlanTicket("T01")
			state := blockedPlanState("active", ticket, []map[string]any{ticket}, []string{}, 1)
			inputs := map[string]any{"features_path": "features.md", "fast": false}
			if name == "issue-fix" {
				inputs = map[string]any{"issue_path": "issue.md", "run": true}
			}
			run, _, f, err := runBlockedPlanFlow(t, name, state, state, inputs)
			if err == nil || run.Status == workflow.RunSucceeded {
				t.Fatalf("unfinished workable plan silently succeeded: run=%+v err=%v", run, err)
			}
			if f.roles["coder"] != 1 || f.roles["gov_reviewer"] != 0 {
				t.Fatalf("budget exhaustion ran unexpected work: %v", f.roles)
			}
		})
	}
}

func TestBlockedPlanDirectTicketSubflowGuardsNullTicket(t *testing.T) {
	for _, status := range []string{"active", "blocked"} {
		t.Run(status, func(t *testing.T) {
			state := blockedPlanState(status, nil, []map[string]any{blockedPlanTicket("T02"), blockedPlanTicket("T03", "T02")}, []string{"T02", "T03"}, 3)
			run, _, f, err := runBlockedPlanFlow(t, "develop-ticket", state, state, map[string]any{"state": state})
			if err != nil || run.Status != workflow.RunSucceeded {
				t.Fatalf("direct no-ticket call failed: run=%+v err=%v", run, err)
			}
			if len(f.roles) != 0 || len(f.ticketScopes) != 0 {
				t.Fatalf("direct no-ticket call produced effects: roles=%v scopes=%v", f.roles, f.ticketScopes)
			}
		})
	}
}
