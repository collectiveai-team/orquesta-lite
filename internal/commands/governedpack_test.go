package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/flow"
	"github.com/lionelchamorro/orquestalite/internal/workflow"
)

func governedPackRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "examples", "governed-pack", "pack"))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// The governed-pack example must always satisfy the cutover gate's required
// development flows: they must exist, verify against pack.json, and compile.
func TestGovernedPackRequiredFlowsCompile(t *testing.T) {
	root := governedPackRoot(t)
	if _, err := flow.LoadPack(root); err != nil {
		t.Fatalf("pack.json digests are stale — run examples/governed-pack/regen-digests.py: %v", err)
	}
	catalog := flow.NewDirectoryCatalog(root, builtinSpecs())
	for _, name := range []string{"factory-fast", "factory-governed", "issue-fix", "plan-tickets", "pr-review", "review-existing", "task-list"} {
		ref := flow.ResourceRef{Kind: "flow", Name: name, Version: "1"}
		doc, _, resolveErr := catalog.ResolveDocument(ref)
		if resolveErr != nil {
			t.Errorf("%s: %v", name, resolveErr)
			continue
		}
		if _, diagnostics := flow.Compile(doc, catalog); diagnostics.HasErrors() {
			t.Errorf("%s does not compile: %+v", name, diagnostics)
		}
	}
}

// Parity gate: every flow in the pack declares its own policy, so none of them
// can fall through to the engine default when launched without --policy.
func TestGovernedPackFlowsAllDeclareAPolicy(t *testing.T) {
	root := governedPackRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, "flows"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no flows found")
	}
	for _, entry := range entries {
		doc, loadErr := flow.Load(filepath.Join(root, "flows", entry.Name()))
		if loadErr != nil {
			t.Errorf("%s: %v", entry.Name(), loadErr)
			continue
		}
		if doc.Metadata.Policy == "" {
			t.Errorf("%s does not declare metadata.policy", entry.Name())
		}
	}
}

// Parity gate: no flow, subflow, or prompt in the pack hardcodes the benchmark
// project's toolchain. Uninstalling a pack cannot remove a gate baked into a
// subflow, which is why this is checked over the whole pack and not just flows.
func TestGovernedPackHasNoHardcodedToolchain(t *testing.T) {
	root := governedPackRoot(t)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(raw), "uv ") {
			relative, _ := filepath.Rel(root, path)
			t.Errorf("%s still hardcodes a `uv ` invocation; gates come from team.json now", relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The regression test for the round-2 incident and the real 24-ticket run: a
// governed flow launched with no --policy must run on the pack's own policy,
// not the engine default's 32-attempt budget.
func TestGovernedFactoryLoadsItsPolicyWithoutTheFlag(t *testing.T) {
	project := t.TempDir()
	installGovernedPack(t, project)
	compiled, err := compileWorkflowTarget(project, "development/factory-governed@1")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := loadWorkflowPolicy(compiled, "")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Source != "flow-metadata" || resolved.Ref != "policy:development@3" {
		t.Fatalf("resolved=%+v", resolved)
	}
	if resolved.Policy.MaxAttempts != 0 || resolved.Policy.MaxAgentAttempts != 0 {
		t.Fatalf("attempt budgets must be unlimited — any cap is a covert ticket cap: %+v", resolved.Policy)
	}
	if resolved.Policy.MaxAttempts == workflow.DefaultPolicy().MaxAttempts {
		t.Fatal("the 32-attempt engine default leaked into a governed run again")
	}
	if resolved.Policy.MaxDurationSeconds != 28800 || resolved.Policy.MaxCostUSD != 250 || resolved.Policy.MaxParallelism != 1 {
		t.Fatalf("the economic brakes are the real limits now: %+v", resolved.Policy)
	}
}

// The pack's own gates must resolve against the example team.json — if they
// did not, every governed run would abort at startup.
func TestGovernedPackGatesValidateAgainstTheExampleTeamConfig(t *testing.T) {
	project := t.TempDir()
	installGovernedPack(t, project)
	teamPath := filepath.Join("..", "..", "examples", "governed-pack", "team.json")
	config := loadGateConfig(teamPath)
	for _, name := range []string{"factory-governed", "task-list", "issue-fix", "factory-fast", "review-existing"} {
		compiled, err := compileWorkflowTarget(project, "development/"+name+"@1")
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if err = validateConfigReferences(compiled.IR, config); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The plan-driven loops take their bound from the plan, not a literal.
func TestGovernedPackPlanDrivenLoopsDeriveTheirBound(t *testing.T) {
	project := t.TempDir()
	installGovernedPack(t, project)
	for name, stepID := range map[string]string{
		"factory-governed": "develop_tickets",
		"task-list":        "develop_tickets",
		"issue-fix":        "develop_tickets",
	} {
		compiled, err := compileWorkflowTarget(project, "development/"+name+"@1")
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		found := false
		for _, step := range compiled.IR.Steps {
			if step.ID != stepID || step.While == nil {
				continue
			}
			found = true
			if step.While.MaxIterations.Ref == nil || step.While.MaxIterations.Ref.Path != "item.state.iteration_budget" {
				t.Errorf("%s.%s bound = %+v, want a reference to item.state.iteration_budget", name, stepID, step.While.MaxIterations)
			}
		}
		if !found {
			t.Errorf("%s has no while step %s", name, stepID)
		}
	}
}

// installGovernedPack installs the example pack into a scratch project, which
// is also an end-to-end exercise of LoadPack's digest verification.
func installGovernedPack(t *testing.T, project string) {
	t.Helper()
	source := governedPackRoot(t)
	pack, err := flow.LoadPack(source)
	if err != nil {
		t.Fatalf("pack.json digests are stale — run examples/governed-pack/regen-digests.py: %v", err)
	}
	dest := filepath.Join(project, ".orquestalite", "packs", pack.Name, pack.Version)
	for relative := range pack.Files {
		raw, readErr := os.ReadFile(filepath.Join(source, filepath.FromSlash(relative)))
		if readErr != nil {
			t.Fatal(readErr)
		}
		path := filepath.Join(dest, filepath.FromSlash(relative))
		if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(source, "pack.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dest, "pack.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
