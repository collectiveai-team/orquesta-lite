package loops

import (
	"context"
	"errors"
	"fmt"

	"github.com/lionelchamorro/orquesta-lite/internal/tasks"
)

var ErrFullSuiteFailed = errors.New("full test suite failed")

type TaskDeps interface {
	RunFix(ctx context.Context, taskID string) (*FixResult, error)
	FullSuite(ctx context.Context) error
	Commit(ctx context.Context, msg string) (string, error)
	Rollback(ctx context.Context) error
	SaveTasks(ctx context.Context, tl *tasks.TaskList) error
}

func RunTaskLoop(ctx context.Context, tl *tasks.TaskList, d TaskDeps) error {
	for {
		t := tl.NextPending()
		if t == nil {
			return nil
		}
		t.Status = tasks.StatusInProgress
		t.Attempts++
		_ = d.SaveTasks(ctx, tl)

		fx, err := d.RunFix(ctx, t.ID)
		if err != nil {
			return err
		}
		if fx.Status == FixFailed {
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
