package loops

import (
	"context"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/invoke"
	"github.com/lionelchamorro/orquestalite/internal/preflight"
	"github.com/lionelchamorro/orquestalite/internal/results"
	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

type stubReviewDeps struct {
	taskDeps     TaskDeps
	cycleBaseSHA func() (string, error)
	verification func(cycle int) string
	reviewer     func(cycle int) results.ReviewerResult
	cycles       []int
	bases        []string
	reports      []string
}

func (s *stubReviewDeps) RunFix(ctx context.Context, id string, rc invoke.RunContext) (*FixResult, error) {
	return s.taskDeps.RunFix(ctx, id, rc)
}
func (s *stubReviewDeps) FullSuite(ctx context.Context) error { return s.taskDeps.FullSuite(ctx) }
func (s *stubReviewDeps) Commit(ctx context.Context, m string) (string, error) {
	return s.taskDeps.Commit(ctx, m)
}
func (s *stubReviewDeps) Rollback(ctx context.Context) error { return s.taskDeps.Rollback(ctx) }
func (s *stubReviewDeps) SaveTasks(ctx context.Context, tl *tasks.TaskList) error {
	return s.taskDeps.SaveTasks(ctx, tl)
}
func (s *stubReviewDeps) Decompose(ctx context.Context, t *tasks.Task, fx *FixResult, files []string, rc invoke.RunContext) ([]tasks.Task, error) {
	return s.taskDeps.Decompose(ctx, t, fx, files, rc)
}
func (s *stubReviewDeps) Handoff(ctx context.Context, t *tasks.Task) (string, error) {
	return s.taskDeps.Handoff(ctx, t)
}
func (s *stubReviewDeps) PreflightEnabled() bool { return s.taskDeps.PreflightEnabled() }
func (s *stubReviewDeps) Preflight(ctx context.Context, t *tasks.Task) preflight.Verdict {
	return s.taskDeps.Preflight(ctx, t)
}
func (s *stubReviewDeps) CycleBaseSHA(ctx context.Context) (string, error) {
	if s.cycleBaseSHA != nil {
		return s.cycleBaseSHA()
	}
	return "", nil
}
func (s *stubReviewDeps) CycleVerification(ctx context.Context, rc invoke.RunContext) (string, error) {
	if s.verification != nil {
		return s.verification(rc.Cycle), nil
	}
	return "", nil
}
func (s *stubReviewDeps) RunReviewer(ctx context.Context, rc invoke.RunContext, report string) (results.ReviewerResult, error) {
	s.cycles = append(s.cycles, rc.Cycle)
	s.bases = append(s.bases, rc.CycleBaseSHA)
	s.reports = append(s.reports, report)
	return s.reviewer(rc.Cycle), nil
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

func TestReview_PassesCycleBaseSHAToReviewer(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{{ID: "T001", Status: tasks.StatusPending, Priority: 1}}}
	td := &stubTaskDeps{
		fix:       func(string) *FixResult { return &FixResult{Status: FixDone, Iterations: 1} },
		fullSuite: func() error { return nil },
		commit:    func(string) (string, error) { return "new-head", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}
	const base = "0123456789abcdef0123456789abcdef01234567"
	d := &stubReviewDeps{
		taskDeps:     td,
		cycleBaseSHA: func() (string, error) { return base, nil },
		reviewer:     func(int) results.ReviewerResult { return results.ReviewerResult{ShouldStop: boolPtr(true)} },
	}

	if err := RunReviewLoop(context.Background(), tl, ReviewConfig{MaxCycles: 1}, d); err != nil {
		t.Fatal(err)
	}
	if len(d.bases) != 1 {
		t.Fatalf("reviewer calls = %d, want 1", len(d.bases))
	}
	if d.bases[0] != base {
		t.Fatalf("CycleBaseSHA = %q, want %q", d.bases[0], base)
	}
}

// TestReview_PassesVerificationReportToReviewer asserts the end-of-cycle
// verification report is produced after the task loop and handed to the
// reviewer in the same cycle.
func TestReview_PassesVerificationReportToReviewer(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{{ID: "T001", Status: tasks.StatusPending, Priority: 1}}}
	td := &stubTaskDeps{
		fix:       func(string) *FixResult { return &FixResult{Status: FixDone, Iterations: 1} },
		fullSuite: func() error { return nil },
		commit:    func(string) (string, error) { return "x", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}
	d := &stubReviewDeps{
		taskDeps:     td,
		verification: func(cycle int) string { return "FAIL: /healthz returned 500" },
		reviewer:     func(int) results.ReviewerResult { return results.ReviewerResult{ShouldStop: boolPtr(true)} },
	}
	if err := RunReviewLoop(context.Background(), tl, ReviewConfig{MaxCycles: 1}, d); err != nil {
		t.Fatal(err)
	}
	if len(d.reports) != 1 || d.reports[0] != "FAIL: /healthz returned 500" {
		t.Fatalf("reports = %v", d.reports)
	}
}
