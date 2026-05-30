package loops

import (
	"context"
	"fmt"
	"testing"
)

type stubRoles struct {
	coder  func(attempt int, fb CoderFeedback) CoderOutcome
	tester func(attempt int) TesterOutcome
	critic func(attempt int) CriticOutcome
}

func (s *stubRoles) RunCoder(ctx context.Context, attempt int, fb CoderFeedback) (CoderOutcome, error) {
	return s.coder(attempt, fb), nil
}
func (s *stubRoles) RunTester(ctx context.Context, attempt int) (TesterOutcome, error) {
	return s.tester(attempt), nil
}
func (s *stubRoles) RunCritic(ctx context.Context, attempt int) (CriticOutcome, error) {
	return s.critic(attempt), nil
}

func TestFix_PassFirstTry(t *testing.T) {
	r := &stubRoles{
		coder:  func(int, CoderFeedback) CoderOutcome { return CoderOutcome{Status: "completed"} },
		tester: func(int) TesterOutcome { return TesterOutcome{Status: "pass"} },
		critic: func(int) CriticOutcome { return CriticOutcome{Status: "approved"} },
	}
	out, err := RunFix(context.Background(), FixConfig{MaxIterations: 5}, r)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != FixDone || out.Iterations != 1 {
		t.Errorf("got %+v", out)
	}
}

func TestFix_TesterShortCircuitsCritic(t *testing.T) {
	criticCalls := 0
	r := &stubRoles{
		coder: func(int, CoderFeedback) CoderOutcome { return CoderOutcome{Status: "completed"} },
		tester: func(a int) TesterOutcome {
			// Use distinct hashes per attempt to avoid stuck detection.
			if a < 3 {
				return TesterOutcome{Status: "fail", FailuresHash: fmt.Sprintf("h%d", a)}
			}
			return TesterOutcome{Status: "pass"}
		},
		critic: func(int) CriticOutcome { criticCalls++; return CriticOutcome{Status: "approved"} },
	}
	out, _ := RunFix(context.Background(), FixConfig{MaxIterations: 5}, r)
	if out.Status != FixDone {
		t.Errorf("status=%v", out.Status)
	}
	if criticCalls != 1 {
		t.Errorf("critic should run once (after tester finally passed), got %d", criticCalls)
	}
}

func TestFix_HitsMaxIterations(t *testing.T) {
	r := &stubRoles{
		coder:  func(int, CoderFeedback) CoderOutcome { return CoderOutcome{Status: "completed"} },
		tester: func(int) TesterOutcome { return TesterOutcome{Status: "fail", FailuresHash: "h"} },
		critic: func(int) CriticOutcome { return CriticOutcome{Status: "approved"} },
	}
	// MaxIterations=2: the max_iterations check fires on attempt 2 (attempt >= MaxIterations),
	// which takes priority over stuck detection in the same iteration.
	out, _ := RunFix(context.Background(), FixConfig{MaxIterations: 2}, r)
	if out.Status != FixFailed || out.Reason != "max_iterations" {
		t.Errorf("got %+v", out)
	}
	if out.Iterations != 2 {
		t.Errorf("iter=%d", out.Iterations)
	}
}

func TestFix_DetectsAgentRepeatedFailure(t *testing.T) {
	r := &stubRoles{
		coder:  func(int, CoderFeedback) CoderOutcome { return CoderOutcome{Status: "completed"} },
		tester: func(int) TesterOutcome { return TesterOutcome{Status: "fail", FailuresHash: "same"} },
		critic: func(int) CriticOutcome { return CriticOutcome{Status: "approved"} },
	}
	out, _ := RunFix(context.Background(), FixConfig{MaxIterations: 10}, r)
	if out.Reason != "agent_repeated_failure" {
		t.Errorf("expected agent_repeated_failure, got %q", out.Reason)
	}
	// Should detect on the second identical failure (iteration 2), so fewer than 10 iterations.
	if out.Iterations > 3 {
		t.Errorf("repeated-failure detection too slow: %d", out.Iterations)
	}
}

func TestFix_CriticVetoesAndReruns(t *testing.T) {
	criticCalls := 0
	r := &stubRoles{
		coder:  func(int, CoderFeedback) CoderOutcome { return CoderOutcome{Status: "completed"} },
		tester: func(int) TesterOutcome { return TesterOutcome{Status: "pass"} },
		critic: func(int) CriticOutcome {
			criticCalls++
			if criticCalls < 2 {
				return CriticOutcome{Status: "rejected", Feedback: "bad"}
			}
			return CriticOutcome{Status: "approved"}
		},
	}
	out, _ := RunFix(context.Background(), FixConfig{MaxIterations: 5}, r)
	if out.Status != FixDone || out.Iterations != 2 {
		t.Errorf("got %+v", out)
	}
}

// TestRunFix_PassesEnrichedFeedback asserts that on attempt 2, the feedback
// struct passed to RunCoder contains the summary from attempt 1 and the
// files-changed list.
func TestRunFix_PassesEnrichedFeedback(t *testing.T) {
	var capturedFB CoderFeedback
	attempt1Summary := "did some work"
	attempt1Files := []string{"a.go", "b.go"}

	r := &stubRoles{
		coder: func(attempt int, fb CoderFeedback) CoderOutcome {
			if attempt == 2 {
				capturedFB = fb
			}
			return CoderOutcome{
				Status:       "completed",
				Summary:      attempt1Summary,
				FilesChanged: attempt1Files,
			}
		},
		tester: func(attempt int) TesterOutcome {
			// Fail on attempt 1 with a unique hash, pass on attempt 2.
			if attempt == 1 {
				return TesterOutcome{Status: "fail", FailuresHash: "hash1", Feedback: "fix this"}
			}
			return TesterOutcome{Status: "pass"}
		},
		critic: func(int) CriticOutcome { return CriticOutcome{Status: "approved"} },
	}

	out, err := RunFix(context.Background(), FixConfig{MaxIterations: 5}, r)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != FixDone {
		t.Errorf("expected FixDone, got %+v", out)
	}

	// On attempt 2, PreviousAttemptSummary must equal the summary from attempt 1.
	if capturedFB.PreviousAttemptSummary != attempt1Summary {
		t.Errorf("PreviousAttemptSummary = %q, want %q", capturedFB.PreviousAttemptSummary, attempt1Summary)
	}

	// FilesChangedSoFar must include the files from attempt 1.
	if len(capturedFB.FilesChangedSoFar) != len(attempt1Files) {
		t.Errorf("FilesChangedSoFar len = %d, want %d", len(capturedFB.FilesChangedSoFar), len(attempt1Files))
	}
	for i, f := range attempt1Files {
		if capturedFB.FilesChangedSoFar[i] != f {
			t.Errorf("FilesChangedSoFar[%d] = %q, want %q", i, capturedFB.FilesChangedSoFar[i], f)
		}
	}
}

// TestRunFix_StuckTriggersEscalation verifies that after 2 consecutive identical
// failure hashes, the next RunCoder call has fb.AgentOverride == "claude_opus".
func TestRunFix_StuckTriggersEscalation(t *testing.T) {
	var overrideOnAttempt3 string

	r := &stubRoles{
		coder: func(attempt int, fb CoderFeedback) CoderOutcome {
			if attempt == 3 {
				overrideOnAttempt3 = fb.AgentOverride
			}
			return CoderOutcome{Status: "completed"}
		},
		tester: func(attempt int) TesterOutcome {
			// Attempts 1 and 2 produce identical hashes (stuck).
			// Attempt 3 (after escalation) passes.
			if attempt <= 2 {
				return TesterOutcome{Status: "fail", FailuresHash: "stuck_hash", Feedback: "still broken"}
			}
			return TesterOutcome{Status: "pass"}
		},
		critic: func(int) CriticOutcome { return CriticOutcome{Status: "approved"} },
	}

	cfg := FixConfig{
		MaxIterations:    10,
		EscalationLadder: []string{"claude_opus"},
	}
	out, err := RunFix(context.Background(), cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != FixDone {
		t.Errorf("expected FixDone after escalation, got %+v", out)
	}
	if overrideOnAttempt3 != "claude_opus" {
		t.Errorf("attempt 3 AgentOverride = %q, want %q", overrideOnAttempt3, "claude_opus")
	}
}

// TestRunFix_EscalationExhaustedReturnsFailed verifies that when the escalation
// ladder is exhausted (single entry) and stuck happens again after the escalated
// attempt, RunFix returns FixFailed with Reason="agent_repeated_failure".
func TestRunFix_EscalationExhaustedReturnsFailed(t *testing.T) {
	r := &stubRoles{
		coder: func(int, CoderFeedback) CoderOutcome { return CoderOutcome{Status: "completed"} },
		tester: func(int) TesterOutcome {
			// Always fail with the same hash — stuck forever.
			return TesterOutcome{Status: "fail", FailuresHash: "forever_stuck", Feedback: "nope"}
		},
		critic: func(int) CriticOutcome { return CriticOutcome{Status: "approved"} },
	}

	cfg := FixConfig{
		MaxIterations:    20,
		EscalationLadder: []string{"claude_opus"},
	}
	out, err := RunFix(context.Background(), cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != FixFailed {
		t.Errorf("expected FixFailed, got status=%v", out.Status)
	}
	if out.Reason != "agent_repeated_failure" {
		t.Errorf("reason = %q, want %q", out.Reason, "agent_repeated_failure")
	}
	// With threshold=2 and one escalation slot:
	// attempts 1,2 → stuck → escalate (idx=0→1), reset window.
	// attempts 3,4 → stuck again → ladder exhausted → fail.
	// So should fail at iteration 4.
	if out.Iterations > 5 {
		t.Errorf("expected fast failure after exhausted escalation, got iter=%d", out.Iterations)
	}
}
