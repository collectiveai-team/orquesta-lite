package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The ticket loop must commit each green ticket, and must do so in a shape that
// survives this engine's two sharp edges: a skipped step resolves to nil, and
// `&&` does not short-circuit, so a guard on a step that may not have run kills
// the run instead of skipping it.
func loadDevelopTicket(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "examples", "governed-pack", "pack", "subflows", "develop-ticket@1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func developTicketSteps(t *testing.T) map[string]map[string]any {
	t.Helper()
	steps := map[string]map[string]any{}
	raw, ok := loadDevelopTicket(t)["steps"].([]any)
	if !ok {
		t.Fatal("develop-ticket has no steps")
	}
	for _, entry := range raw {
		step, _ := entry.(map[string]any)
		id, _ := step["id"].(string)
		steps[id] = step
	}
	return steps
}

func TestDevelopTicketCommitsAfterTheGatesPass(t *testing.T) {
	steps := developTicketSteps(t)
	commit, ok := steps["commit_ticket"]
	if !ok {
		t.Fatal("the ticket loop must commit the ticket it just verified")
	}
	if commit["uses"] != "activity:git.commit@1" {
		t.Fatalf("want git.commit@1, got %v", commit["uses"])
	}
	order := stepOrder(t)
	if order["commit_ticket"] < order["ticket_tests"] {
		t.Fatal("the commit must come after the test gate, not before it")
	}
}

func stepOrder(t *testing.T) map[string]int {
	t.Helper()
	order := map[string]int{}
	raw, _ := loadDevelopTicket(t)["steps"].([]any)
	for index, entry := range raw {
		step, _ := entry.(map[string]any)
		id, _ := step["id"].(string)
		order[id] = index
	}
	return order
}

func TestCommitStepCarriesApprovalAsDataNotAsACondition(t *testing.T) {
	// If commit_ticket were skipped by an `if`, the steps guarded on its output
	// would reference a nil step and fail to resolve.
	commit := developTicketSteps(t)["commit_ticket"]
	if _, hasIf := commit["if"]; hasIf {
		t.Fatal("commit_ticket must run unconditionally so later refs resolve")
	}
	with, _ := commit["with"].(map[string]any)
	if _, ok := with["enabled"]; !ok {
		t.Fatal("commit_ticket must receive the QA verdict as its `enabled` input")
	}
}

func TestABlockedCommitReachesTheCoder(t *testing.T) {
	repair, ok := developTicketSteps(t)["repair_commit"]
	if !ok {
		t.Fatal("a refused commit must be handed to an agent that can fix it")
	}
	if repair["uses"] != "activity:agent.invoke@1" {
		t.Fatalf("want agent.invoke@1, got %v", repair["uses"])
	}
	with, _ := repair["with"].(map[string]any)
	if with["role"] != "coder" {
		t.Fatalf("repair must reuse an existing role; a new one breaks every project's team.json, got %v", with["role"])
	}
	context, _ := with["context"].(map[string]any)
	if _, ok := context["COMMIT_FAILURE"]; !ok {
		t.Fatal("the coder must be given the hook output it is expected to fix")
	}
}

func TestAStillBlockedCommitFailsTheRun(t *testing.T) {
	// A reproduced failure needs a blocking gate. Without one the loop keeps
	// stacking tickets onto a tree that cannot commit, and signs off in prose.
	retry, ok := developTicketSteps(t)["commit_retry"]
	if !ok {
		t.Fatal("the repair must be followed by a retry that is the last word")
	}
	with, _ := retry["with"].(map[string]any)
	if with["requireCommitted"] != true {
		t.Fatal("commit_retry must set requireCommitted so a still-red hook stops the run")
	}
}

func TestCoderPromptDocumentsTheCommitFailureInput(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "examples", "governed-pack", "pack", "prompts", "coder.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(raw), "{{COMMIT_FAILURE}}") {
		t.Fatal("coder.md must interpolate COMMIT_FAILURE or the repair pass arrives with no findings")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// promptGlobals are supplied by the invoker for every role, so a prompt may use
// them without any step wiring them.
var promptGlobals = map[string]bool{"MEMORY": true, "CONVENTIONS": true, "SKILLS": true, "RESULT_PATH": true}

// knownPromptGaps pins the unsupplied placeholders that already existed when
// this check was written, so the check can be strict about everything else.
//
// Each of these is a real defect: the agent receives the literal text
// "{{QA_REVIEW}}" where its input belongs. They are recorded rather than fixed
// because deciding what each shared review block should receive — an empty
// string, a real review, or no block at all in that prompt — is a question
// about the review pipeline's design, not about the wiring. Shrink this map;
// never grow it.
var knownPromptGaps = map[string]bool{
	"fast-batch@1.json|batch_qa|ADVERSARY_REVIEW":            true,
	"fast-batch@1.json|batch_qa|CRITIC_REVIEW":               true,
	"fast-batch@1.json|batch_qa|QA_REVIEW":                   true,
	"fast-batch@1.json|batch_qa|VISUAL_REVIEW":               true,
	"governance-cycle@1.json|repair|FEEDBACK":                true,
	"integrated-review@1.json|adversary|ADVERSARY_REVIEW":    true,
	"integrated-review@1.json|adversary|CRITIC_REVIEW":       true,
	"integrated-review@1.json|adversary|VISUAL_REVIEW":       true,
	"integrated-review@1.json|critic|CRITIC_REVIEW":          true,
	"integrated-review@1.json|critic|VISUAL_REVIEW":          true,
	"integrated-review@1.json|qa|ADVERSARY_REVIEW":           true,
	"integrated-review@1.json|qa|CRITIC_REVIEW":              true,
	"integrated-review@1.json|qa|QA_REVIEW":                  true,
	"integrated-review@1.json|qa|VISUAL_REVIEW":              true,
	"integrated-review@1.json|visual_verifier|VISUAL_REVIEW": true,
	"pr-review@1.json|review|ADVERSARY_REVIEW":               true,
	"pr-review@1.json|review|CRITIC_REVIEW":                  true,
	"pr-review@1.json|review|FEATURES_PATH":                  true,
	"pr-review@1.json|review|QA_REVIEW":                      true,
	"pr-review@1.json|review|VISUAL_REVIEW":                  true,
}

// Interpolate leaves an unsupplied {{VAR}} in the prompt verbatim. An agent then
// reads a literal placeholder where its instructions should be — the failure
// mode where a role is wired but never actually receives its findings.
func TestEveryPromptPlaceholderIsSuppliedByEveryStepThatUsesTheRole(t *testing.T) {
	packRoot := filepath.Join("..", "..", "examples", "governed-pack", "pack")
	placeholder := regexp.MustCompile(`\{\{([A-Z0-9_]+)\}\}`)

	prompts := map[string][]string{}
	entries, err := os.ReadDir(filepath.Join(packRoot, "prompts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		body, readErr := os.ReadFile(filepath.Join(packRoot, "prompts", entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		var needed []string
		for _, match := range placeholder.FindAllStringSubmatch(string(body), -1) {
			if !promptGlobals[match[1]] {
				needed = append(needed, match[1])
			}
		}
		prompts[entry.Name()] = needed
	}

	roles := map[string]string{}
	teamRaw, err := os.ReadFile(filepath.Join("assets", "team.json"))
	if err != nil {
		t.Fatal(err)
	}
	var team struct {
		Roles map[string]struct {
			Prompt string `json:"prompt"`
		} `json:"roles"`
	}
	if err = json.Unmarshal(teamRaw, &team); err != nil {
		t.Fatal(err)
	}
	for name, role := range team.Roles {
		roles[name] = filepath.Base(role.Prompt)
	}

	for _, dir := range []string{"flows", "subflows"} {
		files, readErr := os.ReadDir(filepath.Join(packRoot, dir))
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, file := range files {
			path := filepath.Join(packRoot, dir, file.Name())
			raw, _ := os.ReadFile(path)
			var doc struct {
				Steps []struct {
					ID   string `json:"id"`
					With struct {
						Role    string         `json:"role"`
						Vars    map[string]any `json:"vars"`
						Context map[string]any `json:"context"`
					} `json:"with"`
				} `json:"steps"`
			}
			if err = json.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			for _, step := range doc.Steps {
				if step.With.Role == "" {
					continue
				}
				prompt, ok := roles[step.With.Role]
				if !ok {
					t.Errorf("%s step %s uses role %q, which team.json does not define", file.Name(), step.ID, step.With.Role)
					continue
				}
				for _, name := range prompts[prompt] {
					_, inVars := step.With.Vars[name]
					_, inContext := step.With.Context[name]
					if knownPromptGaps[file.Name()+"|"+step.ID+"|"+name] {
						continue
					}
					if !inVars && !inContext {
						t.Errorf("%s step %s (role %s) never supplies {{%s}}, which %s reads", file.Name(), step.ID, step.With.Role, name, prompt)
					}
				}
			}
		}
	}
}

// Conventional Commits carries meaning in the type. A flow whose whole purpose
// is repairing a reported issue must not label its commits as features.
func TestIssueFixCommitsAsAFix(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "examples", "governed-pack", "pack", "flows", "issue-fix@1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Steps []struct {
			ID   string         `json:"id"`
			Uses string         `json:"uses"`
			With map[string]any `json:"with"`
		} `json:"steps"`
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, step := range doc.Steps {
		if step.Uses != "subflow:develop-ticket@1" {
			continue
		}
		if step.With["commit_type"] != "fix" {
			t.Fatalf("issue-fix step %s must pass commit_type=fix, got %v", step.ID, step.With["commit_type"])
		}
		return
	}
	t.Fatal("issue-fix no longer calls develop-ticket")
}
