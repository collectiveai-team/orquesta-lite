package loops

import (
	"context"
	"errors"
	"fmt"

	"github.com/lionelchamorro/orquestalite/internal/invoke"
	"github.com/lionelchamorro/orquestalite/internal/preflight"
	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

var ErrFullSuiteFailed = errors.New("full test suite failed")

// MaxDecompositionDepth caps recursive decomposition: a subtask produced by
// two decomposition generations does not decompose again — it hands off to a
// human instead. Without a cap, a structurally impossible task could spawn
// subtasks forever.
const MaxDecompositionDepth = 2

// ErrNoDecomposer is returned by Decompose implementations that do not support
// decomposition (e.g. decompose_prompt not configured). The task loop falls
// through to the normal failed path when it sees this error.
var ErrNoDecomposer = errors.New("no decomposer configured")

// ErrCommitSkipped is returned by Commit implementations when the working
// directory is not a git work tree (no .git directory). The task loop treats
// this as a successful task completion with VerifyState=commit_skipped, rather
// than as a hard failure: the work shipped, only the bookkeeping was absent.
var ErrCommitSkipped = errors.New("commit skipped: not a git repository")

// ErrNothingToCommit is returned by Commit implementations when, after a passing
// test suite, there is no net diff to commit because an earlier task already
// produced this task's files (overlapping decomposition). The task loop treats
// this as a successful no-op (VerifyState=commit_empty), not a hard failure: the
// desired end-state already exists on the branch.
var ErrNothingToCommit = errors.New("commit skipped: nothing to commit")

// SingleOutcome is the result of a single-role squad (setup/generic).
type SingleOutcome struct {
	Status       string // "done" | "failed"
	Summary      string
	FilesChanged []string
}

type TaskDeps interface {
	RunFix(ctx context.Context, taskID string, rc invoke.RunContext) (*FixResult, error)
	FullSuite(ctx context.Context) error
	Commit(ctx context.Context, msg string) (string, error)
	Rollback(ctx context.Context) error
	SaveTasks(ctx context.Context, tl *tasks.TaskList) error
	// Decompose attempts to break task t into 2-5 subtasks when the fix loop
	// exhausted all retries. Return ErrNoDecomposer if decomposition is not
	// configured for this project; the task loop will fall through to failed.
	Decompose(ctx context.Context, t *tasks.Task, fx *FixResult, filesChangedSoFar []string, rc invoke.RunContext) ([]tasks.Task, error)
	// Handoff writes a human-readable markdown handoff file for the task and
	// returns the path of the file written. It is called when both the escalation
	// ladder and auto-decomposition have been exhausted.
	Handoff(ctx context.Context, t *tasks.Task) (path string, err error)
	// PreflightEnabled reports whether the opt-in pre-flight validator is active.
	PreflightEnabled() bool
	// Preflight runs a lightweight validity check on the task before any fix
	// attempt. Only called when PreflightEnabled() returns true.
	Preflight(ctx context.Context, t *tasks.Task) preflight.Verdict
	// RunSingle runs exactly one role once (with bounded retry on transient
	// agent failure), with no tester/critic/verifier loop. Used by the setup
	// and generic squads.
	RunSingle(ctx context.Context, role string, rc invoke.RunContext) (SingleOutcome, error)
	// HasRole reports whether the named role is configured in team.json. Used to
	// fall a generic task back to the full lane when no generalist role exists.
	HasRole(role string) bool
}

// commitTask commits the task's work and sets its VerifyState/Status. It treats
// a no-git-repo (ErrCommitSkipped) and an empty no-op commit (ErrNothingToCommit)
// as success, any other commit error as a hard failure (rolled back by caller).
// Returns true when the task is done, false when it failed.
func commitTask(ctx context.Context, d TaskDeps, t *tasks.Task) bool {
	msg := fmt.Sprintf("feat(%s): %s", t.ID, t.Title)
	_, commitErr := d.Commit(ctx, msg)
	switch {
	case commitErr == nil:
		t.VerifyState = tasks.VerifyCommitOK
	case errors.Is(commitErr, ErrCommitSkipped):
		t.VerifyState = tasks.VerifyCommitSkipped
	case errors.Is(commitErr, ErrNothingToCommit):
		t.VerifyState = tasks.VerifyCommitEmpty
	default:
		t.Status = tasks.StatusFailed
		r := tasks.ReasonCommitRejected
		t.FailureReason = &r
		t.VerifyState = tasks.VerifyCommitRejected
		t.LastFeedback = strPtr(commitErr.Error())
		return false
	}
	t.Status = tasks.StatusDone
	return true
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
	return RunTaskLoopWithContext(ctx, tl, d, invoke.RunContext{})
}

func RunTaskLoopWithContext(ctx context.Context, tl *tasks.TaskList, d TaskDeps, baseRC invoke.RunContext) error {
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

		taskRC := baseRC
		taskRC.TaskID = taskID

		squad := t.SquadOrDefault()
		if squad == tasks.SquadGeneric && !d.HasRole("generalist") {
			squad = tasks.SquadFull // no generalist configured: use the full lane
		}
		if squad == tasks.SquadSetup || squad == tasks.SquadGeneric {
			role := "coder"
			if squad == tasks.SquadGeneric {
				role = "generalist"
			}
			out, err := d.RunSingle(ctx, role, taskRC)
			if err != nil {
				return err
			}
			if out.Status != "done" {
				t.Status = tasks.StatusFailed
				r := tasks.ReasonAgentRepeatedFail
				t.FailureReason = &r
				t.LastFeedback = strPtr(out.Summary)
				_ = d.Rollback(ctx)
				_ = d.SaveTasks(ctx, tl)
				continue
			}
			if !commitTask(ctx, d, t) {
				_ = d.Rollback(ctx)
				_ = d.SaveTasks(ctx, tl)
				continue
			}
			_ = d.SaveTasks(ctx, tl)
			continue
		}

		// full lane (default): existing RunFix path below, unchanged.
		fx, err := d.RunFix(ctx, taskID, taskRC)
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
				decomposeRC := taskRC
				decomposeRC.Attempt = fx.Iterations
				if decomposeRC.Attempt == 0 {
					decomposeRC.Attempt = 1
				}
				subtasks, decompErr := []tasks.Task(nil), error(ErrNoDecomposer)
				if t.DecompositionDepth < MaxDecompositionDepth {
					subtasks, decompErr = d.Decompose(ctx, t, fx, fx.FilesChangedSoFar, decomposeRC)
				} else {
					decomposeFailureNote = fmt.Sprintf("decomposition depth %d reached cap %d", t.DecompositionDepth, MaxDecompositionDepth)
				}
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
				// Rollback BEFORE writing the handoff: Rollback removes untracked
				// files created since the task started, which would include a
				// freshly written handoff document. The handoff only needs the
				// task state, not the (discarded) work tree.
				_ = d.Rollback(ctx)
				handoffPath, _ := d.Handoff(ctx, t)
				t.FailureDetails.HandoffPath = handoffPath
				t.Status = tasks.StatusNeedsHuman
				r := tasks.ReasonAgentRepeatedFail
				t.FailureReason = &r
				t.LastFeedback = strPtr(fx.LastFeedback)
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
			t.VerifyState = tasks.VerifyTestsFail
			t.LastFeedback = strPtr(err.Error())
			_ = d.Rollback(ctx)
			_ = d.SaveTasks(ctx, tl)
			continue
		}
		t.VerifyState = tasks.VerifyTestsPass

		if !commitTask(ctx, d, t) {
			_ = d.Rollback(ctx)
			_ = d.SaveTasks(ctx, tl)
			continue
		}
		_ = d.SaveTasks(ctx, tl)
	}
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
