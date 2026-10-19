package review

import (
	"strings"
	"testing"
)

func completeReview(findings ...Finding) Result {
	decision := "approve"
	approved := true
	for _, finding := range findings {
		if finding.State != "open" {
			continue
		}
		approved = false
		if finding.Severity == "low" {
			decision = "warn"
		} else {
			decision = "block"
		}
	}
	return Result{
		Version:          2,
		Status:           "complete",
		Decision:         decision,
		Approved:         approved,
		Summary:          "review complete",
		Findings:         append([]Finding{}, findings...),
		Limitations:      []string{},
		ReviewedRevision: "abc123",
		Evidence:         []string{"test evidence"},
	}
}

func blockingFinding(id string) Finding {
	return Finding{
		ID: id, Category: "correctness", Severity: "high", State: "open",
		Location: "x.go:1", Trigger: "specific input", Impact: "wrong result",
		Evidence: []string{"reproduction"}, Origins: []string{"qa"},
	}
}

func TestValidateAcceptsCompleteReviewWithNoFindings(t *testing.T) {
	if err := completeReview().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsIncompleteApproval(t *testing.T) {
	r := completeReview()
	r.Status = "partial"
	r.Limitations = []string{"browser unavailable"}
	if err := r.Validate(); err == nil {
		t.Fatal("partial review was allowed to approve")
	}
}

func TestValidateRejectsStaticSubstituteForRequiredBrowser(t *testing.T) {
	r := Result{
		Version: 2, Status: "partial", Decision: "inconclusive", Approved: false,
		Summary: "static HTML only", Findings: []Finding{},
		Limitations: []string{"browser unavailable"}, Evidence: []string{"curl output"},
		Visual: &Visual{
			Requested: "browser", Executed: "static", Required: []string{"interaction"},
			Checked: []string{}, Pending: []string{"interaction"}, Reason: "no browser",
			Artifacts: []string{"page.html"},
		},
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	r.Status, r.Decision, r.Approved = "complete", "approve", true
	if err := r.Validate(); err == nil {
		t.Fatal("degraded visual review was allowed to approve")
	}
}

func TestAggregateKeepsIncompleteCoverageOutOfRepairs(t *testing.T) {
	partial := Result{
		Version: 2, Status: "unavailable", Decision: "inconclusive", Approved: false,
		Summary: "provider unavailable", Findings: []Finding{},
		Limitations: []string{"provider unavailable"}, Evidence: []string{},
	}
	findings, ready, err := Aggregate([]Result{partial})
	if err != nil {
		t.Fatal(err)
	}
	if ready || len(findings) != 0 {
		t.Fatalf("ready=%v findings=%v", ready, findings)
	}
}

func TestAggregateMergesDuplicateEvidence(t *testing.T) {
	left := blockingFinding("BUG-1")
	right := left
	right.Evidence = []string{"second reproduction"}
	right.Origins = []string{"adversary"}
	findings, ready, err := Aggregate([]Result{completeReview(left), completeReview(right)})
	if err != nil || !ready || len(findings) != 1 || len(findings[0].Evidence) != 2 || len(findings[0].Origins) != 2 {
		t.Fatalf("ready=%v findings=%+v err=%v", ready, findings, err)
	}
}

func TestPreserveRejectsDroppedBlockingFinding(t *testing.T) {
	prior := completeReview(blockingFinding("BUG-1"))
	if err := Preserve(completeReview(), []Result{prior}); err == nil {
		t.Fatal("blocking finding disappeared")
	}
}

// A corrective retry only converges when the agent sees every violation at
// once; reporting the first one made governance fix one detail per attempt and
// exhaust its budget on the next.
func TestValidateReportsEveryViolationWithItsSubject(t *testing.T) {
	missingEvidence := blockingFinding("BUG-2")
	missingEvidence.Evidence = []string{}
	r := completeReview(missingEvidence)
	r.Status, r.Decision, r.Approved = "partial", "block", false
	r.Limitations = []string{"browser flaky"}
	r.Visual = &Visual{
		Requested: "browser", Executed: "agent-browser", Required: []string{"login form", "mobile layout"},
		Checked: []string{"login form"}, Pending: []string{}, Reason: "partial run",
	}
	err := r.Validate()
	if err == nil {
		t.Fatal("invalid review accepted")
	}
	for _, want := range []string{
		`finding "BUG-2"`,
		`visual.executed "agent-browser"`,
		"browser, static, not_applicable, unavailable",
		`criterion "mobile layout"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

func TestPreserveReportsEveryLostEvidenceVerbatim(t *testing.T) {
	old := blockingFinding("BUG-1")
	old.Evidence = []string{"repro one", "repro two"}
	old.Origins = []string{"qa", "adversary"}
	current := blockingFinding("BUG-1")
	current.Evidence = []string{"paraphrased repro"}
	current.Origins = []string{"governance"}
	err := Preserve(completeReview(current), []Result{completeReview(old)})
	if err == nil {
		t.Fatal("lost evidence accepted")
	}
	for _, want := range []string{`"repro one"`, `"repro two"`, `origin "qa"`, `origin "adversary"`, "verbatim"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

// Reviewers that agree on a defect still describe its impact in their own
// words: one quotes the spec, another paraphrases it and cites a file. Reading
// that prose as a conflict failed the run on the strongest signal the harness
// can get — independent reviewers reporting the same defect — and the findings
// never reached the repair loop.
func TestAggregateMergesAgreementThatIsWordedDifferently(t *testing.T) {
	qa := blockingFinding("contract-filters-ignored")
	qa.Impact = "El filtro declarado en el spec se ignora por completo."
	adversary := blockingFinding("contract-filters-ignored")
	adversary.Impact = "The status filter required by the spec is ignored (services/insights.py:18)."
	adversary.Origins = []string{"adversary"}
	adversary.Evidence = []string{"reproduced with status=closed"}

	findings, ready, err := Aggregate([]Result{completeReview(qa), completeReview(adversary)})
	if err != nil {
		t.Fatalf("reviewers that agree must not fail the run: %v", err)
	}
	if !ready || len(findings) != 1 {
		t.Fatalf("ready=%v findings=%+v", ready, findings)
	}
	merged := findings[0]
	if len(merged.Origins) != 2 || len(merged.Evidence) < 2 {
		t.Fatalf("merge lost a reviewer's sources: %+v", merged)
	}
	for _, want := range []string{qa.Impact, adversary.Impact} {
		if !strings.Contains(merged.Impact+"\n"+strings.Join(merged.Evidence, "\n"), want) {
			t.Fatalf("lost a reviewer's wording %q: %+v", want, merged)
		}
	}
}

// A real disagreement resolves towards the more alarming claim and stays
// visible, instead of aborting a run whose tickets are already committed.
// Escalating can over-report a defect; it can never drop one.
func TestAggregateEscalatesASeverityDisagreementInsteadOfFailingTheRun(t *testing.T) {
	mild := blockingFinding("BUG-1")
	mild.Severity = "medium"
	severe := blockingFinding("BUG-1")
	severe.Severity = "critical"
	severe.Origins = []string{"adversary"}

	findings, _, err := Aggregate([]Result{completeReview(mild), completeReview(severe)})
	if err != nil {
		t.Fatalf("a severity disagreement must not fail the run: %v", err)
	}
	if len(findings) != 1 || findings[0].Severity != "critical" {
		t.Fatalf("want the worst severity kept, got %+v", findings)
	}
}

func TestAggregateKeepsAFindingOpenWhenReviewersDisagreeOnItsState(t *testing.T) {
	resolved := blockingFinding("BUG-1")
	resolved.State, resolved.Resolution = "resolved", "fixed in abc123"
	stillOpen := blockingFinding("BUG-1")
	stillOpen.Origins = []string{"adversary"}

	findings, _, err := Aggregate([]Result{completeReview(resolved), completeReview(stillOpen)})
	if err != nil {
		t.Fatalf("a state disagreement must not fail the run: %v", err)
	}
	if len(findings) != 1 || findings[0].State != "open" {
		t.Fatalf("an unresolved claim must keep the defect open, got %+v", findings)
	}
}
