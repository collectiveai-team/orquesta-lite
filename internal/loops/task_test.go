package loops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/invoke"
	"github.com/lionelchamorro/orquestalite/internal/preflight"
	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

type stubTaskDeps struct {
	fix                func(taskID string) *FixResult
	fullSuite          func() error
	commit             func(msg string) (string, error)
	rollback           func() error
	saveTasks          func(*tasks.TaskList) error
	decompose          func(t *tasks.Task, fx *FixResult) ([]tasks.Task, error)
	handoff            func(t *tasks.Task) (string, error)
	runSingle          func(role string) (SingleOutcome, error)
	hasRole            func(role string) bool
	preflightEnabled   bool
	preflightVerdict   preflight.Verdict
	commits            []string
	rollbacks          int
	fixCalled          int
	singleCalls        []string // roles passed to RunSingle
	fullSuiteCalls     int
	lastDecomposeFiles []string // captures filesChangedSoFar passed to Decompose
}

func (s *stubTaskDeps) PreflightEnabled() bool { return s.preflightEnabled }
func (s *stubTaskDeps) Preflight(_ context.Context, _ *tasks.Task) preflight.Verdict {
	return s.preflightVerdict
}

func (s *stubTaskDeps) RunFix(ctx context.Context, taskID string, rc invoke.RunContext) (*FixResult, error) {
	s.fixCalled++
	return s.fix(taskID), nil
}
func (s *stubTaskDeps) FullSuite(ctx context.Context) error { s.fullSuiteCalls++; return s.fullSuite() }
func (s *stubTaskDeps) RunSingle(ctx context.Context, role string, rc invoke.RunContext) (SingleOutcome, error) {
	s.singleCalls = append(s.singleCalls, role)
	if s.runSingle != nil {
		return s.runSingle(role)
	}
	return SingleOutcome{Status: "done"}, nil
}
func (s *stubTaskDeps) HasRole(role string) bool {
	if s.hasRole != nil {
		return s.hasRole(role)
	}
	return true
}
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
func (s *stubTaskDeps) Decompose(ctx context.Context, t *tasks.Task, fx *FixResult, filesChangedSoFar []string, rc invoke.RunContext) ([]tasks.Task, error) {
	s.lastDecomposeFiles = filesChangedSoFar
	if s.decompose != nil {
		return s.decompose(t, fx)
	}
	return nil, ErrNoDecomposer
}
func (s *stubTaskDeps) Handoff(ctx context.Context, t *tasks.Task) (string, error) {
	if s.handoff != nil {
		return s.handoff(t)
	}
	return "/tmp/handoff-" + t.ID + ".md", nil
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

func TestTaskLoop_NothingToCommitMarksDoneNoOp(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Title: "already satisfied", Status: tasks.StatusPending, Priority: 1},
	}}
	d := &stubTaskDeps{
		fix:       func(id string) *FixResult { return &FixResult{Status: FixDone, Iterations: 1} },
		fullSuite: func() error { return nil },
		// Coder ran and tests passed, but a prior task already produced these
		// files: there is no net diff, so Commit reports ErrNothingToCommit.
		commit:    func(msg string) (string, error) { return "", ErrNothingToCommit },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}

	if err := RunTaskLoop(context.Background(), tl, d); err != nil {
		t.Fatal(err)
	}
	if tl.Tasks[0].Status != tasks.StatusDone {
		t.Errorf("no-op task should be Done (work already satisfied), got %s", tl.Tasks[0].Status)
	}
	if tl.Tasks[0].VerifyState != tasks.VerifyCommitEmpty {
		t.Errorf("expected verify=commit_empty, got %q", tl.Tasks[0].VerifyState)
	}
	if tl.Tasks[0].FailureReason != nil {
		t.Errorf("no-op should have no failure_reason, got %v", *tl.Tasks[0].FailureReason)
	}
	if d.rollbacks != 0 {
		t.Errorf("no-op must not roll back work, got %d rollbacks", d.rollbacks)
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

// TestRunTaskLoop_DecomposeFailureFallsThroughToNeedsHuman verifies that when
// Decompose returns ErrNoDecomposer, the original task is marked needs_human
// (not failed) and a handoff is triggered.
func TestRunTaskLoop_DecomposeFailureFallsThroughToNeedsHuman(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Title: "hard task", Status: tasks.StatusPending, Priority: 1},
	}}
	handoffCalled := false
	d := &stubTaskDeps{
		fix: func(string) *FixResult {
			return &FixResult{Status: FixFailed, Reason: "agent_repeated_failure", LastFeedback: "no luck"}
		},
		fullSuite: func() error { return nil },
		commit:    func(string) (string, error) { return "", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
		// decompose is nil → returns ErrNoDecomposer
		handoff: func(task *tasks.Task) (string, error) {
			handoffCalled = true
			return "/tmp/handoff-T001.md", nil
		},
	}

	if err := RunTaskLoop(context.Background(), tl, d); err != nil {
		t.Fatal(err)
	}

	if tl.Tasks[0].Status != tasks.StatusNeedsHuman {
		t.Errorf("status = %s, want needs_human", tl.Tasks[0].Status)
	}
	if tl.Tasks[0].FailureReason == nil || *tl.Tasks[0].FailureReason != tasks.ReasonAgentRepeatedFail {
		t.Errorf("failure_reason = %v, want agent_repeated_failure", tl.Tasks[0].FailureReason)
	}
	if tl.Tasks[0].FailureDetails == nil {
		t.Fatal("FailureDetails is nil, want populated")
	}
	if !tl.Tasks[0].FailureDetails.TaskSuspect {
		t.Errorf("TaskSuspect = false, want true")
	}
	if tl.Tasks[0].FailureDetails.HandoffPath != "/tmp/handoff-T001.md" {
		t.Errorf("HandoffPath = %q, want /tmp/handoff-T001.md", tl.Tasks[0].FailureDetails.HandoffPath)
	}
	if !handoffCalled {
		t.Errorf("Handoff was not called")
	}
	if len(tl.Tasks) != 1 {
		t.Errorf("task count = %d, want 1 (no subtasks appended)", len(tl.Tasks))
	}
}

// TestRunTaskLoop_HandoffWhenDecomposeUnavailable is a focused test verifying
// that when Decompose returns ErrNoDecomposer (no handoff func override),
// the task still ends up needs_human with FailureDetails populated.
func TestRunTaskLoop_HandoffWhenDecomposeUnavailable(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Title: "big task", Status: tasks.StatusPending, Priority: 1},
	}}
	d := &stubTaskDeps{
		fix: func(string) *FixResult {
			return &FixResult{Status: FixFailed, Reason: "agent_repeated_failure", LastFeedback: "still broken"}
		},
		fullSuite: func() error { return nil },
		commit:    func(string) (string, error) { return "", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
		// decompose nil → ErrNoDecomposer; handoff nil → default stub path
	}

	if err := RunTaskLoop(context.Background(), tl, d); err != nil {
		t.Fatal(err)
	}

	task := tl.Tasks[0]
	if task.Status != tasks.StatusNeedsHuman {
		t.Errorf("status = %s, want needs_human", task.Status)
	}
	if task.FailureDetails == nil {
		t.Fatal("FailureDetails is nil")
	}
	if task.FailureDetails.HandoffPath == "" {
		t.Errorf("HandoffPath is empty, want a path")
	}
}

// TestRunTaskLoop_PreflightSkipsTaskWhenInvalid verifies that when preflight is
// enabled and returns OK=false, the task is marked needs_clarification, RunFix
// is not called, and attempts stays at 0.
func TestRunTaskLoop_PreflightSkipsTaskWhenInvalid(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Title: "bad task", Status: tasks.StatusPending, Priority: 1},
	}}
	reason := "description too short (<50 chars after trim)"
	d := &stubTaskDeps{
		preflightEnabled: true,
		preflightVerdict: preflight.Verdict{OK: false, Reason: reason},
		fix:              func(string) *FixResult { return &FixResult{Status: FixDone} },
		fullSuite:        func() error { return nil },
		commit:           func(string) (string, error) { return "x", nil },
		rollback:         func() error { return nil },
		saveTasks:        func(*tasks.TaskList) error { return nil },
	}

	if err := RunTaskLoop(context.Background(), tl, d); err != nil {
		t.Fatal(err)
	}

	task := tl.Tasks[0]
	if task.Status != tasks.StatusNeedsClarification {
		t.Errorf("status = %s, want needs_clarification", task.Status)
	}
	if task.LastFeedback == nil || *task.LastFeedback != reason {
		t.Errorf("LastFeedback = %v, want %q", task.LastFeedback, reason)
	}
	if task.Attempts != 0 {
		t.Errorf("Attempts = %d, want 0", task.Attempts)
	}
	if d.fixCalled != 0 {
		t.Errorf("RunFix called %d times, want 0", d.fixCalled)
	}
}

// TestRunTaskLoop_PreflightDisabledRunsAsBefore verifies that when preflight is
// disabled, RunFix is called normally.
func TestRunTaskLoop_PreflightDisabledRunsAsBefore(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Title: "normal task", Status: tasks.StatusPending, Priority: 1},
	}}
	d := &stubTaskDeps{
		preflightEnabled: false,
		// preflightVerdict left as zero value (OK=false) to confirm it is never consulted
		fix:       func(string) *FixResult { return &FixResult{Status: FixDone, Iterations: 1} },
		fullSuite: func() error { return nil },
		commit:    func(string) (string, error) { return "sha", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}

	if err := RunTaskLoop(context.Background(), tl, d); err != nil {
		t.Fatal(err)
	}

	if tl.Tasks[0].Status != tasks.StatusDone {
		t.Errorf("status = %s, want done", tl.Tasks[0].Status)
	}
	if d.fixCalled != 1 {
		t.Errorf("RunFix called %d times, want 1", d.fixCalled)
	}
}

// TestRunTaskLoop_DecomposeErrorRecordedInHandoff verifies Fix 1: when Decompose
// returns an unexpected error (not ErrNoDecomposer), the error message is captured
// in FailureDetails.LastStderrTail so the handoff document records what failed.
func TestRunTaskLoop_DecomposeErrorRecordedInHandoff(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Title: "failing task", Status: tasks.StatusPending, Priority: 1},
	}}
	d := &stubTaskDeps{
		fix: func(string) *FixResult {
			return &FixResult{Status: FixFailed, Reason: "agent_repeated_failure", LastFeedback: "stuck"}
		},
		fullSuite: func() error { return nil },
		commit:    func(string) (string, error) { return "", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
		decompose: func(*tasks.Task, *FixResult) ([]tasks.Task, error) {
			return nil, errors.New("agent timeout")
		},
	}

	if err := RunTaskLoop(context.Background(), tl, d); err != nil {
		t.Fatal(err)
	}

	task := tl.Tasks[0]
	if task.Status != tasks.StatusNeedsHuman {
		t.Errorf("status = %s, want needs_human", task.Status)
	}
	if task.FailureDetails == nil {
		t.Fatal("FailureDetails is nil, want populated")
	}
	tail := task.FailureDetails.LastStderrTail
	if !strings.Contains(tail, "decompose failed") {
		t.Errorf("LastStderrTail = %q, want it to contain \"decompose failed\"", tail)
	}
	if !strings.Contains(tail, "agent timeout") {
		t.Errorf("LastStderrTail = %q, want it to contain \"agent timeout\"", tail)
	}
}

// TestRunTaskLoop_FilesChangedSoFarPassedToDecompose verifies Fix 2: the
// filesChangedSoFar slice from FixResult is passed through to Decompose.
func TestRunTaskLoop_FilesChangedSoFarPassedToDecompose(t *testing.T) {
	wantFiles := []string{"foo.go", "bar.go"}
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Title: "task", Status: tasks.StatusPending, Priority: 1},
	}}
	d := &stubTaskDeps{
		fix: func(string) *FixResult {
			return &FixResult{
				Status:            FixFailed,
				Reason:            "agent_repeated_failure",
				FilesChangedSoFar: wantFiles,
			}
		},
		fullSuite: func() error { return nil },
		commit:    func(string) (string, error) { return "", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
		// decompose returns ErrNoDecomposer so the task falls through to handoff
	}

	if err := RunTaskLoop(context.Background(), tl, d); err != nil {
		t.Fatal(err)
	}

	if len(d.lastDecomposeFiles) != len(wantFiles) {
		t.Fatalf("Decompose received %d files, want %d", len(d.lastDecomposeFiles), len(wantFiles))
	}
	for i, f := range wantFiles {
		if d.lastDecomposeFiles[i] != f {
			t.Errorf("Decompose files[%d] = %q, want %q", i, d.lastDecomposeFiles[i], f)
		}
	}
}

func TestTaskLoop_SetupSquadRunsCoderOnlyNoFullSuite(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Squad: tasks.SquadSetup, Status: tasks.StatusPending, Priority: 1},
	}}
	d := &stubTaskDeps{
		fix:       func(id string) *FixResult { t.Fatalf("RunFix must not run for setup"); return nil },
		fullSuite: func() error { return nil },
		commit:    func(msg string) (string, error) { return "sha", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}
	if err := RunTaskLoop(context.Background(), tl, d); err != nil {
		t.Fatal(err)
	}
	if len(d.singleCalls) != 1 || d.singleCalls[0] != "coder" {
		t.Errorf("setup should call RunSingle(coder), got %v", d.singleCalls)
	}
	if d.fullSuiteCalls != 0 {
		t.Errorf("setup must skip FullSuite, got %d calls", d.fullSuiteCalls)
	}
	if len(d.commits) != 1 {
		t.Errorf("setup should commit once, got %d", len(d.commits))
	}
	if tl.Tasks[0].Status != tasks.StatusDone {
		t.Errorf("setup task should be Done, got %s", tl.Tasks[0].Status)
	}
}

func TestTaskLoop_GenericSquadRunsGeneralist(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Squad: tasks.SquadGeneric, Status: tasks.StatusPending, Priority: 1},
	}}
	d := &stubTaskDeps{
		fix:       func(id string) *FixResult { t.Fatalf("RunFix must not run for generic"); return nil },
		fullSuite: func() error { return nil },
		commit:    func(msg string) (string, error) { return "sha", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}
	if err := RunTaskLoop(context.Background(), tl, d); err != nil {
		t.Fatal(err)
	}
	if len(d.singleCalls) != 1 || d.singleCalls[0] != "generalist" {
		t.Errorf("generic should call RunSingle(generalist), got %v", d.singleCalls)
	}
	if d.fullSuiteCalls != 0 {
		t.Errorf("generic must skip FullSuite")
	}
	if tl.Tasks[0].Status != tasks.StatusDone {
		t.Errorf("generic task should be Done, got %s", tl.Tasks[0].Status)
	}
}

func TestTaskLoop_GenericFallsBackToFullWhenNoGeneralist(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Squad: tasks.SquadGeneric, Status: tasks.StatusPending, Priority: 1},
	}}
	d := &stubTaskDeps{
		fix:       func(id string) *FixResult { return &FixResult{Status: FixDone, Iterations: 1} },
		fullSuite: func() error { return nil },
		commit:    func(msg string) (string, error) { return "sha", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
		hasRole:   func(role string) bool { return role != "generalist" },
	}
	if err := RunTaskLoop(context.Background(), tl, d); err != nil {
		t.Fatal(err)
	}
	if len(d.singleCalls) != 0 {
		t.Errorf("no generalist → must not call RunSingle, got %v", d.singleCalls)
	}
	if d.fixCalled != 1 {
		t.Errorf("no generalist → generic falls back to RunFix, fixCalled=%d", d.fixCalled)
	}
	if tl.Tasks[0].Status != tasks.StatusDone {
		t.Errorf("fallback task should be Done, got %s", tl.Tasks[0].Status)
	}
}

func TestTaskLoop_SingleRoleFailureMarksFailed(t *testing.T) {
	tl := &tasks.TaskList{Tasks: []tasks.Task{
		{ID: "T001", Squad: tasks.SquadSetup, Status: tasks.StatusPending, Priority: 1},
	}}
	d := &stubTaskDeps{
		runSingle: func(role string) (SingleOutcome, error) {
			return SingleOutcome{Status: "failed", Summary: "blocked"}, nil
		},
		fullSuite: func() error { return nil },
		commit:    func(msg string) (string, error) { return "sha", nil },
		rollback:  func() error { return nil },
		saveTasks: func(*tasks.TaskList) error { return nil },
	}
	_ = RunTaskLoop(context.Background(), tl, d)
	if tl.Tasks[0].Status != tasks.StatusFailed {
		t.Errorf("failed RunSingle → task Failed, got %s", tl.Tasks[0].Status)
	}
	if len(d.commits) != 0 {
		t.Errorf("failed single-role task must not commit")
	}
	if d.rollbacks != 1 {
		t.Errorf("failed single-role task should roll back, got %d", d.rollbacks)
	}
}
