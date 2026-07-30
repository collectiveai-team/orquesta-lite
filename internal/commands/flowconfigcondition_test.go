package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// conditionGatedFlow puts the config reference in a step's `if` expression
// instead of its `with` block. Both are references the runtime resolves through
// the same `config.` namespace root.
const conditionGatedFlow = `{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"condgated","version":"1"},"steps":[{"id":"noop","uses":"activity:gate.run@1","if":"config.lint_argv != null","with":{"argv":["/usr/bin/true"]}}]}`

// TestFlowRunFailsFastOnConfigReferenceInsideACondition covers the half of the
// `config.` namespace the startup validator cannot see.
//
// validateConfigReferences walks IR.ReferencePaths(), which only visits Value
// fields (step.With, foreach.items, while.initial/maxIterations, outputs). A
// step's `if` and a while's `condition` are expression strings evaluated by
// flow.EvalBool against the very same resolver, and nothing validates their
// reference paths — not the compiler (compileDocument never calls
// validateReference on step.If or step.While.Condition) and not the startup
// pass. So a flow whose gate is guarded by `config.<key>` compiles, passes the
// fail-fast check, starts a run, and only then dies on the unresolvable
// reference.
//
// That is precisely the failure mode §E.2 of the design exists to prevent: "un
// config. roto que explota a los 20 minutos de run es inaceptable". Here the
// run is created and burns whatever preceded the guarded step first.
func TestFlowRunFailsFastOnConfigReferenceInsideACondition(t *testing.T) {
	dir := t.TempDir()
	installFixturePack(t, dir, "condgates", "1", map[string]string{"flows/condgated@1.json": conditionGatedFlow})
	// team.json declares test_argv but not the lint_argv the condition needs.
	writeTeamJSON(t, dir, map[string]any{"test_argv": []string{"/usr/bin/true"}})

	var out bytes.Buffer
	err := FlowCLI(context.Background(), dir, []string{"run", "condgates/condgated@1"}, &out)
	if err == nil {
		t.Fatalf("an unsatisfiable config reference must not succeed: out=%s", out.String())
	}
	if !strings.Contains(err.Error(), `does not declare "lint_argv"`) {
		t.Errorf("err=%v, want the startup validator's message naming lint_argv", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".orquestalite", "workflows.db")); statErr == nil {
		t.Errorf("a run was created despite an unsatisfiable config reference in a condition")
	}
}
