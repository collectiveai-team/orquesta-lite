package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// inputRef finds every `inputs.x` reference anywhere in a document, including
// the ones nested inside prompt context objects and `if` expressions.
var inputRef = regexp.MustCompile(`inputs\.([A-Za-z_][A-Za-z0-9_]*)`)

type packDoc struct {
	Metadata struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"metadata"`
	Inputs map[string]struct {
		Default *json.RawMessage `json:"default"`
	} `json:"inputs"`
	Steps []struct {
		ID   string          `json:"id"`
		Uses string          `json:"uses"`
		With json.RawMessage `json:"with"`
	} `json:"steps"`
}

// TestEverySubflowCallSiteBindsTheInputsItsStepsRead pins the defect that killed
// a factory-governed@2 run 32 minutes in, against the pack that actually ships.
//
// develop-ticket@1 declares commit_type with a default and commit_ticket reads
// `inputs.commit_type`, but factory-governed@2 and task-list@1 never passed it.
// `flow validate` was right to stay green — the reference is legal — and the run
// died at the step that read it, long after the call site that omitted it. The
// engine now binds declared defaults; this test is the pack-level statement of
// the property that fix buys, so a new subflow input cannot reintroduce it.
func TestEverySubflowCallSiteBindsTheInputsItsStepsRead(t *testing.T) {
	packRoot := builtinPackTestRoot()

	subflows := map[string]packDoc{}
	entries, err := os.ReadDir(filepath.Join(packRoot, "subflows"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		doc := readPackDoc(t, filepath.Join(packRoot, "subflows", entry.Name()))
		subflows["subflow:"+doc.Metadata.Name+"@"+doc.Metadata.Version] = doc
	}

	for _, dir := range []string{"flows", "subflows"} {
		files, readErr := os.ReadDir(filepath.Join(packRoot, dir))
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, file := range files {
			caller := readPackDoc(t, filepath.Join(packRoot, dir, file.Name()))
			for _, step := range caller.Steps {
				if !strings.HasPrefix(step.Uses, "subflow:") {
					continue
				}
				sub, ok := subflows[step.Uses]
				if !ok {
					t.Errorf("%s step %s calls %s, which the pack does not ship", file.Name(), step.ID, step.Uses)
					continue
				}
				var with map[string]json.RawMessage
				if len(step.With) > 0 {
					if err = json.Unmarshal(step.With, &with); err != nil {
						t.Fatalf("%s step %s with: %v", file.Name(), step.ID, err)
					}
				}
				// An input is available inside the subflow when the call site
				// passes it or the subflow declares a default the engine binds.
				bound := map[string]bool{}
				for name := range with {
					bound[name] = true
				}
				for name, spec := range sub.Inputs {
					if spec.Default != nil {
						bound[name] = true
					}
				}
				raw, _ := os.ReadFile(filepath.Join(packRoot, "subflows", sub.Metadata.Name+"@"+sub.Metadata.Version+".json"))
				reported := map[string]bool{}
				for _, match := range inputRef.FindAllStringSubmatch(string(raw), -1) {
					if bound[match[1]] || reported[match[1]] {
						continue
					}
					reported[match[1]] = true
					t.Errorf("%s step %s calls %s, whose steps read inputs.%s — not passed and not defaulted", file.Name(), step.ID, step.Uses, match[1])
				}
			}
		}
	}
}

// builtinPackTestRoot locates the shipped development pack from inside the
// package under test. Every test that reads the real pack goes through here:
// the path lived in five separate literals until the pack moved out of
// examples/, and a grep for the old directory missed most of them.
func builtinPackTestRoot() string {
	return filepath.Join("..", "..", "packs", "development", "pack")
}

func readPackDoc(t *testing.T, path string) packDoc {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc packDoc
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return doc
}
