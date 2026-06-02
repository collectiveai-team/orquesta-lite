package loops

import (
	"context"

	"github.com/lionelchamorro/orquestalite/internal/invoke"
	"github.com/lionelchamorro/orquestalite/internal/results"
	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

// ReviewDeps extends TaskDeps with the reviewer agent call.
type ReviewDeps interface {
	TaskDeps
	RunReviewer(ctx context.Context, rc invoke.RunContext) (results.ReviewerResult, error)
}

// ReviewConfig holds configuration for the review loop.
type ReviewConfig struct {
	MaxCycles int
}

// RunReviewLoop runs up to cfg.MaxCycles iterations of:
//  1. Drain all pending tasks via RunTaskLoop.
//  2. Call RunReviewer for the current cycle.
//  3. Convert rev.NewTasks → tasks.Task and append them (assigns IDs + CreatedInReviewCycle).
//  4. SaveTasks.
//  5. Return nil early if reviewer signals stop or there is nothing left to do.
func RunReviewLoop(ctx context.Context, tl *tasks.TaskList, cfg ReviewConfig, d ReviewDeps) error {
	for cycle := 1; cycle <= cfg.MaxCycles; cycle++ {
		rc := invoke.RunContext{Cycle: cycle, Attempt: 1}
		if err := RunTaskLoopWithContext(ctx, tl, d, rc); err != nil {
			return err
		}

		rev, err := d.RunReviewer(ctx, rc)
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
