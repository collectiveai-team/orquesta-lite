package loops

import (
	"context"
)

type CoderOutcome struct {
	Status       string // "completed" | "blocked"
	Summary      string
	FilesChanged []string
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

// CoderFeedback carries all inputs to a coder attempt.
type CoderFeedback struct {
	PreviousAttemptSummary string
	FilesChangedSoFar      []string
	TesterFeedback         string
	CriticFeedback         string
	AgentOverride          string // empty = use default chain; non-empty = use this specific agent
}

type RoleRunner interface {
	RunCoder(ctx context.Context, attempt int, fb CoderFeedback) (CoderOutcome, error)
	RunTester(ctx context.Context, attempt int) (TesterOutcome, error)
	RunCritic(ctx context.Context, attempt int) (CriticOutcome, error)
}

type FixStatus int

const (
	FixDone   FixStatus = iota
	FixFailed FixStatus = iota
)

type FixConfig struct {
	MaxIterations    int
	EscalationLadder []string // tried in order when stuck detected
}

type FixResult struct {
	Status            FixStatus
	Reason            string // "max_iterations" | "agent_repeated_failure" | "" when Done
	Iterations        int
	LastFeedback      string
	FilesChangedSoFar []string // union of all files touched across coder attempts
}

// appendUnique appends items from src to dst, skipping duplicates.
func appendUnique(dst []string, src []string) []string {
	seen := make(map[string]struct{}, len(dst))
	for _, v := range dst {
		seen[v] = struct{}{}
	}
	for _, v := range src {
		if _, ok := seen[v]; !ok {
			dst = append(dst, v)
			seen[v] = struct{}{}
		}
	}
	return dst
}

func RunFix(ctx context.Context, cfg FixConfig, r RoleRunner) (*FixResult, error) {
	var fb CoderFeedback
	var prevHash string
	sameHashCount := 0
	escalationIdx := 0

	var previousAttemptSummary string
	var filesChangedSoFar []string

	for attempt := 1; attempt <= cfg.MaxIterations; attempt++ {
		fb.PreviousAttemptSummary = previousAttemptSummary
		fb.FilesChangedSoFar = filesChangedSoFar

		coder, err := r.RunCoder(ctx, attempt, fb)
		if err != nil {
			return nil, err
		}

		// Capture enriched feedback for next attempt.
		previousAttemptSummary = coder.Summary
		filesChangedSoFar = appendUnique(filesChangedSoFar, coder.FilesChanged)

		t, err := r.RunTester(ctx, attempt)
		if err != nil {
			return nil, err
		}
		if t.Status == "fail" {
			// max_iterations check takes priority when the iteration budget is exhausted.
			if attempt >= cfg.MaxIterations {
				return &FixResult{
					Status:            FixFailed,
					Reason:            "max_iterations",
					Iterations:        cfg.MaxIterations,
					LastFeedback:      t.Feedback,
					FilesChangedSoFar: filesChangedSoFar,
				}, nil
			}

			// Repeated-failure detection: track consecutive identical FailuresHash values.
			// Fire after 2 consecutive identical failures (coder given feedback but still stuck).
			if t.FailuresHash != "" && t.FailuresHash == prevHash {
				sameHashCount++
			} else {
				sameHashCount = 1
				prevHash = t.FailuresHash
			}
			if sameHashCount >= 2 {
				// Attempt escalation before giving up.
				if escalationIdx < len(cfg.EscalationLadder) {
					fb.AgentOverride = cfg.EscalationLadder[escalationIdx]
					escalationIdx++
					// Give the new agent a clean stuck-detection window.
					sameHashCount = 0
					prevHash = ""
					fb.TesterFeedback = t.Feedback
					fb.CriticFeedback = ""
					continue
				}
				// Ladder exhausted.
				return &FixResult{
					Status:            FixFailed,
					Reason:            "agent_repeated_failure",
					Iterations:        attempt,
					LastFeedback:      t.Feedback,
					FilesChangedSoFar: filesChangedSoFar,
				}, nil
			}

			fb.TesterFeedback = t.Feedback
			fb.CriticFeedback = ""
			fb.AgentOverride = ""
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
		fb.CriticFeedback = c.Feedback
		fb.TesterFeedback = ""
		fb.AgentOverride = ""
		prevHash = ""
		sameHashCount = 0
	}

	return &FixResult{
		Status:            FixFailed,
		Reason:            "max_iterations",
		Iterations:        cfg.MaxIterations,
		LastFeedback:      fb.TesterFeedback + fb.CriticFeedback,
		FilesChangedSoFar: filesChangedSoFar,
	}, nil
}
