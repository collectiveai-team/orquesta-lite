package review

import "testing"

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

func TestAggregateMergesDuplicateEvidenceAndRejectsConflict(t *testing.T) {
	left := blockingFinding("BUG-1")
	right := left
	right.Evidence = []string{"second reproduction"}
	right.Origins = []string{"adversary"}
	findings, ready, err := Aggregate([]Result{completeReview(left), completeReview(right)})
	if err != nil || !ready || len(findings) != 1 || len(findings[0].Evidence) != 2 || len(findings[0].Origins) != 2 {
		t.Fatalf("ready=%v findings=%+v err=%v", ready, findings, err)
	}
	right.Severity = "medium"
	if _, _, err := Aggregate([]Result{completeReview(left), completeReview(right)}); err == nil {
		t.Fatal("conflicting duplicate finding was accepted")
	}
}

func TestPreserveRejectsDroppedBlockingFinding(t *testing.T) {
	prior := completeReview(blockingFinding("BUG-1"))
	if err := Preserve(completeReview(), []Result{prior}); err == nil {
		t.Fatal("blocking finding disappeared")
	}
}
