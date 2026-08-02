package commands

import (
	"os"
	"path/filepath"
	"slices"
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

// The governed pack's required development flows must exist, verify against
// pack.json, and compile.
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
//
// The rule lives in packtoolchain_test.go, where it is itself tested against
// both encodings of a legacy gate. It used to be an inlined `strings.Contains(raw, "uv ")`
// here, which the JSON argv form (`["uv","run",...]`) walks straight past.
func TestGovernedPackHasNoHardcodedToolchain(t *testing.T) {
	assertPackHasNoHardcodedToolchain(t, governedPackRoot(t))
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
	// The attempt budgets are backstops, not budgets: high enough that the
	// ticket loop's own bound always binds first, low enough to stop a genuine
	// runaway. Zero (unlimited) is not acceptable either — that was the first
	// answer, and it left maxDurationSeconds as the only working brake because
	// maxCostUSD prices through a table that has no entry for the models this
	// pack runs. TestGovernedPackAttemptBackstopExceedsItsOwnLoopCeiling pins
	// the derivation; this only checks the policy that actually loaded carries
	// one.
	if resolved.Policy.MaxAgentAttempts <= 0 || resolved.Policy.MaxAttempts <= 0 {
		t.Fatalf("a governed run needs a runaway backstop that works today, not an unlimited budget: %+v", resolved.Policy)
	}
	if resolved.Policy.MaxAttempts == workflow.DefaultPolicy().MaxAttempts {
		t.Fatal("the 32-attempt engine default leaked into a governed run again")
	}
	if resolved.Policy.MaxDurationSeconds != 28800 || resolved.Policy.MaxCostUSD != 250 || resolved.Policy.MaxParallelism != 1 {
		t.Fatalf("wall-clock, cost ceiling and serialization must survive policy edits: %+v", resolved.Policy)
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

// Parity gate: every flow that takes its loop bound from the plan also blocks
// on the plan having finished.
//
// A `while` that stops because it hit its bound returns *normally* — the
// scheduler breaks out of the loop and the flow carries on to its final gates,
// reporting success over a backlog that is still half pending. The bound and
// the gate are therefore one feature, not two, and rolling the bound out to
// three flows while gating only one is what makes budget exhaustion silent in
// exactly the flows behind the CLI aliases. That is the round-3 lesson: a
// reproduced failure needs a blocking gate, not prose.
func TestGovernedPackPlanDrivenLoopsAreGatedOnCompletion(t *testing.T) {
	project := t.TempDir()
	installGovernedPack(t, project)
	for _, name := range []string{"factory-governed", "task-list", "issue-fix"} {
		compiled, err := compileWorkflowTarget(project, "development/"+name+"@1")
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		var loops, gated []string
		for _, step := range compiled.IR.Steps {
			if step.While != nil {
				loops = append(loops, step.ID)
			}
			if step.Uses.Kind != "activity" || step.Uses.Name != "gate.assert" {
				continue
			}
			value, ok := step.With["value"]
			if !ok || value.Ref == nil {
				continue
			}
			equals, ok := step.With["equals"]
			if !ok || equals.Literal != "complete" {
				continue
			}
			// steps.<loop>.output.last.state.status
			for _, loop := range loops {
				if strings.HasPrefix(value.Ref.Path, "steps."+loop+".output") && strings.HasSuffix(value.Ref.Path, "status") {
					gated = append(gated, loop)
				}
			}
		}
		if len(loops) == 0 {
			t.Errorf("%s has no while loop", name)
			continue
		}
		for _, loop := range loops {
			if !slices.Contains(gated, loop) {
				t.Errorf("%s.%s takes a plan-derived bound but nothing asserts the plan reached complete; exhausting the budget would report success over a pending backlog", name, loop)
			}
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
