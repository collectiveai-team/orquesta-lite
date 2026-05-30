package loops

import (
	"context"
	"errors"
	"fmt"

	"github.com/lionelchamorro/orquestalite/internal/preflight"
	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

var ErrFullSuiteFailed = errors.New("full test suite failed")

// ErrNoDecomposer is returned by Decompose implementations that do not support
// decomposition (e.g. decompose_prompt not configured). The task loop falls
// through to the normal failed path when it sees this error.
var ErrNoDecomposer = errors.New("no decomposer configured")

type TaskDeps interface {
	RunFix(ctx context.Context, taskID string) (*FixResult, error)
	FullSuite(ctx context.Context) error
	Commit(ctx context.Context, msg string) (string, error)
	Rollback(ctx context.Context) error
	SaveTasks(ctx context.Context, tl *tasks.TaskList) error
	// Decompose attempts to break task t into 2-5 subtasks when the fix loop
	// exhausted all retries. Return ErrNoDecomposer if decomposition is not
	// configured for this project; the task loop will fall through to failed.
	Decompose(ctx context.Context, t *tasks.Task, fx *FixResult, filesChangedSoFar []string) ([]tasks.Task, error)
	// Handoff writes a human-readable markdown handoff file for the task and
	// returns the path of the file written. It is called when both the escalation
	// ladder and auto-decomposition have been exhausted.
	Handoff(ctx context.Context, t *tasks.Task) (path string, err error)
	// PreflightEnabled reports whether the opt-in pre-flight validator is active.
	PreflightEnabled() bool
	// Preflight runs a lightweight validity check on the task before any fix
	// attempt. Only called when PreflightEnabled() returns true.
	Preflight(ctx context.Context, t *tasks.Task) preflight.Verdict
}

// findTaskIdx returns the index of the task with the given ID, or -1 if not found.
func findTaskIdx(tl *tasks.TaskList, id string) int {
	for i := range tl.Tasks {
		if tl.Tasks[i].ID == id {
			return i
		}
	}
	return -1
}

func RunTaskLoop(ctx context.Context, tl *tasks.TaskList, d TaskDeps) error {
	for {
		t := tl.NextPending()
		if t == nil {
			return nil
		}
		taskID := t.ID

		// Opt-in pre-flight check: runs once before the first fix attempt.
		if d.PreflightEnabled() {
			if v := d.Preflight(ctx, t); !v.OK {
				t.Status = tasks.StatusNeedsClarification
				t.LastFeedback = &v.Reason
				_ = d.SaveTasks(ctx, tl)
				continue
			}
		}

		t.Status = tasks.StatusInProgress
		t.Attempts++
		_ = d.SaveTasks(ctx, tl)

		fx, err := d.RunFix(ctx, taskID)
		if err != nil {
			return err
		}
		if fx.Status == FixFailed {
			if fx.Reason == "agent_repeated_failure" {
				// Re-acquire t by index before calling Decompose (t may be stale if
				// the caller passed the original pointer after a prior append).
				idx := findTaskIdx(tl, taskID)
				if idx >= 0 {
					t = &tl.Tasks[idx]
				}
				var decomposeFailureNote string
				subtasks, decompErr := d.Decompose(ctx, t, fx, fx.FilesChangedSoFar)
				if decompErr == nil && len(subtasks) > 0 {
					added := tl.Append(subtasks, t.CreatedInReviewCycle)
					ids := make([]string, len(added))
					for i, sub := range added {
						ids[i] = sub.ID
					}
					// Re-acquire pointer after Append (slice may have been reallocated).
					idx2 := findTaskIdx(tl, taskID)
					if idx2 >= 0 {
						tl.Tasks[idx2].Status = tasks.StatusDecomposed
						tl.Tasks[idx2].DecomposedIntoIDs = ids
					}
					_ = d.Rollback(ctx)
					_ = d.SaveTasks(ctx, tl)
					continue
				}
				// ErrNoDecomposer or empty result: attempt handoff then continue.
				// Unexpected decompose errors are captured so the handoff document
				// records what went wrong.
				if decompErr != nil && !errors.Is(decompErr, ErrNoDecomposer) {
					decomposeFailureNote = "decompose failed: " + decompErr.Error()
				}
				// Re-acquire pointer after Decompose (slice may have grown).
				idx2 := findTaskIdx(tl, taskID)
				if idx2 >= 0 {
					t = &tl.Tasks[idx2]
				}
				fd := &tasks.FailureDetails{
					Reason:         tasks.ReasonAgentRepeatedFail,
					TaskSuspect:    true,
					LastStderrTail: decomposeFailureNote,
				}
				t.FailureDetails = fd
				handoffPath, _ := d.Handoff(ctx, t)
				t.FailureDetails.HandoffPath = handoffPath
				t.Status = tasks.StatusNeedsHuman
				r := tasks.ReasonAgentRepeatedFail
				t.FailureReason = &r
				t.LastFeedback = strPtr(fx.LastFeedback)
				_ = d.Rollback(ctx)
				_ = d.SaveTasks(ctx, tl)
				continue
			}
			// Re-acquire in case Decompose triggered any slice growth.
			idx := findTaskIdx(tl, taskID)
			if idx >= 0 {
				t = &tl.Tasks[idx]
			}
			t.Status = tasks.StatusFailed
			t.LastFeedback = strPtr(fx.LastFeedback)
			r := tasks.FailureReason(fx.Reason)
			t.FailureReason = &r
			_ = d.Rollback(ctx)
			_ = d.SaveTasks(ctx, tl)
			continue
		}

		if err := d.FullSuite(ctx); err != nil {
			t.Status = tasks.StatusFailed
			r := tasks.ReasonFullSuiteFailed
			t.FailureReason = &r
			t.LastFeedback = strPtr(err.Error())
			_ = d.Rollback(ctx)
			_ = d.SaveTasks(ctx, tl)
			continue
		}

		msg := fmt.Sprintf("feat(%s): %s", t.ID, t.Title)
		if _, err := d.Commit(ctx, msg); err != nil {
			t.Status = tasks.StatusFailed
			r := tasks.ReasonCommitRejected
			t.FailureReason = &r
			t.LastFeedback = strPtr(err.Error())
			_ = d.Rollback(ctx)
			_ = d.SaveTasks(ctx, tl)
			continue
		}

		t.Status = tasks.StatusDone
		_ = d.SaveTasks(ctx, tl)
	}
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
