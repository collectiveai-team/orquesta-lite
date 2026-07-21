package commands

import (
	"path/filepath"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/flow"
)

// The governed-pack example must always satisfy the cutover gate's required
// development flows: they must exist, verify against pack.json, and compile.
func TestGovernedPackRequiredFlowsCompile(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "examples", "governed-pack", "pack"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flow.LoadPack(root); err != nil {
		t.Fatalf("pack.json digests are stale — run examples/governed-pack/regen-digests.py: %v", err)
	}
	catalog := flow.NewDirectoryCatalog(root, builtinSpecs())
	for _, name := range []string{"factory-fast", "factory-governed", "issue-fix", "plan-tickets", "pr-review", "task-list"} {
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
