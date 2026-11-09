package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/activity/builtin"
	"github.com/collectiveai-team/orquesta-lite/internal/checks"
	"github.com/collectiveai-team/orquesta-lite/internal/eventlog"
	"github.com/collectiveai-team/orquesta-lite/internal/flow"
	"github.com/collectiveai-team/orquesta-lite/internal/review"
	"github.com/collectiveai-team/orquesta-lite/internal/workflow"
)

func adversarialVerifiedRuntime(t *testing.T, project, id string) (*compiledWorkflow, *workflow.Store, *activity.Registry, *workflow.Runtime, *verifiedFakeAgent) {
	t.Helper()
	compiled, err := compileWorkflowTarget(project, verifiedTarget(t, "verified-development"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := workflow.Open(filepath.Join(project, ".orquestalite", "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	registry := activity.NewRegistry()
	if err := registerVerification(project, id, compiled, registry, store); err != nil {
		t.Fatal(err)
	}
	fake := &verifiedFakeAgent{project: project}
	for _, executor := range []activity.Executor{fake, builtin.GateAssertExecutor{}} {
		if err := registry.Register(executor); err != nil {
			t.Fatal(err)
		}
	}
	runtime := &workflow.Runtime{Store: store, Activities: registry, Catalog: compiled.Catalog,
		CompletionValidator: func(ctx context.Context, run *workflow.Run) error {
			return verifyFinalCandidate(ctx, project, store, run)
		},
	}
	return compiled, store, registry, runtime, fake
}

func TestVerifiedClosureRejectsOmittedChecksWithAuthenticReview(t *testing.T) {
	ctx := context.Background()
	project := verifiedProject(t)
	const id = "forged-checks"
	compiled, store, registry, runtime, _ := adversarialVerifiedRuntime(t, project, id)
	run, err := runtime.Start(ctx, compiled.IR, workflow.StartOptions{RunID: id, Inputs: map[string]any{"features_path": "features.md"}, Policy: workflow.Policy{MaxAttempts: 100}})
	if err != nil || run.Status != workflow.RunSucceeded {
		t.Fatalf("initial verified run failed: %v %+v", err, run)
	}
	steps, err := store.ListSteps(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var original map[string]json.RawMessage
	for _, step := range steps {
		if activity.ExecutorRef(step.ActivityRef) == "unit.close@1" && step.Status == workflow.StepSucceeded {
			if err = json.Unmarshal(step.Inputs, &original); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if original == nil {
		t.Fatal("fixture did not close a verified unit")
	}
	var verdict review.ResultV3
	if err = json.Unmarshal(original["review"], &verdict); err != nil {
		t.Fatal(err)
	}
	if err = persistedReview(ctx, store, registry, id, verdict); err != nil {
		t.Fatalf("authentic review is invalid: %v", err)
	}
	var forged builtin.ChecksOutput
	if err = json.Unmarshal(original["checks"], &forged); err != nil {
		t.Fatal(err)
	}
	forged.Selected = []string{}
	forged.Evidence = []builtin.CheckEvidence{}
	forged.Passed = true
	original["checks"] = mustJSON(forged)
	executor, ok := registry.Resolve("unit.close@1")
	if !ok {
		t.Fatal("closure executor missing")
	}
	_, err = executor.Execute(ctx, activity.Request{RunID: id, StepID: "forged_close", Attempt: 1, Inputs: mustJSON(original)})
	var decision *activity.ApprovalRequiredError
	if !errors.As(err, &decision) || !strings.Contains(err.Error(), "persisted runtime result") {
		t.Fatalf("omitted checks closed with authentic review: %v", err)
	}
}

type mutateAfterVerification struct {
	project string
	calls   int
}

func (*mutateAfterVerification) Spec() activity.Spec {
	return activity.Spec{Name: "test.mutate_after_verification", Version: "1", Effect: activity.EffectAtMostOnce}
}
func (e *mutateAfterVerification) Execute(context.Context, activity.Request) (activity.Result, error) {
	e.calls++
	err := os.WriteFile(filepath.Join(e.project, "source.txt"), []byte("unverified\n"), 0600)
	return activity.Result{Output: json.RawMessage(`{"changed":true}`)}, err
}

func TestVerifiedCompletionRejectsProductMutationAfterClosure(t *testing.T) {
	ctx := context.Background()
	project := verifiedProject(t)
	const id = "mutation-after-close"
	compiled, _, registry, runtime, fake := adversarialVerifiedRuntime(t, project, id)
	mutator := &mutateAfterVerification{project: project}
	if err := registry.Register(mutator); err != nil {
		t.Fatal(err)
	}
	spec := mutator.Spec()
	compiled.IR.Steps = append(compiled.IR.Steps, flow.IRStep{ID: "unverified_mutation", Uses: flow.ResourceRef{Kind: "activity", Name: spec.Name, Version: spec.Version}, Activity: &spec})
	if err := compiled.IR.Rehash(); err != nil {
		t.Fatal(err)
	}
	run, err := runtime.Start(ctx, compiled.IR, workflow.StartOptions{RunID: id, Inputs: map[string]any{"features_path": "features.md"}, Policy: workflow.Policy{MaxAttempts: 100}})
	if !errors.Is(err, workflow.ErrNeedsHuman) || run == nil || run.Status != workflow.RunNeedsHuman {
		t.Fatalf("unverified final product accepted: run=%+v err=%v", run, err)
	}
	if fake.implementations != 1 || mutator.calls != 1 {
		t.Fatal("fixture did not execute the mutation after one implementation")
	}
	source, err := os.ReadFile(filepath.Join(project, "source.txt"))
	if err != nil || string(source) != "unverified\n" {
		t.Fatalf("fixture mutation absent: %s %v", source, err)
	}
	_, err = runtime.Resume(ctx, id)
	if !errors.Is(err, workflow.ErrNeedsHuman) {
		t.Fatalf("pending integrity decision bypassed on resume: %v", err)
	}
	if fake.implementations != 1 || mutator.calls != 1 {
		t.Fatal("integrity failure repeated an already applied product effect")
	}
}

func TestVerificationDependencyResolutionPreservesPinnedIRHash(t *testing.T) {
	ctx := context.Background()
	project := verifiedProject(t)
	// This test constructs dependencies but never invokes a provider or proxy.
	teamPath := filepath.Join(project, "team.json")
	raw, err := os.ReadFile(teamPath)
	if err != nil {
		t.Fatal(err)
	}
	var team map[string]any
	if err = json.Unmarshal(raw, &team); err != nil {
		t.Fatal(err)
	}
	team["runtime"] = map[string]any{"context_optimization": map[string]any{"compression_proxy": map[string]any{"enabled": false}, "command_filter": map[string]any{"enabled": false}}}
	if err = os.WriteFile(teamPath, mustJSON(team), 0600); err != nil {
		t.Fatal(err)
	}
	compiled, err := compileWorkflowTarget(project, verifiedTarget(t, "verified-development"))
	if err != nil {
		t.Fatal(err)
	}
	before := mustJSON(compiled.IR)
	digest := compiled.IR.Digest
	if checks.Hash(before) != string(digest) {
		t.Fatal("compiled fixture digest already mismatches its IR")
	}
	deps, err := newWorkflowDeps(project, teamPath, "pinned-dependencies", compiled, eventlog.FormatVerbose)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = deps.close(ctx) })
	if string(before) != string(mustJSON(compiled.IR)) || compiled.IR.Digest != digest {
		t.Fatal("resolving host prompts mutated the pinned IR or its definition hash")
	}
}
