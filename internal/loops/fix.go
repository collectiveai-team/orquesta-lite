package loops

import (
	"context"
)

type CoderOutcome struct {
	Status  string // "completed" | "blocked"
	Summary string
}

type TesterOutcome struct {
	Status       string // "pass" | "fail"
	Feedback     string // injected back to coder
	FailuresHash string // for repeated-failure detection
}

type CriticOutcome struct {
	Status   string // "approved" | "rejected"
	Feedback string
}

type RoleRunner interface {
	RunCoder(ctx context.Context, attempt int, testerFB, criticFB string) (CoderOutcome, error)
	RunTester(ctx context.Context, attempt int) (TesterOutcome, error)
	RunCritic(ctx context.Context, attempt int) (CriticOutcome, error)
}

type FixStatus int

const (
	FixDone   FixStatus = iota
	FixFailed FixStatus = iota
)

type FixConfig struct {
	MaxIterations int
}

type FixResult struct {
	Status       FixStatus
	Reason       string // "max_iterations" | "agent_repeated_failure" | "" when Done
	Iterations   int
	LastFeedback string
}

func RunFix(ctx context.Context, cfg FixConfig, r RoleRunner) (*FixResult, error) {
	var testerFB, criticFB string
	var prevHash string
	sameHashCount := 0

	for attempt := 1; attempt <= cfg.MaxIterations; attempt++ {
		if _, err := r.RunCoder(ctx, attempt, testerFB, criticFB); err != nil {
			return nil, err
		}

		t, err := r.RunTester(ctx, attempt)
		if err != nil {
			return nil, err
		}
		if t.Status == "fail" {
			// max_iterations check takes priority when the iteration budget is exhausted.
			if attempt >= cfg.MaxIterations {
				return &FixResult{
					Status:       FixFailed,
					Reason:       "max_iterations",
					Iterations:   cfg.MaxIterations,
					LastFeedback: t.Feedback,
				}, nil
			}

			// Repeated-failure detection: track consecutive identical FailuresHash values.
			// Fire after 3 consecutive identical failures (coder given feedback but still stuck).
			if t.FailuresHash != "" && t.FailuresHash == prevHash {
				sameHashCount++
			} else {
				sameHashCount = 1
				prevHash = t.FailuresHash
			}
			if sameHashCount >= 3 {
				return &FixResult{
					Status:       FixFailed,
					Reason:       "agent_repeated_failure",
					Iterations:   attempt,
					LastFeedback: t.Feedback,
				}, nil
			}

			testerFB = t.Feedback
			criticFB = ""
			continue
		}

		// Tester passed — run critic.
		c, err := r.RunCritic(ctx, attempt)
		if err != nil {
			return nil, err
		}
		if c.Status == "approved" {
			return &FixResult{Status: FixDone, Iterations: attempt}, nil
		}
		// Critic rejected: feed critic feedback back to coder on next attempt.
		// Reset hash tracking so a future tester failure starts fresh.
		criticFB = c.Feedback
		testerFB = ""
		prevHash = ""
		sameHashCount = 0
	}

	return &FixResult{
		Status:       FixFailed,
		Reason:       "max_iterations",
		Iterations:   cfg.MaxIterations,
		LastFeedback: testerFB + criticFB,
	}, nil
}
