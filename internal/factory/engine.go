package factory

import (
	"context"
	"fmt"
	"time"
)

// Summary is what a feature run reports back to the queue.
type Summary struct {
	TasksDone   int
	TasksFailed int
	TasksOther  int
}

// Deps abstracts everything the engine needs from the outside world so the
// queue-processing logic is testable without git or live agents.
type Deps interface {
	// CheckoutFeatureBranch makes the work tree point at the feature's branch,
	// creating it from base when it does not exist yet.
	CheckoutFeatureBranch(branch, base string) error
	// CheckoutBase returns the work tree to the base branch.
	CheckoutBase(base string) error
	// RunFeature plans and runs one feature on the current branch.
	RunFeature(ctx context.Context, f Feature) (Summary, error)
	// SaveState persists the queue after every transition.
	SaveState(q *Queue) error
	// Logf reports human-readable progress.
	Logf(format string, args ...any)
}

// Run drains the queue: each runnable feature is checked out on its own
// branch, planned, and run. A feature failure is recorded and the engine
// moves on; only infrastructure errors (git checkout, state persistence)
// abort the whole factory run. The work tree is returned to the base branch
// after every feature.
func Run(ctx context.Context, q *Queue, d Deps) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		f := q.NextRunnable()
		if f == nil {
			d.Logf("factory: queue drained (%s)", Counts(q))
			return nil
		}

		now := time.Now().UTC()
		f.Status = StatusInProgress
		if f.StartedAt == nil {
			f.StartedAt = &now
		}
		if err := d.SaveState(q); err != nil {
			return err
		}

		d.Logf("factory: %s %q -> branch %s", f.ID, f.Title, f.Branch)
		if err := d.CheckoutFeatureBranch(f.Branch, q.BaseBranch); err != nil {
			return fmt.Errorf("checkout %s: %w", f.Branch, err)
		}

		sum, runErr := d.RunFeature(ctx, *f)
		f.TasksDone, f.TasksFailed, f.TasksOther = sum.TasksDone, sum.TasksFailed, sum.TasksOther
		end := time.Now().UTC()
		f.FinishedAt = &end
		if runErr != nil {
			f.Status = StatusFailed
			f.Error = runErr.Error()
			d.Logf("factory: %s failed: %v", f.ID, runErr)
		} else if sum.TasksDone == 0 && sum.TasksFailed > 0 {
			f.Status = StatusFailed
			f.Error = "no task completed"
			d.Logf("factory: %s failed: no task completed", f.ID)
		} else {
			f.Status = StatusDone
			d.Logf("factory: %s done (%d tasks done, %d failed)", f.ID, sum.TasksDone, sum.TasksFailed)
		}

		if err := d.CheckoutBase(q.BaseBranch); err != nil {
			_ = d.SaveState(q)
			return fmt.Errorf("checkout base %s: %w", q.BaseBranch, err)
		}
		if err := d.SaveState(q); err != nil {
			return err
		}

		// Context cancellation during the run surfaces here; stop cleanly with
		// the feature state already persisted.
		if runErr != nil && ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// Counts renders a short "3 done, 1 failed, 2 pending" summary.
func Counts(q *Queue) string {
	byStatus := map[Status]int{}
	for _, f := range q.Features {
		byStatus[f.Status]++
	}
	return fmt.Sprintf("%d done, %d failed, %d pending",
		byStatus[StatusDone], byStatus[StatusFailed], byStatus[StatusPending])
}
