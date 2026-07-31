package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The governed pack's attempt budgets are runaway backstops, not budgets. The
// distinction is not stylistic: an attempt cap below what the ticket loop's own
// declared ceiling can legitimately consume is a covert cap on how many tickets
// a run can finish, and it fails silently — the run reports a budget-exhausted
// error that names attempts, while the actual casualty is the backlog.
//
// That is not hypothetical. `development@2` shipped `maxAgentAttempts: 48`,
// which at 3 agent invocations per ticket stopped a governed run at ~15 tickets
// and cost six relaunches before anyone looked at the policy. The spec's first
// answer — setting both caps to 0 (unlimited) and leaning on `maxCostUSD` —
// traded a badly-sized brake for one that does not work at all, because no
// price entry exists for the models the pack actually runs.
//
// So the numbers are derived, and this test pins the derivation rather than the
// numbers: whatever the pack's iteration ceiling and per-pass agent spend are,
// the backstop must sit above their product. Lower it and this test says why.
func TestGovernedPackAttemptBackstopExceedsItsOwnLoopCeiling(t *testing.T) {
	root := filepath.Join("..", "..", "examples", "governed-pack", "pack")

	// The ceiling the planner's budget can legitimately reach.
	var stateSchema struct {
		Properties struct {
			IterationBudget struct {
				Maximum *int `json:"maximum"`
			} `json:"iteration_budget"`
		} `json:"properties"`
	}
	readPackJSON(t, filepath.Join(root, "schemas", "workflow-state@2.json"), &stateSchema)
	maxPasses := stateSchema.Properties.IterationBudget.Maximum
	if maxPasses == nil || *maxPasses < 1 {
		t.Fatalf("workflow-state@2 must bound iteration_budget with a maximum; got %v", maxPasses)
	}

	// What one pass of the ticket loop costs, counted from the subflow rather
	// than hardcoded, so adding a role to develop-ticket@1 moves this floor.
	var developTicket struct {
		Steps []struct {
			Uses string `json:"uses"`
		} `json:"steps"`
	}
	readPackJSON(t, filepath.Join(root, "subflows", "develop-ticket@1.json"), &developTicket)
	agentsPerPass := 0
	for _, step := range developTicket.Steps {
		if step.Uses == "activity:agent.invoke@1" {
			agentsPerPass++
		}
	}
	if agentsPerPass == 0 {
		t.Fatal("develop-ticket@1 declares no agent steps; the derivation below is meaningless")
	}

	var policy struct {
		MaxAttempts      int `json:"maxAttempts"`
		MaxAgentAttempts int `json:"maxAgentAttempts"`
	}
	readPackJSON(t, filepath.Join(root, "policies", "development@3.json"), &policy)

	floor := *maxPasses * agentsPerPass
	if policy.MaxAgentAttempts != 0 && policy.MaxAgentAttempts < floor {
		t.Errorf("maxAgentAttempts=%d is below %d (%d passes x %d agents): the cap binds before the loop's own bound, so it is a covert ticket cap",
			policy.MaxAgentAttempts, floor, *maxPasses, agentsPerPass)
	}
	if policy.MaxAttempts != 0 && policy.MaxAttempts < policy.MaxAgentAttempts {
		t.Errorf("maxAttempts=%d is below maxAgentAttempts=%d: every agent invocation is also an attempt, so the total budget must be the larger of the two",
			policy.MaxAttempts, policy.MaxAgentAttempts)
	}
}

func readPackJSON(t *testing.T, path string, into any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, into); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}
