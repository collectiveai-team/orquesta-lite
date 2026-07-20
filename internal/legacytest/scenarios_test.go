package legacytest

import "testing"

func TestScenariosHaveStableUniqueNames(t *testing.T) {
	seen := map[string]bool{}
	for _, scenario := range Scenarios {
		if scenario.Name == "" || scenario.Description == "" || scenario.LegacyExpected == "" || scenario.V2Expected == "" {
			t.Fatalf("incomplete scenario: %+v", scenario)
		}
		if seen[scenario.Name] {
			t.Fatalf("duplicate scenario %q", scenario.Name)
		}
		seen[scenario.Name] = true
		if got, ok := ByName(scenario.Name); !ok || got != scenario {
			t.Fatalf("ByName(%q) = %+v, %v", scenario.Name, got, ok)
		}
	}
}
