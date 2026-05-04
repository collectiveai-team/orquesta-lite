package loops

import (
	"context"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

type stubTaskDeps struct {
	fix       func(taskID string) *FixResult
	fullSuite func() error
	commit    func(msg string) (string, error)
	rollback  func() error
	saveTasks func(*tasks.TaskList) error
	commits   []string
	rollbacks int
}

func (s *stubTaskDeps) RunFix(ctx context.Context, taskID string) (*FixResult, error) {
	return s.fix(taskID), nil
}
func (s *stubTaskDeps) FullSuite(ctx context.Context) error { return s.fullSuite() }
func (s *stubTaskDeps) Commit(ctx context.Context, msg string) (string, error) {
	sha, err := s.commit(msg)
	s.commits = append(s.commits, msg)
	return sha, err
}
func (s *stubTaskDeps) Rollback(ctx context.Context) error {
	s.rollbacks++
	return s.rollback()
}
func (s *stubTaskDeps) SaveTasks(ctx context.Context, tl *tasks.TaskList) error {
	return s.saveTasks(tl)
}

func TestTaskLoop_HappyPath(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Title: "first", Status: tasks.StatusPending, Priority: 1},
	}}
	d := &stubTaskDeps{
		fix:       func(id string) *FixResult { return &FixResult{Status: FixDone, Iterations: 1} },
		fullSuite: func() error { return nil },
		commit:    func(msg string) (string, error) { return "abc1234", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}

	if err := RunTaskLoop(context.Background(), tl, d); err != nil {
		t.Fatal(err)
	}
	if tl.Tasks[0].Status != tasks.StatusDone {
		t.Errorf("expected Done, got %s", tl.Tasks[0].Status)
	}
	if len(d.commits) != 1 {
		t.Errorf("commits=%d", len(d.commits))
	}
}

func TestTaskLoop_FullSuiteFailureRollsBack(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Status: tasks.StatusPending, Priority: 1},
	}}
	d := &stubTaskDeps{
		fix:       func(id string) *FixResult { return &FixResult{Status: FixDone, Iterations: 1} },
		fullSuite: func() error { return ErrFullSuiteFailed },
		commit:    func(msg string) (string, error) { return "x", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}
	_ = RunTaskLoop(context.Background(), tl, d)
	if tl.Tasks[0].Status != tasks.StatusFailed {
		t.Errorf("expected Failed, got %s", tl.Tasks[0].Status)
	}
	if tl.Tasks[0].FailureReason == nil || *tl.Tasks[0].FailureReason != tasks.ReasonFullSuiteFailed {
		t.Errorf("failure_reason wrong: %+v", tl.Tasks[0].FailureReason)
	}
	if d.rollbacks != 1 {
		t.Errorf("rollback should run, got %d", d.rollbacks)
	}
}

func TestTaskLoop_FixFailedMarkedFailed(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Status: tasks.StatusPending, Priority: 1},
	}}
	d := &stubTaskDeps{
		fix: func(id string) *FixResult {
			return &FixResult{Status: FixFailed, Reason: "max_iterations", Iterations: 5}
		},
		fullSuite: func() error { return nil },
		commit:    func(string) (string, error) { return "", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}
	_ = RunTaskLoop(context.Background(), tl, d)
	if tl.Tasks[0].Status != tasks.StatusFailed || *tl.Tasks[0].FailureReason != tasks.ReasonMaxIterations {
		t.Errorf("got status=%s reason=%v", tl.Tasks[0].Status, tl.Tasks[0].FailureReason)
	}
}

func TestTaskLoop_SkipsAlreadyDone(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Status: tasks.StatusDone, Priority: 1},
		{ID: "T002", Status: tasks.StatusPending, Priority: 2},
	}}
	called := 0
	d := &stubTaskDeps{
		fix:       func(string) *FixResult { called++; return &FixResult{Status: FixDone, Iterations: 1} },
		fullSuite: func() error { return nil },
		commit:    func(string) (string, error) { return "x", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}
	_ = RunTaskLoop(context.Background(), tl, d)
	if called != 1 {
		t.Errorf("fix should run once for T002, ran %d times", called)
	}
}
