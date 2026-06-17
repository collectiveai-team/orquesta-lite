package loops

import (
	"context"

	"github.com/lionelchamorro/orquestalite/internal/invoke"
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

// VerifierOutcome is the result of the optional black-box verification pass
// that runs after the critic approves. The verifier exercises the change the
// way a human would (start the app, hit endpoints, run the CLI) instead of
// trusting the test suite — closing the "tests pass but manual testing
// fails" gap.
type VerifierOutcome struct {
	Status   string // "pass" | "fail"
	Feedback string // injected back to coder on fail
}

// CoderFeedback carries all inputs to a coder attempt.
type CoderFeedback struct {
	PreviousAttemptSummary string
	FilesChangedSoFar      []string
	TesterFeedback         string
	CriticFeedback         string
	VerifierFeedback       string
	LintFeedback           string
	AgentOverride          string // empty = use default chain; non-empty = use this specific agent
}

type RoleRunner interface {
	RunCoder(ctx context.Context, rc invoke.RunContext, fb CoderFeedback) (CoderOutcome, error)
	RunTester(ctx context.Context, rc invoke.RunContext) (TesterOutcome, error)
	RunCritic(ctx context.Context, rc invoke.RunContext) (CriticOutcome, error)
	// RunVerifier is only called when FixConfig.VerifierEnabled is true.
	RunVerifier(ctx context.Context, rc invoke.RunContext) (VerifierOutcome, error)
}

type FixStatus int

const (
	FixDone   FixStatus = iota
	FixFailed FixStatus = iota
)

type FixConfig struct {
	MaxIterations    int
	EscalationLadder []string // tried in order when stuck detected
	VerifierEnabled  bool     // run the verifier role after critic approval
	// LintGate, when non-nil, runs as a deterministic quality gate after each
	// coder attempt and before the tester. It returns ok=true when the change
	// is clean (or there is no linter); on ok=false the feedback is injected
	// back to the coder and the attempt retries — so lint violations are fixed
	// in-loop rather than only blocking the commit. nil = no lint gate.
	LintGate func(ctx context.Context) (ok bool, feedback string)
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

func RunFix(ctx context.Context, cfg FixConfig, r RoleRunner, baseRC invoke.RunContext) (*FixResult, error) {
	var fb CoderFeedback
	var prevHash string
	sameHashCount := 0
	escalationIdx := 0

	var previousAttemptSummary string
	var filesChangedSoFar []string

	for attempt := 1; attempt <= cfg.MaxIterations; attempt++ {
		rc := baseRC
		rc.Attempt = attempt
		fb.PreviousAttemptSummary = previousAttemptSummary
		fb.FilesChangedSoFar = filesChangedSoFar

		coder, err := r.RunCoder(ctx, rc, fb)
		if err != nil {
			return nil, err
		}

		// Capture enriched feedback for next attempt.
		previousAttemptSummary = coder.Summary
		filesChangedSoFar = appendUnique(filesChangedSoFar, coder.FilesChanged)

		// Lint gate: a deterministic quality check before the (slower) tester.
		// A failure feeds the linter output back to the coder and retries, so
		// lint/format violations are fixed in-loop instead of only blocking the
		// commit. A missing linter returns ok=true (see liveDeps.lintGateOutcome).
		if cfg.LintGate != nil {
			if ok, lintFB := cfg.LintGate(ctx); !ok {
				if attempt >= cfg.MaxIterations {
					return &FixResult{
						Status:            FixFailed,
						Reason:            "lint_failed",
						Iterations:        cfg.MaxIterations,
						LastFeedback:      lintFB,
						FilesChangedSoFar: filesChangedSoFar,
					}, nil
				}
				fb.LintFeedback = lintFB
				fb.TesterFeedback = ""
				fb.CriticFeedback = ""
				fb.VerifierFeedback = ""
				fb.AgentOverride = ""
				prevHash = ""
				sameHashCount = 0
				continue
			}
		}
		fb.LintFeedback = ""

		t, err := r.RunTester(ctx, rc)
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
					fb.VerifierFeedback = ""
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
			fb.VerifierFeedback = ""
			fb.AgentOverride = ""
			continue
		}

		// Tester passed — run critic.
		c, err := r.RunCritic(ctx, rc)
		if err != nil {
			return nil, err
		}
		if c.Status == "approved" {
			// Critic approved — optionally run the black-box verifier before
			// declaring the task done. The verifier exercises the running
			// software directly, so a green-but-meaningless test suite cannot
			// close the task on its own.
			if cfg.VerifierEnabled {
				v, err := r.RunVerifier(ctx, rc)
				if err != nil {
					return nil, err
				}
				if v.Status != "pass" {
					if attempt >= cfg.MaxIterations {
						return &FixResult{
							Status:            FixFailed,
							Reason:            "max_iterations",
							Iterations:        cfg.MaxIterations,
							LastFeedback:      v.Feedback,
							FilesChangedSoFar: filesChangedSoFar,
						}, nil
					}
					fb.VerifierFeedback = v.Feedback
					fb.TesterFeedback = ""
					fb.CriticFeedback = ""
					fb.AgentOverride = ""
					prevHash = ""
					sameHashCount = 0
					continue
				}
			}
			return &FixResult{Status: FixDone, Iterations: attempt}, nil
		}
		// Critic rejected: feed critic feedback back to coder on next attempt.
		// Reset hash tracking so a future tester failure starts fresh.
		fb.CriticFeedback = c.Feedback
		fb.TesterFeedback = ""
		fb.VerifierFeedback = ""
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
