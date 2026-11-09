package review

import (
	"strings"
	"testing"
)

func claims3() ClaimsV3 {
	return ClaimsV3{Version: 3, Lens: "reliability", Status: "complete", Summary: "reviewed", ReviewedRevision: "tree", Findings: []ClaimV3{{Category: "runtime_check", Severity: "medium", State: "open", Location: "foo.go:2", Trigger: "unit", Impact: "crashes", ProofRefs: []string{"proof"}, Causal: "introduced"}}, Limitations: []string{}, Evidence: []string{}}
}
func resolver3(causal string) ProofResolver {
	return func(string) (Proof, error) {
		return Proof{Revision: "tree", BaseRevision: "base", Kind: "check_comparison", Subject: "unit", Causal: causal, Verified: true}, nil
	}
}

func TestV3ComparisonCannotAttributeUnrelatedClaim(t *testing.T) {
	for _, change := range []func(*ClaimV3){func(f *ClaimV3) { f.Category = "correctness" }, func(f *ClaimV3) { f.Trigger = "another-check" }} {
		c := claims3()
		change(&c.Findings[0])
		result, err := EvaluateV3(c, "tree", "base", resolver3("introduced"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Findings[0].Causal != "unknown" || result.Decision != "inconclusive" {
			t.Fatalf("comparison laundered into unrelated finding: %+v", result)
		}
	}
}

func TestV3CannotResolveFailedRuntimeCheckByNarration(t *testing.T) {
	c := claims3()
	c.Findings[0].State = "resolved"
	c.Findings[0].Resolution = "fixed it"
	if _, err := EvaluateV3(c, "tree", "base", resolver3("introduced"), nil); err == nil {
		t.Fatal("failed comparison accepted as resolution")
	}
	r, err := EvaluateV3(c, "tree", "base", func(string) (Proof, error) {
		return Proof{Revision: "tree", BaseRevision: "base", Kind: "check_comparison", Subject: "unit", CheckPassed: true, Verified: true}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Approved {
		t.Fatalf("passing resolution not accepted: %+v", r)
	}
}

func TestV3PreservesMediumBlockingAndRuntimeIdentity(t *testing.T) {
	c := claims3()
	r, err := EvaluateV3(c, "tree", "base", resolver3("introduced"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Approved || r.Decision != "block" || len(r.Findings) != 1 || !strings.HasPrefix(r.Findings[0].ID, "finding-") || r.Digest == "" {
		t.Fatalf("%+v", r)
	}
	c.Findings[0].Severity = "critical"
	other, err := EvaluateV3(c, "tree", "base", resolver3("introduced"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Findings[0].ID != other.Findings[0].ID {
		t.Fatal("severity changed identity")
	}
}
func TestV3ModelCannotDeclareCausalityWithoutTrustedComparison(t *testing.T) {
	c := claims3()
	r, err := EvaluateV3(c, "tree", "base", resolver3(""), nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Decision != "inconclusive" || len(r.NeedsDecision) != 1 || r.Findings[0].Causal != "unknown" {
		t.Fatalf("%+v", r)
	}
}
func TestV3PreexistingBecomesFollowup(t *testing.T) {
	r, err := EvaluateV3(claims3(), "tree", "base", resolver3("preexisting"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Approved || len(r.Followups) != 1 {
		t.Fatalf("%+v", r)
	}
}
func TestV3RejectsMemoryAndForeignRevision(t *testing.T) {
	for _, p := range []Proof{{Revision: "tree", Kind: "memory", Verified: true}, {Revision: "other", Kind: "code", Verified: true}, {Revision: "tree", Kind: "code", Verified: false}} {
		_, err := EvaluateV3(claims3(), "tree", "base", func(string) (Proof, error) { return p, nil }, nil)
		if err == nil {
			t.Fatal("invalid proof accepted", p)
		}
	}
}
func TestV3PreservesHistoricalFindings(t *testing.T) {
	c := claims3()
	prior, err := EvaluateV3(c, "tree", "base", resolver3("introduced"), nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Findings = []ClaimV3{}
	if _, err = EvaluateV3(c, "tree", "base", resolver3("introduced"), []ResultV3{prior}); err == nil {
		t.Fatal("lost historical blocker accepted")
	}
}
func TestV3RejectsAgentAssignedIDAndTrailingJSON(t *testing.T) {
	for _, raw := range []string{`{"version":3,"id":"fake"}`, `{"version":3} {}`} {
		if _, err := DecodeClaimsV3([]byte(raw)); err == nil {
			t.Fatal("invalid claims accepted")
		}
	}
}
