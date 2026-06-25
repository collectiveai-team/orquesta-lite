package factory

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Summary is what a feature run reports back to the queue.
type Summary struct {
	TasksDone   int
	TasksFailed int
	TasksOther  int
	CostUSD     float64
	// FailedTaskIDs lists the tasks that ended in failed/needs_human, used by the
	// engine's no-progress guard to decide whether a repair retry made headway.
	FailedTaskIDs []string
}

// Config holds factory-level knobs.
type Config struct {
	// BudgetUSD stops the queue before starting the next feature once the
	// recorded spend of finished features reaches it. 0 = unlimited. The
	// queue state is preserved, so raising the budget and resuming continues
	// where it stopped.
	BudgetUSD float64
	// Resume makes failed features runnable again so the queue retries them.
	// It no longer governs plan reuse: reuse is the default (see Replan). A
	// resumed feature continues its persisted tasks-<ID>.json, skipping
	// completed tasks.
	Resume bool
	// Replan forces a fresh task decomposition for every feature this run,
	// discarding the persisted tasks-<ID>.json. Without it, an already-planned
	// feature reuses its task list (the default).
	Replan bool
	// MaxFeatureRetries is the number of EXTRA feature-level runs attempted when
	// a feature fails the merge gate (the no-progress guard may stop sooner).
	// Populated from limits.max_feature_retries; 0 means stop on first failure.
	MaxFeatureRetries int
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
	// RunFeature plans and runs one feature on the current branch. When
	// reusePlan is true it skips the re-plan and continues the existing
	// tasks.json (resume), so already-completed tasks are not redone.
	RunFeature(ctx context.Context, f Feature, reusePlan bool) (Summary, error)
	// PublishFeature optionally opens a pull request for a finished feature
	// branch. Return "" (no error) when publishing is disabled. Errors are
	// logged and never fail the feature — the branch still exists locally.
	PublishFeature(ctx context.Context, f Feature, base string) (prURL string, err error)
	// SaveState persists the queue after every transition.
	SaveState(q *Queue) error
	// Logf reports human-readable progress.
	Logf(format string, args ...any)
	// MergeFeatureToBase merges a gate-passed feature branch into base and leaves
	// the work tree on base. Returns the merge method ("ff" or "no-ff"). An error
	// (e.g. a conflict) leaves base unchanged for the caller to handle.
	MergeFeatureToBase(branch, base string) (method string, err error)
	// Event records a structured factory event (one JSONL line in run.log).
	Event(name string, fields map[string]any)
}

// Run drains the queue: each runnable feature is checked out on its own
// branch, planned, and run. Features that pass the merge gate are merged into
// base (leaving the tree on base), then the queue continues. Features that fail
// the gate after a bounded repair loop are left on their branch and the queue
// stops for operator intervention (--resume). Merge conflicts likewise pause the
// queue. Only infrastructure errors (git checkout, state persistence) abort the
// entire factory run.
func Run(ctx context.Context, q *Queue, cfg Config, d Deps) error {
	// attempted guards against re-picking a feature already run this invocation
	// — without it, --resume would re-select a feature that fails again on every
	// pass and loop forever.
	attempted := map[string]bool{}
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
		f := q.NextRunnable(cfg.Resume, attempted)
		if f == nil {
			d.Logf("factory: queue drained (%s)", Counts(q))
			return nil
		}
		attempted[f.ID] = true

		// Reuse the feature's persisted task list whenever it has already been
		// decomposed — unless --replan forces a fresh plan. This is the default,
		// so an interrupted or retried feature continues its own tasks-<ID>.json
		// instead of re-planning from scratch.
		reusePlan := !cfg.Replan && q.FeatureIsPlanned(f.ID)

		now := time.Now().UTC()
		f.Status = StatusInProgress
		if f.StartedAt == nil {
			f.StartedAt = &now
		}
		// Record that this feature owns a persisted task list from here on
		// (RunFeature writes tasks-<ID>.json whether it plans fresh or reuses).
		q.MarkFeaturePlanned(f.ID)
		if err := d.SaveState(q); err != nil {
			return err
		}

		if reusePlan {
			d.Logf("factory: %s %q -> branch %s (reusing task list, no re-plan)", f.ID, f.Title, f.Branch)
		} else {
			d.Logf("factory: %s %q -> branch %s", f.ID, f.Title, f.Branch)
		}
		if err := d.CheckoutFeatureBranch(f.Branch, q.BaseBranch); err != nil {
			return fmt.Errorf("checkout %s: %w", f.Branch, err)
		}

		sum, runErr := d.RunFeature(ctx, *f, reusePlan)

		// Feature-level repair loop: when the feature does not pass the merge gate,
		// re-run it (reusing its task list) up to cfg.MaxFeatureRetries times,
		// stopping early once the set of failing tasks stops shrinking so a
		// structurally-unfixable feature cannot loop-fail.
		failedPrev := len(sum.FailedTaskIDs)
		for attempt := 0; !featureGatePassed(sum, runErr) && attempt < cfg.MaxFeatureRetries; attempt++ {
			if ctx.Err() != nil {
				break
			}
			d.Logf("factory: %s did not pass the merge gate (%d task(s) failing) — repair attempt %d/%d",
				f.ID, len(sum.FailedTaskIDs), attempt+1, cfg.MaxFeatureRetries)
			sum, runErr = d.RunFeature(ctx, *f, true)
			if featureGatePassed(sum, runErr) {
				break
			}
			if len(sum.FailedTaskIDs) >= failedPrev {
				d.Logf("factory: %s repair made no progress (%d task(s) still failing) — stopping retries",
					f.ID, len(sum.FailedTaskIDs))
				break
			}
			failedPrev = len(sum.FailedTaskIDs)
		}

		f.TasksDone, f.TasksFailed, f.TasksOther = sum.TasksDone, sum.TasksFailed, sum.TasksOther
		f.CostUSD = sum.CostUSD
		end := time.Now().UTC()
		f.FinishedAt = &end

		if featureGatePassed(sum, runErr) {
			method, mErr := d.MergeFeatureToBase(f.Branch, q.BaseBranch)
			if mErr != nil {
				// Conflict or git failure: keep the feature on its branch, return to
				// base, and stop the queue for the operator.
				f.Status = StatusFailed
				f.Error = "merge to base failed: " + mErr.Error()
				d.Event("feature_merge_blocked", map[string]any{
					"feature": f.ID, "branch": f.Branch, "reason": "merge_failed", "error": mErr.Error(),
				})
				d.Logf("factory: %s merge to base failed: %v — left on branch %s; resolve and `orq-lite factory --resume`",
					f.ID, mErr, f.Branch)
				if err := d.CheckoutBase(q.BaseBranch); err != nil {
					_ = d.SaveState(q)
					return fmt.Errorf("checkout base %s: %w", q.BaseBranch, err)
				}
				return d.SaveState(q)
			}
			merged := time.Now().UTC()
			f.Status = StatusDone
			f.Merged = true
			f.MergedAt = &merged
			d.Event("feature_merged", map[string]any{
				"feature": f.ID, "branch": f.Branch, "base": q.BaseBranch, "method": method,
			})
			d.Logf("factory: %s done (%d tasks done, $%.2f) — merged to %s (%s)",
				f.ID, sum.TasksDone, sum.CostUSD, q.BaseBranch, method)
			if url, err := d.PublishFeature(ctx, *f, q.BaseBranch); err != nil {
				d.Logf("factory: %s PR not created: %v", f.ID, err)
			} else if url != "" {
				f.PRURL = url
				d.Logf("factory: %s PR %s", f.ID, url)
			}
			if err := d.SaveState(q); err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}

		// Gate not passed: record the failure, preserve any residue on the feature
		// branch, return to base WITHOUT merging, and stop the queue.
		f.Status = StatusFailed
		if runErr != nil {
			f.Error = runErr.Error()
		} else {
			f.Error = fmt.Sprintf("%d task(s) did not pass the gate: %s",
				len(sum.FailedTaskIDs), strings.Join(sum.FailedTaskIDs, ", "))
		}
		d.Event("feature_merge_blocked", map[string]any{
			"feature": f.ID, "branch": f.Branch, "failed_tasks": sum.FailedTaskIDs, "reason": "gate_failed",
		})
		d.Logf("factory: %s did not pass the merge gate — %s; left on branch %s, not merged. Fix and `orq-lite factory --resume`.",
			f.ID, f.Error, f.Branch)

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
		return nil // stop the queue; operator fixes and resumes
	}
}

// featureGatePassed reports whether a feature run is clean enough to merge into
// base: no run error, no failed tasks, and no tasks left in a non-terminal state.
func featureGatePassed(sum Summary, runErr error) bool {
	return runErr == nil && sum.TasksFailed == 0 && sum.TasksOther == 0
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
