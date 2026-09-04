package commands

import (
	"encoding/json"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/cost"
)

// TestScaffoldedTeamModelsArePriceable ties the scaffolded team.json to the
// embedded price table. Changing a default model used to be a two-file change
// that nothing enforced: when init started defaulting to gpt-5.6-sol and
// gpt-5.6-terra, neither had a price, so `orq-lite cost` reported nothing and
// - worse - runSpendUSD contributed 0 to the workflow cost budget, leaving the
// pack policy's maxCostUSD unable to trip on any run that fell back to Codex.
func TestScaffoldedTeamModelsArePriceable(t *testing.T) {
	var team struct {
		Agents map[string]struct {
			Model string `json:"model"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(mustReadAsset("assets/team.json"), &team); err != nil {
		t.Fatalf("scaffolded team.json does not parse: %v", err)
	}
	if len(team.Agents) == 0 {
		t.Fatal("scaffolded team.json declares no agents")
	}
	for name, agent := range team.Agents {
		if agent.Model == "" {
			continue // the provider default is priced under its own name
		}
		if _, ok := cost.EstimateUSD(agent.Model, 1_000_000, 1_000_000); !ok {
			t.Errorf("agent %q defaults to model %q, which has no embedded price: "+
				"every run using it reports $0 and cannot exhaust a cost budget",
				name, agent.Model)
		}
	}
}
