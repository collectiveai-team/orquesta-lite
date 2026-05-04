package loops

import (
	"context"
	"testing"
)

type stubRoles struct {
	coder  func(attempt int, testerFB, criticFB string) CoderOutcome
	tester func(attempt int) TesterOutcome
	critic func(attempt int) CriticOutcome
}

func (s *stubRoles) RunCoder(ctx context.Context, attempt int, testerFB, criticFB string) (CoderOutcome, error) {
	return s.coder(attempt, testerFB, criticFB), nil
}
func (s *stubRoles) RunTester(ctx context.Context, attempt int) (TesterOutcome, error) {
	return s.tester(attempt), nil
}
func (s *stubRoles) RunCritic(ctx context.Context, attempt int) (CriticOutcome, error) {
	return s.critic(attempt), nil
}

func TestFix_PassFirstTry(t *testing.T) {
	r := &stubRoles{
		coder:  func(int, string, string) CoderOutcome { return CoderOutcome{Status: "completed"} },
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
		coder: func(int, string, string) CoderOutcome { return CoderOutcome{Status: "completed"} },
		tester: func(a int) TesterOutcome {
			if a < 3 {
				return TesterOutcome{Status: "fail", FailuresHash: "h1"}
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
		coder:  func(int, string, string) CoderOutcome { return CoderOutcome{Status: "completed"} },
		tester: func(int) TesterOutcome { return TesterOutcome{Status: "fail", FailuresHash: "h"} },
		critic: func(int) CriticOutcome { return CriticOutcome{Status: "approved"} },
	}
	out, _ := RunFix(context.Background(), FixConfig{MaxIterations: 3}, r)
	if out.Status != FixFailed || out.Reason != "max_iterations" {
		t.Errorf("got %+v", out)
	}
	if out.Iterations != 3 {
		t.Errorf("iter=%d", out.Iterations)
	}
}

func TestFix_DetectsAgentRepeatedFailure(t *testing.T) {
	r := &stubRoles{
		coder:  func(int, string, string) CoderOutcome { return CoderOutcome{Status: "completed"} },
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
		coder:  func(int, string, string) CoderOutcome { return CoderOutcome{Status: "completed"} },
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
