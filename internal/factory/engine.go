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
	CostUSD     float64
}

// Config holds factory-level knobs.
type Config struct {
	// BudgetUSD stops the queue before starting the next feature once the
	// recorded spend of finished features reaches it. 0 = unlimited. The
	// queue state is preserved, so raising the budget and resuming continues
	// where it stopped.
	BudgetUSD float64
}

// Deps abstracts everything the engine needs from the outside world so the
// queue-processing logic is testable without git or live agents.
type Deps interface {
	// CheckoutFeatureBranch makes the work tree point at the feature's branch,
	// creating it from base when it does not exist yet.
	CheckoutFeatureBranch(branch, base string) error
	// CheckpointResidue commits any uncommitted residue from an interrupted
	// task to the CURRENT (feature) branch as a labelled WIP commit, so the
	// work is preserved on the branch and the subsequent return to base finds a
	// clean tree. No-op when the tree is already clean. Returns whether a
	// checkpoint commit was made.
	CheckpointResidue(f Feature) (bool, error)
	// CheckoutBase returns the work tree to the base branch.
	CheckoutBase(base string) error
	// RunFeature plans and runs one feature on the current branch.
	RunFeature(ctx context.Context, f Feature) (Summary, error)
	// PublishFeature optionally opens a pull request for a finished feature
	// branch. Return "" (no error) when publishing is disabled. Errors are
	// logged and never fail the feature — the branch still exists locally.
	PublishFeature(ctx context.Context, f Feature, base string) (prURL string, err error)
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
func Run(ctx context.Context, q *Queue, cfg Config, d Deps) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if cfg.BudgetUSD > 0 {
			if spent := SpentUSD(q); spent >= cfg.BudgetUSD {
				d.Logf("factory: budget exhausted ($%.2f of $%.2f) — stopping; raise limits.factory_budget_usd and resume with `orq-lite factory`", spent, cfg.BudgetUSD)
				return nil
			}
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
		f.CostUSD = sum.CostUSD
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
			d.Logf("factory: %s done (%d tasks done, %d failed, $%.2f)", f.ID, sum.TasksDone, sum.TasksFailed, sum.CostUSD)
			if url, err := d.PublishFeature(ctx, *f, q.BaseBranch); err != nil {
				d.Logf("factory: %s PR not created: %v", f.ID, err)
			} else if url != "" {
				f.PRURL = url
				d.Logf("factory: %s PR %s", f.ID, url)
			}
		}

		// Preserve any half-done residue (e.g. an interrupted task whose hard
		// failure skipped task-level rollback) as a WIP commit on the feature
		// branch before switching away — otherwise the work is lost AND the
		// checkout would abort on a dirty tree. The branch keeps the work; base
		// stays clean.
		if checkpointed, err := d.CheckpointResidue(*f); err != nil {
			_ = d.SaveState(q)
			return fmt.Errorf("checkpoint residue for %s: %w", f.ID, err)
		} else if checkpointed {
			d.Logf("factory: %s checkpointed uncommitted residue to %s (wip commit)", f.ID, f.Branch)
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

// SpentUSD sums the recorded cost of features that finished (done or failed).
func SpentUSD(q *Queue) float64 {
	total := 0.0
	for _, f := range q.Features {
		if f.Status == StatusDone || f.Status == StatusFailed {
			total += f.CostUSD
		}
	}
	return total
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
