package loops

import (
	"context"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/results"
	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

type stubReviewDeps struct {
	taskDeps TaskDeps
	reviewer func(cycle int) results.ReviewerResult
	cycles   []int
}

func (s *stubReviewDeps) RunFix(ctx context.Context, id string) (*FixResult, error) {
	return s.taskDeps.RunFix(ctx, id)
}
func (s *stubReviewDeps) FullSuite(ctx context.Context) error { return s.taskDeps.FullSuite(ctx) }
func (s *stubReviewDeps) Commit(ctx context.Context, m string) (string, error) {
	return s.taskDeps.Commit(ctx, m)
}
func (s *stubReviewDeps) Rollback(ctx context.Context) error { return s.taskDeps.Rollback(ctx) }
func (s *stubReviewDeps) SaveTasks(ctx context.Context, tl *tasks.TaskList) error {
	return s.taskDeps.SaveTasks(ctx, tl)
}
func (s *stubReviewDeps) RunReviewer(ctx context.Context, cycle int) (results.ReviewerResult, error) {
	s.cycles = append(s.cycles, cycle)
	return s.reviewer(cycle), nil
}

func boolPtr(b bool) *bool { return &b }

func TestReview_StopsWhenReviewerSaysSo(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{{ID: "T001", Status: tasks.StatusPending, Priority: 1}}}
	td := &stubTaskDeps{
		fix:       func(string) *FixResult { return &FixResult{Status: FixDone, Iterations: 1} },
		fullSuite: func() error { return nil },
		commit:    func(string) (string, error) { return "x", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}
	d := &stubReviewDeps{
		taskDeps: td,
		reviewer: func(int) results.ReviewerResult { return results.ReviewerResult{ShouldStop: boolPtr(true)} },
	}
	if err := RunReviewLoop(context.Background(), tl, ReviewConfig{MaxCycles: 5}, d); err != nil {
		t.Fatal(err)
	}
	if len(d.cycles) != 1 {
		t.Errorf("cycles ran = %d, want 1", len(d.cycles))
	}
}

func TestReview_StopsAtMaxCycles(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{{ID: "T001", Status: tasks.StatusPending, Priority: 1}}}
	td := &stubTaskDeps{
		fix:       func(string) *FixResult { return &FixResult{Status: FixDone, Iterations: 1} },
		fullSuite: func() error { return nil },
		commit:    func(string) (string, error) { return "x", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}
	d := &stubReviewDeps{
		taskDeps: td,
		reviewer: func(int) results.ReviewerResult {
			return results.ReviewerResult{ShouldStop: boolPtr(false), NewTasks: []results.ReviewerNewTask{{Title: "x", Description: "y", Priority: 1}}}
		},
	}
	_ = RunReviewLoop(context.Background(), tl, ReviewConfig{MaxCycles: 2}, d)
	if len(d.cycles) != 2 {
		t.Errorf("cycles=%d, want 2", len(d.cycles))
	}
}

func TestReview_AppendsNewTasks(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{{ID: "T001", Status: tasks.StatusPending, Priority: 1}}}
	td := &stubTaskDeps{
		fix:       func(string) *FixResult { return &FixResult{Status: FixDone, Iterations: 1} },
		fullSuite: func() error { return nil },
		commit:    func(string) (string, error) { return "x", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}
	called := false
	d := &stubReviewDeps{
		taskDeps: td,
		reviewer: func(c int) results.ReviewerResult {
			if !called {
				called = true
				return results.ReviewerResult{ShouldStop: boolPtr(false), NewTasks: []results.ReviewerNewTask{
					{Title: "follow-up", Description: "y", Priority: 5},
				}}
			}
			return results.ReviewerResult{ShouldStop: boolPtr(true)}
		},
	}
	_ = RunReviewLoop(context.Background(), tl, ReviewConfig{MaxCycles: 5}, d)
	if len(tl.Tasks) != 2 {
		t.Errorf("expected 2 tasks after append, got %d", len(tl.Tasks))
	}
	if tl.Tasks[1].Title != "follow-up" || tl.Tasks[1].CreatedInReviewCycle != 1 {
		t.Errorf("appended task wrong: %+v", tl.Tasks[1])
	}
}
