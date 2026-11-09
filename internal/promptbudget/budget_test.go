package promptbudget

import (
	"strings"
	"testing"
)

func TestStablePrefixDoesNotDependOnRunContext(t *testing.T) {
	template := "Rules {{SKILLS}} objective {{OBJECTIVE}} memory {{MEMORY}}"
	a, ma := Assemble(template, map[string]string{"SKILLS": "read carefully"}, map[string]string{"OBJECTIVE": "first", "MEMORY": "one"})
	b, mb := Assemble(template, map[string]string{"SKILLS": "read carefully"}, map[string]string{"OBJECTIVE": "second", "MEMORY": "two"})
	if ma.PrefixHash != mb.PrefixHash || a == b {
		t.Fatal("volatile context must change suffix but not prefix")
	}
	if strings.Index(a, "read carefully") > strings.Index(a, "<run_context>") {
		t.Fatal("stable instructions follow volatile context")
	}
}
func TestBudgetAndBaselineAreIndependent(t *testing.T) {
	p := Policy{MaxTokens: 200, BaselineTokens: 100, MaxGrowthPercent: 10}
	if p.Check(Measurement{Tokens: 111}) == nil {
		t.Fatal("unjustified growth accepted")
	}
	p.Justification = "approved additional tool policy"
	if err := p.Check(Measurement{Tokens: 111}); err != nil {
		t.Fatal(err)
	}
	if p.Check(Measurement{Tokens: 201}) == nil {
		t.Fatal("justification bypassed hard budget")
	}
}

func TestUnreferencedStableContextIsIncludedAndMeasured(t *testing.T) {
	prompt, measurement := Assemble("Review carefully", map[string]string{"CONVENTIONS": "Use explicit error handling"}, map[string]string{"OBJECTIVE": "fix regression"})
	if !strings.Contains(prompt, "Use explicit error handling") {
		t.Fatal("unreferenced conventions dropped")
	}
	if strings.Index(prompt, "Use explicit error handling") > strings.Index(prompt, "<run_context>") {
		t.Fatal("stable context follows volatile state")
	}
	if measurement.Tokens <= len("Review carefully") {
		t.Fatal("conventions omitted from budget measurement")
	}
}
