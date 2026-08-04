package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/doctor"
)

// TestInitScaffoldsEveryGateConfigKey ties the scaffold to the whitelist.
//
// gateConfigKeys is the single source of truth for what a flow may read out of
// team.json, but nothing used to tie it to what `orq-lite init` actually
// writes. The only guard on the fail-fast path validated the pack against
// examples/governed-pack/team.json — a hand-maintained file that happens to
// declare both argv keys — while the embedded template shipped neither. The
// result was an adoption path where init, pack install and doctor all exited 0
// and the first `flow run` failed before creating workflows.db.
//
// Asserting against the embedded asset (not a copy) means the next key added
// to gateConfigKeys fails here until the scaffold grows it too.
func TestInitScaffoldsEveryGateConfigKey(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal(mustReadAsset("assets/team.json"), &document); err != nil {
		t.Fatal(err)
	}
	for _, key := range gateConfigKeys {
		value, ok := document[key]
		if !ok {
			t.Errorf("assets/team.json does not declare %q, so a scaffolded project cannot run any flow that references config.%s", key, key)
			continue
		}
		items, ok := value.([]any)
		if !ok || len(items) == 0 {
			t.Errorf("assets/team.json %q must be a non-empty array of strings, got %#v", key, value)
			continue
		}
		for index, item := range items {
			if _, ok := item.(string); !ok {
				t.Errorf("assets/team.json %q[%d] must be a string, got %T", key, index, item)
			}
		}
	}
}

// TestInitProducesATeamConfigTheGovernedPackCanRun is the end-to-end version of
// the same guarantee, over the exact sequence an adopter follows: init, then
// install the pack, then run a flow. The third command must not be the first
// one to discover that the first one left a hole.
func TestInitProducesATeamConfigTheGovernedPackCanRun(t *testing.T) {
	for _, lang := range []string{"go", "python", "node"} {
		t.Run(lang, func(t *testing.T) {
			project := t.TempDir()
			if err := InitWithOptions(project, InitOptions{Lang: lang}); err != nil {
				t.Fatal(err)
			}
			installGovernedPack(t, project)
			config := loadGateConfig(filepath.Join(project, "team.json"))
			for _, name := range []string{"factory-governed", "task-list", "issue-fix", "review-existing"} {
				compiled, err := compileWorkflowTarget(project, governedFlowRef(name))
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if err = validateConfigReferences(compiled.IR, config); err != nil {
					t.Errorf("a project scaffolded by `orq-lite init --lang %s` cannot start %s: %v", lang, name, err)
				}
			}
		})
	}
}

// TestInitForAnUnknownLanguageLeavesTheGatesVisiblyEmpty pins the one case the
// scaffold genuinely cannot fill in. It must fail loudly rather than ship a Go
// gate into, say, a Rust repo: an empty argv is rejected by the pre-run
// validator, and doctor names it. What must NOT happen is a silent pass.
func TestInitForAnUnknownLanguageLeavesTheGatesVisiblyEmpty(t *testing.T) {
	project := t.TempDir()
	if err := InitWithOptions(project, InitOptions{Lang: "rust"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(project, "team.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("an unknown language must still scaffold valid JSON: %v", err)
	}
	for _, key := range gateConfigKeys {
		items, ok := document[key].([]any)
		if !ok {
			t.Fatalf("%s must still be present as an array: %#v", key, document[key])
		}
		if len(items) != 0 {
			t.Errorf("%s = %#v, want empty: the scaffold must not guess a toolchain it could not detect", key, items)
		}
	}

	// And doctor has to say so instead of reporting a clean bill of health.
	var warned []string
	for _, check := range doctor.Run(context.Background(), project) {
		if check.Status != doctor.StatusOK && (check.Name == "lint_argv" || check.Name == "test_argv") {
			warned = append(warned, check.Name)
			if !strings.Contains(check.Detail, "config."+check.Name) {
				t.Errorf("%s detail should name the reference that breaks: %q", check.Name, check.Detail)
			}
		}
	}
	if len(warned) != len(gateConfigKeys) {
		t.Errorf("doctor flagged %v; it must report every unset gate a flow can reference", warned)
	}
}
