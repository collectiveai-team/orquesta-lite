package cost

import "testing"

func TestPricingVersionTracksRatesAndResolutionRules(t *testing.T) {
	first := map[string]price{"b": {InputPerMillion: 2}, "a": {InputPerMillion: 1}}
	reordered := map[string]price{"a": {InputPerMillion: 1}, "b": {InputPerMillion: 2}}
	version := pricingVersion(first, []string{"a", "b"}, "resolver-v1")
	if version != pricingVersion(reordered, []string{"a", "b"}, "resolver-v1") {
		t.Fatal("map insertion order changed pricing identity")
	}
	changed := map[string]price{"a": {InputPerMillion: 1.5}, "b": {InputPerMillion: 2}}
	if version == pricingVersion(changed, []string{"a", "b"}, "resolver-v1") {
		t.Fatal("changed rates retained pricing identity")
	}
	if version == pricingVersion(first, []string{"b", "a"}, "resolver-v1") {
		t.Fatal("changed prefix precedence retained pricing identity")
	}
	if version == pricingVersion(first, []string{"a", "b"}, "resolver-v2") {
		t.Fatal("changed resolver algorithm retained pricing identity")
	}
	if len(PricingVersion()) != len("sha256:")+64 {
		t.Fatal("embedded pricing identity is not SHA-256")
	}
}
