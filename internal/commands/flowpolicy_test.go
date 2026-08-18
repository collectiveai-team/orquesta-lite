package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/workflow"
)

const budgetedPolicy = `{"maxDurationSeconds":28800,"maxAttempts":0,"maxAgentAttempts":0,"maxCostUSD":250,"maxParallelism":1}`
const tightPolicy = `{"maxAttempts":7,"maxParallelism":1}`

// packWithPolicies installs a pack whose flow declares metadata.policy, plus a
// sibling flow that declares none.
func packWithPolicies(t *testing.T, projectDir string) {
	t.Helper()
	installFixturePack(t, projectDir, "development", "1", map[string]string{
		"policies/development@3.json": budgetedPolicy,
		"policies/tight@1.json":       tightPolicy,
		"flows/governed@1.json":       `{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"governed","version":"1","policy":"policy:development@3"},"steps":[{"id":"noop","uses":"activity:command.run@1","with":{"argv":["/usr/bin/true"]}}]}`,
	})
}

func compileGoverned(t *testing.T, projectDir, ref string) *compiledWorkflow {
	t.Helper()
	compiled, err := compileWorkflowTarget(projectDir, ref)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// Precedence 2: a pack's declared policy loads with no --policy at all. This is
// the regression guard for the incident that motivated the change — a governed
// run silently inheriting DefaultPolicy's 32-attempt budget.
func TestPolicyPrecedenceUsesFlowMetadataWithoutFlag(t *testing.T) {
	dir := t.TempDir()
	packWithPolicies(t, dir)
	resolved, err := loadWorkflowPolicy(compileGoverned(t, dir, "development/governed@1"), "")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Source != "flow-metadata" || resolved.Ref != "policy:development@3" {
		t.Fatalf("resolved=%+v", resolved)
	}
	if resolved.Policy.MaxAttempts != 0 || resolved.Policy.MaxDurationSeconds != 28800 || resolved.Policy.MaxCostUSD != 250 {
		t.Fatalf("declared policy was not applied: %+v", resolved.Policy)
	}
	if resolved.Policy.MaxAttempts == workflow.DefaultPolicy().MaxAttempts {
		t.Fatal("the engine default's attempt budget leaked into a governed run")
	}
}

// Precedence 1: an explicit flag still wins, for experiments and benchmarks.
func TestPolicyPrecedenceFlagOverridesFlowMetadata(t *testing.T) {
	dir := t.TempDir()
	packWithPolicies(t, dir)
	resolved, err := loadWorkflowPolicy(compileGoverned(t, dir, "development/governed@1"), "policy:tight@1")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Source != "flag" || resolved.Policy.MaxAttempts != 7 {
		t.Fatalf("resolved=%+v", resolved)
	}
}

// Precedence 3: a flow that declares nothing still gets the engine default.
func TestPolicyPrecedenceFallsBackToDefault(t *testing.T) {
	dir := t.TempDir()
	packWithPolicies(t, dir)
	resolved, err := loadWorkflowPolicy(compileGoverned(t, dir, "development/probe@1"), "")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Source != "default" || resolved.Policy.MaxAttempts != workflow.DefaultPolicy().MaxAttempts {
		t.Fatalf("resolved=%+v", resolved)
	}
}

// A metadata.policy that does not resolve must fail at compile time — before
// any step runs — not at the moment the scheduler reaches for it.
func TestFlowWithUnresolvablePolicyFailsValidation(t *testing.T) {
	dir := t.TempDir()
	installFixturePack(t, dir, "broken", "1", map[string]string{
		"flows/ghost@1.json": `{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"ghost","version":"1","policy":"policy:missing@9"},"steps":[{"id":"noop","uses":"activity:command.run@1","with":{"argv":["/usr/bin/true"]}}]}`,
	})
	var out bytes.Buffer
	err := FlowCLI(context.Background(), dir, []string{"validate", "broken/ghost@1"}, &out)
	if err == nil || !strings.Contains(err.Error(), "metadata.policy") {
		t.Fatalf("err=%v", err)
	}
	out.Reset()
	if err = FlowCLI(context.Background(), dir, []string{"run", "broken/ghost@1"}, &out); err == nil {
		t.Fatal("a flow with an unresolvable policy must not start")
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".orquestalite", "workflows.db")); statErr == nil {
		t.Fatal("no run may be created for a flow that fails to compile")
	}
}

// The run line names both the policy and which precedence branch produced it.
func TestFlowRunReportsPolicySource(t *testing.T) {
	dir := t.TempDir()
	packWithPolicies(t, dir)
	var out bytes.Buffer
	if err := FlowCLI(context.Background(), dir, []string{"run", "development/governed@1"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "policy=policy:development@3 policy_source=flow-metadata") {
		t.Fatalf("out=%s", out.String())
	}
	out.Reset()
	if err := FlowCLI(context.Background(), dir, []string{"run", "development/probe@1"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "policy_source=default") {
		t.Fatalf("out=%s", out.String())
	}
	out.Reset()
	if err := FlowCLI(context.Background(), dir, []string{"run", "development/governed@1", "--policy=policy:tight@1"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "policy=policy:tight@1 policy_source=flag") {
		t.Fatalf("out=%s", out.String())
	}
}
