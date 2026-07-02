package cost

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeLog(t *testing.T, lines string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "run.log")
	if err := os.WriteFile(p, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunsFromLog(t *testing.T) {
	p := writeLog(t, `{"event":"agent_run","ts":"2026-06-11T10:00:00Z","task_id":"T001","role":"coder","agent":"a1","session_id":"s1"}
{"event":"task_done","task_id":"T001"}
not json at all
{"event":"agent_run","ts":"2026-06-11T10:05:00Z","task_id":"T001","role":"tester","agent":"a1","session_id":"s2"}
{"event":"agent_run","ts":"2026-06-11T10:06:00Z","task_id":"T002","role":"coder","agent":"a1","provider":"claude","model":"claude-sonnet-4-6","input_tokens":200,"output_tokens":20}
`)
	runs, err := RunsFromLog(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 || runs[0].SessionID != "s1" || runs[1].Role != "tester" {
		t.Fatalf("runs = %+v", runs)
	}
	if runs[2].SessionID != "" || runs[2].Model != "claude-sonnet-4-6" || runs[2].InputTok != 200 || runs[2].OutputTok != 20 {
		t.Fatalf("third run = %+v", runs[2])
	}
}

func TestRunsFromLog_MissingFile(t *testing.T) {
	runs, err := RunsFromLog(filepath.Join(t.TempDir(), "nope.log"))
	if err != nil || runs != nil {
		t.Fatalf("got %v, %v", runs, err)
	}
}

func sessionsFixture() map[string]Session {
	return map[string]Session{
		"s1": {SessionID: "s1", Cost: Money{Total: 0.50}, Tokens: Tokens{Input: 100, CachedInput: 900, Output: 50}},
		"s2": {SessionID: "s2", Cost: Money{Total: 0.25}, Tokens: Tokens{Input: 40, Output: 10}},
	}
}

func TestRollup(t *testing.T) {
	runs := []AgentRun{
		{TaskID: "T001", SessionID: "s1"},
		{TaskID: "T001", SessionID: "s2", InputTok: 20, OutputTok: 5},
		{TaskID: "T002", Model: "claude-sonnet-4-6", InputTok: 1000, OutputTok: 100},
		{TaskID: "T003", SessionID: "unknown"},
	}
	rep := Rollup(runs, sessionsFixture())
	if len(rep.Tasks) != 3 {
		t.Fatalf("tasks = %+v", rep.Tasks)
	}
	t1 := rep.Tasks[0]
	if t1.TaskID != "T001" || t1.Runs != 2 || t1.Priced != 2 || t1.TotalUSD != 0.75 {
		t.Errorf("t1 = %+v", t1)
	}
	if t1.InputTok != 1020 || t1.OutputTok != 55 {
		t.Errorf("t1 tokens = %+v", t1)
	}
	t2 := rep.Tasks[1]
	if t2.Runs != 1 || t2.Priced != 1 || t2.TotalUSD == 0 {
		t.Errorf("t2 = %+v", t2)
	}
	t3 := rep.Tasks[2]
	if t3.Runs != 1 || t3.Priced != 0 || t3.TotalUSD != 0 {
		t.Errorf("t3 = %+v", t3)
	}
	if rep.TotalUSD <= 0.75 || rep.Runs != 4 || rep.Priced != 3 {
		t.Errorf("report = %+v", rep)
	}
}

func TestSpendSince(t *testing.T) {
	t0 := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	runs := []AgentRun{
		{TS: t0.Add(-time.Hour), SessionID: "s1"},         // before window
		{TS: t0.Add(time.Minute), SessionID: "s2"},        // counted
		{TS: t0.Add(2 * time.Minute), SessionID: "s2"},    // duplicate session, not double-counted
		{TS: t0.Add(3 * time.Minute), SessionID: "ghost"}, // unpriced
	}
	got := SpendSince(runs, sessionsFixture(), t0)
	if got != 0.25 {
		t.Errorf("SpendSince = %v, want 0.25", got)
	}
}
