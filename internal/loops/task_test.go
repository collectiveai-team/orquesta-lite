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
	decompose func(t *tasks.Task, fx *FixResult) ([]tasks.Task, error)
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
func (s *stubTaskDeps) Decompose(ctx context.Context, t *tasks.Task, fx *FixResult, _ []string) ([]tasks.Task, error) {
	if s.decompose != nil {
		return s.decompose(t, fx)
	}
	return nil, ErrNoDecomposer
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

// TestRunTaskLoop_DecomposesOnRepeatedFailure verifies that when the fix loop
// returns agent_repeated_failure, the task loop calls Decompose, marks the
// original task as decomposed, and appends the subtasks.
// The fix stub returns agent_repeated_failure only for the original task ID;
// subtasks succeed so the loop terminates cleanly.
func TestRunTaskLoop_DecomposesOnRepeatedFailure(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Title: "big task", Status: tasks.StatusPending, Priority: 1},
	}}
	subtaskDefs := []tasks.Task{
		{Title: "sub1", Description: "do part 1 acceptance: x", Priority: 1},
		{Title: "sub2", Description: "do part 2 acceptance: y", Priority: 2},
		{Title: "sub3", Description: "do part 3 acceptance: z", Priority: 3},
	}
	d := &stubTaskDeps{
		fix: func(id string) *FixResult {
			if id == "T001" {
				return &FixResult{Status: FixFailed, Reason: "agent_repeated_failure", LastFeedback: "still broken"}
			}
			return &FixResult{Status: FixDone, Iterations: 1}
		},
		fullSuite: func() error { return nil },
		commit:    func(string) (string, error) { return "sha", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
		decompose: func(orig *tasks.Task, fx *FixResult) ([]tasks.Task, error) {
			return subtaskDefs, nil
		},
	}

	if err := RunTaskLoop(context.Background(), tl, d); err != nil {
		t.Fatal(err)
	}

	if tl.Tasks[0].Status != tasks.StatusDecomposed {
		t.Errorf("original task status = %s, want decomposed", tl.Tasks[0].Status)
	}
	if len(tl.Tasks[0].DecomposedIntoIDs) != 3 {
		t.Fatalf("DecomposedIntoIDs len = %d, want 3", len(tl.Tasks[0].DecomposedIntoIDs))
	}
	// Three subtasks should have been appended (total = 4).
	if len(tl.Tasks) != 4 {
		t.Fatalf("total tasks = %d, want 4 (1 original + 3 subtasks)", len(tl.Tasks))
	}
	// Verify IDs in DecomposedIntoIDs match the appended subtask IDs.
	for i, id := range tl.Tasks[0].DecomposedIntoIDs {
		if tl.Tasks[i+1].ID != id {
			t.Errorf("DecomposedIntoIDs[%d]=%s but tasks[%d].ID=%s", i, id, i+1, tl.Tasks[i+1].ID)
		}
	}
	// Subtasks should all be done.
	for _, sub := range tl.Tasks[1:] {
		if sub.Status != tasks.StatusDone {
			t.Errorf("subtask %s status = %s, want done", sub.ID, sub.Status)
		}
	}
	// Rollback must have been called at least once (for the original task).
	if d.rollbacks == 0 {
		t.Errorf("expected at least 1 rollback, got 0")
	}
}

// TestRunTaskLoop_DecomposeFailureFallsThroughToFailed verifies that when
// Decompose returns ErrNoDecomposer, the original task is marked failed with
// reason agent_repeated_failure.
func TestRunTaskLoop_DecomposeFailureFallsThroughToFailed(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Title: "hard task", Status: tasks.StatusPending, Priority: 1},
	}}
	d := &stubTaskDeps{
		fix: func(string) *FixResult {
			return &FixResult{Status: FixFailed, Reason: "agent_repeated_failure", LastFeedback: "no luck"}
		},
		fullSuite: func() error { return nil },
		commit:    func(string) (string, error) { return "", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
		// decompose is nil → returns ErrNoDecomposer
	}

	if err := RunTaskLoop(context.Background(), tl, d); err != nil {
		t.Fatal(err)
	}

	if tl.Tasks[0].Status != tasks.StatusFailed {
		t.Errorf("status = %s, want failed", tl.Tasks[0].Status)
	}
	if tl.Tasks[0].FailureReason == nil || *tl.Tasks[0].FailureReason != tasks.ReasonAgentRepeatedFail {
		t.Errorf("failure_reason = %v, want agent_repeated_failure", tl.Tasks[0].FailureReason)
	}
	if len(tl.Tasks) != 1 {
		t.Errorf("task count = %d, want 1 (no subtasks appended)", len(tl.Tasks))
	}
}
