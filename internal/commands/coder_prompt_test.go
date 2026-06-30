package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fast-batch loop (RunFastBatch) clears tester/critic feedback and relies on
// each gate's feedback reaching the coder via its own template placeholder. A var
// passed in the vars map but absent from the template is silently dropped by
// prompts.Interpolate, leaving the coder with no signal — which previously made
// lint-gate failures loop without progress. Guard every feedback channel.
var requiredCoderFeedbackPlaceholders = []string{
	"{{TESTER_FEEDBACK}}",
	"{{CRITIC_FEEDBACK}}",
	"{{VERIFIER_FEEDBACK}}",
	"{{LINT_FEEDBACK}}",
}

func TestCoderPromptReferencesAllFeedbackChannels(t *testing.T) {
	embedded, err := defaultAssets.ReadFile("assets/prompts/coder.md")
	if err != nil {
		t.Fatalf("read embedded coder.md: %v", err)
	}
	for _, ph := range requiredCoderFeedbackPlaceholders {
		if !strings.Contains(string(embedded), ph) {
			t.Errorf("embedded coder.md is missing %s; the orchestrator passes this var but Interpolate drops it, so the coder never sees that gate's feedback", ph)
		}
	}
}

// The repo-root prompts/coder.md (used when orquesta-lite runs its own factory)
// must stay in sync with the embedded copy that `orq-lite init` scaffolds, or a
// fix applied to one silently misses the other.
func TestCoderPromptRepoCopyMatchesEmbedded(t *testing.T) {
	embedded, err := defaultAssets.ReadFile("assets/prompts/coder.md")
	if err != nil {
		t.Fatalf("read embedded coder.md: %v", err)
	}
	// internal/commands -> repo root is two levels up.
	repoCopy, err := os.ReadFile(filepath.Join("..", "..", "prompts", "coder.md"))
	if err != nil {
		t.Skipf("repo-root prompts/coder.md not found (%v); skipping sync check", err)
	}
	if string(embedded) != string(repoCopy) {
		t.Errorf("prompts/coder.md and internal/commands/assets/prompts/coder.md have drifted; keep them identical")
	}
}
