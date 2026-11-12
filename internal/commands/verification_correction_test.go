package commands

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/activity/builtin"
	"github.com/collectiveai-team/orquesta-lite/internal/checks"
	"github.com/collectiveai-team/orquesta-lite/internal/review"
	"github.com/collectiveai-team/orquesta-lite/internal/risk"
	"github.com/collectiveai-team/orquesta-lite/internal/supervision"
	"github.com/collectiveai-team/orquesta-lite/internal/telemetry"
	"github.com/collectiveai-team/orquesta-lite/internal/workflow"
)

type correctionLifecycleAgent struct {
	project         string
	failCorrection  bool
	implementations int
	corrections     int
	reviews         int
}

func (*correctionLifecycleAgent) Spec() activity.Spec {
	return activity.Spec{Name: "agent.invoke", Version: "1", Effect: activity.EffectAtMostOnce}
}

func (e *correctionLifecycleAgent) Execute(ctx context.Context, request activity.Request) (activity.Result, error) {
	var input struct {
		Role     string                     `json:"role"`
		Unit     string                     `json:"unit"`
		Snapshot checks.Snapshot            `json:"snapshot"`
		Context  map[string]json.RawMessage `json:"context"`
	}
	if err := json.Unmarshal(request.Inputs, &input); err != nil {
		return activity.Result{}, err
	}
	iteration := func() activity.Result {
		return activity.Result{Output: json.RawMessage(`{"continue":false,"summary":"product change complete","remaining":[]}`)}
	}
	switch input.Role {
	case "verified_coder":
		e.implementations++
		if err := os.WriteFile(filepath.Join(e.project, "source.txt"), []byte("bad\n1\n2\n3\n"), 0600); err != nil {
			return activity.Result{}, err
		}
		return iteration(), nil
	case "verified_integrator":
		e.corrections++
		identity, _ := json.Marshal([]string{request.RunID, request.ScopePath, request.ForeachKey, request.StepID})
		task := fmt.Sprintf("%x", sha256.Sum256(identity))
		root := filepath.Join(e.project, ".orquestalite", "runs", request.RunID)
		units := risk.UnitStore{Root: filepath.Join(root, "units")}
		if _, err := units.ClaimInvocation(input.Unit, task); err != nil {
			return activity.Result{}, err
		}
		body := "good\n1\n2\n3\n"
		if e.failCorrection {
			body = "stillbad\n1\n2\n3\n"
		}
		if err := os.WriteFile(filepath.Join(e.project, "source.txt"), []byte(body), 0600); err != nil {
			return activity.Result{}, err
		}
		call := telemetry.NewCall("correction-"+task, task, input.Role, "codex", "gpt-5.6-terra", "prefix", "api_call", map[string]int{"input_tokens": 10, "cached_input_tokens": 0, "cache_write_tokens": 0, "output_tokens": 5})
		if err := telemetry.Append(root, []telemetry.Call{call}); err != nil {
			return activity.Result{}, err
		}
		if err := supervision.AppendObservation(root, supervision.Record{Task: task, Role: input.Role, Time: time.Now().UTC(), Observation: supervision.Observation{Tool: "apply_patch", Nonmechanical: true}}); err != nil {
			return activity.Result{}, err
		}
		return iteration(), nil
	case "verified_reviewer":
		e.reviews++
		var lens string
		if err := json.Unmarshal(input.Context["LENS"], &lens); err != nil {
			return activity.Result{}, err
		}
		var comparisons []struct {
			Reference string `json:"reference"`
			CheckID   string `json:"check_id"`
		}
		if err := json.Unmarshal(input.Context["COMPARISONS"], &comparisons); err != nil {
			return activity.Result{}, err
		}
		if len(comparisons) != 1 {
			return activity.Result{}, fmt.Errorf("expected one runtime comparison, got %d", len(comparisons))
		}
		finding := review.ClaimV3{Category: "runtime_check", Severity: "medium", State: "open", Location: "verify.sh:1", Trigger: "unit-source", Impact: "declared source verification fails", ProofRefs: []string{comparisons[0].Reference}, Causal: "introduced"}
		if priorRaw, ok := input.Context["PREVIOUS_REVIEW"]; ok {
			var prior review.ResultV3
			if err := json.Unmarshal(priorRaw, &prior); err != nil {
				return activity.Result{}, err
			}
			if len(prior.Findings) != 1 {
				return activity.Result{}, fmt.Errorf("historical finding missing")
			}
			finding = prior.Findings[0].ClaimV3
			finding.ProofRefs = append(finding.ProofRefs, comparisons[0].Reference)
			source, err := os.ReadFile(filepath.Join(input.Snapshot.Dir, "source.txt"))
			if err != nil {
				return activity.Result{}, err
			}
			if strings.HasPrefix(string(source), "good\n") {
				finding.State = "resolved"
				finding.Resolution = "Declared check passes on corrected candidate"
			}
		}
		claims := review.ClaimsV3{Version: 3, Lens: lens, Status: "complete", Summary: "runtime check comparison inspected", Findings: []review.ClaimV3{finding}, Limitations: []string{}, ReviewedRevision: input.Snapshot.TreeHash, Evidence: []string{"snapshot:" + input.Snapshot.TreeHash}}
		raw, _ := json.Marshal(claims)
		return activity.Result{Output: raw}, nil
	default:
		return activity.Result{}, fmt.Errorf("unexpected fake role %s", input.Role)
	}
}

func TestVerifiedCorrectionLifecycle(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "passes"
		if failed {
			name = "fails"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			project := verifiedProject(t)
			// Keep fixture configuration out of the candidate's product diff: the
			// coder changes one old line and adds three, yielding a 3-line budget.
			for _, args := range [][]string{{"add", "team.json", checks.DefaultPath}, {"-c", "core.hooksPath=/dev/null", "commit", "-qm", "fixture configuration"}} {
				command := exec.Command("git", args...)
				command.Dir = project
				if raw, err := command.CombinedOutput(); err != nil {
					t.Fatalf("fixture git: %v %s", err, raw)
				}
			}
			compiled, err := compileWorkflowTarget(project, verifiedTarget(t, "verified-development"))
			if err != nil {
				t.Fatal(err)
			}
			store, err := workflow.Open(filepath.Join(project, ".orquestalite", "workflows.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			id := "correction-" + name
			registry := activity.NewRegistry()
			if err := registerVerification(project, id, compiled, registry, store); err != nil {
				t.Fatal(err)
			}
			agent := &correctionLifecycleAgent{project: project, failCorrection: failed}
			for _, executor := range []activity.Executor{agent, builtin.GateAssertExecutor{}} {
				if err := registry.Register(executor); err != nil {
					t.Fatal(err)
				}
			}
			runtime := &workflow.Runtime{Store: store, Activities: registry, Catalog: compiled.Catalog, CompletionValidator: func(ctx context.Context, run *workflow.Run) error {
				return verifyFinalCandidate(ctx, project, store, run)
			}}
			run, err := runtime.Start(ctx, compiled.IR, workflow.StartOptions{RunID: id, Inputs: map[string]any{"features_path": "features.md"}, Policy: workflow.Policy{MaxAttempts: 100}})
			if failed {
				if !errors.Is(err, workflow.ErrNeedsHuman) || run.Status != workflow.RunNeedsHuman {
					t.Fatalf("failed correction status=%s err=%v", run.Status, err)
				}
			} else if err != nil || run.Status != workflow.RunSucceeded {
				t.Fatalf("passing correction status=%s err=%v", run.Status, err)
			}
			if agent.implementations != 1 || agent.corrections != 1 || agent.reviews != 2 {
				t.Fatalf("unexpected calls: implementation=%d correction=%d review=%d", agent.implementations, agent.corrections, agent.reviews)
			}
			units := risk.UnitStore{Root: filepath.Join(project, ".orquestalite", "runs", id, "units")}
			unit, err := units.Load("change")
			if err != nil {
				t.Fatal(err)
			}
			if unit.PatchLines != 3 || unit.Risk.Candidate.ChangedLines != 5 || unit.CorrectionTask == "" {
				t.Fatalf("unit budget/identity=%+v", unit)
			}
			if unit.Risk.Level != "low" || len(unit.Risk.Lenses) != 0 {
				t.Fatalf("original risk changed: %+v", unit.Risk)
			}
			if failed {
				if unit.Status != "needs_decision" {
					t.Fatalf("failed unit status=%s", unit.Status)
				}
				resumed, resumeErr := runtime.Resume(ctx, id)
				if !errors.Is(resumeErr, workflow.ErrNeedsHuman) || resumed.Status != workflow.RunNeedsHuman || agent.corrections != 1 {
					t.Fatalf("failed correction reran: status=%s err=%v calls=%d", resumed.Status, resumeErr, agent.corrections)
				}
			} else {
				if unit.Status != "verified" || unit.Validation == nil || unit.Validation.ChangedLines != 2 || unit.Validation.Tokens != 15 || unit.Validation.ToolCalls != 1 {
					t.Fatalf("successful validation=%+v", unit)
				}
			}
		})
	}
}
