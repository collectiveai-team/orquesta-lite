package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gh infers the PR head from the branch's remote-tracking ref. A clone whose
// fetch refspec does not cover the branch (a --single-branch clone, a CI
// checkout) has no such ref even after `git push -u`, and gh aborts with "you
// must first push the current branch" over a branch that is already pushed.
// Every PR step therefore names its head, and is preceded by a push.
func TestEveryPullRequestStepNamesItsHeadAfterAPush(t *testing.T) {
	flows, err := filepath.Glob(filepath.Join(governedPackRoot(t), "flows", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, path := range flows {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Steps []struct {
				ID   string `json:"id"`
				With struct {
					Argv json.RawMessage `json:"argv"`
				} `json:"with"`
			} `json:"steps"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		pushed := false
		for _, step := range doc.Steps {
			// argv may be a $ref (gate steps read config.lint_argv); only a
			// literal list can be a push or a PR command.
			var argv []string
			_ = json.Unmarshal(step.With.Argv, &argv)
			command := strings.Join(argv, " ")
			if strings.Contains(command, "git push") {
				pushed = true
			}
			if !strings.Contains(command, "pr create") {
				continue
			}
			found++
			if !strings.Contains(command, "--head") {
				t.Errorf("%s step %s runs %q without --head", filepath.Base(path), step.ID, command)
			}
			if !pushed {
				t.Errorf("%s step %s opens a PR before any git push step", filepath.Base(path), step.ID)
			}
		}
	}
	if found == 0 {
		t.Fatal("no PR step found; the glob or the step shape changed")
	}
}
