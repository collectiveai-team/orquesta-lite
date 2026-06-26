package tasks

import (
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tasks.json")

	want := &TaskList{Tasks: []Task{
		{ID: "T001", Title: "first", Description: "do X", Status: StatusPending, Priority: 1},
	}}
	if err := Save(p, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tasks) != 1 || got.Tasks[0].ID != "T001" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestNextPendingByPriority(t *testing.T) {
	tl := &TaskList{Tasks: []Task{
		{ID: "T001", Status: StatusDone, Priority: 1},
		{ID: "T002", Status: StatusPending, Priority: 5},
		{ID: "T003", Status: StatusPending, Priority: 2},
	}}
	got := tl.NextPending()
	if got == nil || got.ID != "T003" {
		t.Fatalf("NextPending = %+v, want T003", got)
	}
}

func TestAnyPending(t *testing.T) {
	tl := &TaskList{Tasks: []Task{{Status: StatusDone}, {Status: StatusFailed}}}
	if tl.AnyPending() {
		t.Errorf("AnyPending = true, want false")
	}
	tl.Tasks = append(tl.Tasks, Task{Status: StatusPending})
	if !tl.AnyPending() {
		t.Errorf("AnyPending = false, want true")
	}
}

func TestResetFailedToPending(t *testing.T) {
	reason := ReasonCommitRejected
	tl := &TaskList{Tasks: []Task{
		{ID: "T1", Status: StatusDone, VerifyState: VerifyCommitOK},
		{ID: "T2", Status: StatusFailed, FailureReason: &reason, VerifyState: VerifyCommitRejected,
			FailureDetails: &FailureDetails{Reason: reason}},
		{ID: "T3", Status: StatusPending},
	}}

	n := tl.ResetFailedToPending()

	if n != 1 {
		t.Fatalf("ResetFailedToPending = %d, want 1", n)
	}
	if tl.Tasks[1].Status != StatusPending {
		t.Errorf("failed task should be pending, got %s", tl.Tasks[1].Status)
	}
	if tl.Tasks[1].FailureReason != nil || tl.Tasks[1].FailureDetails != nil {
		t.Errorf("failed task should have its failure bookkeeping cleared")
	}
	if tl.Tasks[1].VerifyState != VerifyUnknown {
		t.Errorf("failed task verify_state should be cleared, got %q", tl.Tasks[1].VerifyState)
	}
	if tl.Tasks[0].Status != StatusDone {
		t.Errorf("done task must be left untouched, got %s", tl.Tasks[0].Status)
	}
}

func TestNextID(t *testing.T) {
	tl := &TaskList{Tasks: []Task{{ID: "T001"}, {ID: "T007"}, {ID: "T003"}}}
	if got := tl.NextID(); got != "T008" {
		t.Errorf("NextID = %q, want T008", got)
	}
	empty := &TaskList{}
	if got := empty.NextID(); got != "T001" {
		t.Errorf("empty NextID = %q, want T001", got)
	}
}

func TestAppendAssignsIDs(t *testing.T) {
	tl := &TaskList{Tasks: []Task{{ID: "T001"}}}
	added := tl.Append([]Task{
		{Title: "x", Description: "y", Priority: 1},
		{Title: "z", Description: "w", Priority: 2},
	}, 1)
	if len(added) != 2 || added[0].ID != "T002" || added[1].ID != "T003" {
		t.Fatalf("Append IDs = %+v", added)
	}
	if added[0].Status != StatusPending || added[0].CreatedInReviewCycle != 1 {
		t.Errorf("Append did not initialise status/cycle: %+v", added[0])
	}
}
