package loops

import (
	"context"

	"github.com/lionelchamorro/orquestalite/internal/invoke"
	"github.com/lionelchamorro/orquestalite/internal/results"
	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

// ReviewDeps extends TaskDeps with the end-of-cycle analysis calls: the
// verifier (does the shipped increment actually work?) and the reviewer
// (is the code sound, what should the next cycle do?).
type ReviewDeps interface {
	TaskDeps
	CycleBaseSHA(ctx context.Context) (string, error)
	// CycleVerification black-box-verifies the cycle's shipped work and
	// returns a human-readable report for the reviewer. Returns "" when
	// per-cycle verification is not configured. Implementations should fold
	// verifier-agent failures into the report rather than erroring, so a
	// flaky verifier cannot kill the run.
	CycleVerification(ctx context.Context, rc invoke.RunContext) (string, error)
	RunReviewer(ctx context.Context, rc invoke.RunContext, verificationReport string) (results.ReviewerResult, error)
}

// ReviewConfig holds configuration for the review loop.
type ReviewConfig struct {
	MaxCycles int
}

// RunReviewLoop runs up to cfg.MaxCycles iterations of:
//  1. Drain all pending tasks via RunTaskLoop.
//  2. Run the end-of-cycle verification (when configured) over the increment.
//  3. Call RunReviewer for the current cycle with the verification report.
//  4. Convert rev.NewTasks → tasks.Task and append them (assigns IDs + CreatedInReviewCycle).
//  5. SaveTasks.
//  6. Return nil early if reviewer signals stop or there is nothing left to do.
func RunReviewLoop(ctx context.Context, tl *tasks.TaskList, cfg ReviewConfig, d ReviewDeps) error {
	for cycle := 1; cycle <= cfg.MaxCycles; cycle++ {
		cycleBaseSHA, err := d.CycleBaseSHA(ctx)
		if err != nil {
			return err
		}
		rc := invoke.RunContext{Cycle: cycle, Attempt: 1, CycleBaseSHA: cycleBaseSHA}
		if err := RunTaskLoopWithContext(ctx, tl, d, rc); err != nil {
			return err
		}

		verification, err := d.CycleVerification(ctx, rc)
		if err != nil {
			return err
		}

		rev, err := d.RunReviewer(ctx, rc, verification)
		if err != nil {
			return err
		}

		// Convert reviewer's new-task suggestions to tasks.Task values.
		newOnes := make([]tasks.Task, 0, len(rev.NewTasks))
		for _, n := range rev.NewTasks {
			newOnes = append(newOnes, tasks.Task{
				Title:       n.Title,
				Description: n.Description,
				Priority:    n.Priority,
			})
		}
		tl.Append(newOnes, cycle)
		_ = d.SaveTasks(ctx, tl)

		if rev.ShouldStop != nil && *rev.ShouldStop {
			return nil
		}
		if !tl.AnyPending() && len(rev.NewTasks) == 0 {
			return nil
		}
	}
	return nil
}
