package eventdb

import (
	"encoding/json"
	"testing"
)

// seedTwoRuns ingests a finished run r1 (one priced agent run, one task done)
// and a still-running run r2 (one agent run with an unknown model).
func seedTwoRuns(t *testing.T) *DB {
	t.Helper()
	db, dir := newTestDB(t)
	appendLog(t, dir,
		lineRunStart+"\n"+lineAgentRun+"\n"+lineTaskDone+"\n"+lineRunEnd+"\n"+
			`{"ts":"2026-07-01T11:00:00Z","event":"run_start","run_id":"r2","command":"factory","args":["features.md"],"orq_version":"v0.2.0"}`+"\n"+
			`{"ts":"2026-07-01T11:00:10Z","event":"agent_run","run_id":"r2","role":"critic","agent":"mystery","task_id":"T9","cycle":1,"attempt":1,"provider":"other","model":"mystery-model","duration_s":5,"exit_code":0,"timed_out":false,"rate_limited":false,"input_tokens":10,"output_tokens":10}`+"\n"+
			`{"ts":"2026-07-01T11:00:11Z","event":"task_failed","run_id":"r2","task_id":"T9","failure_reason":"max_iterations","attempts":3}`+"\n")
	if err := db.Ingest(dir); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRuns_NewestFirstWithAggregates(t *testing.T) {
	db := seedTwoRuns(t)
	runs, total, err := db.Runs(RunsFilter{}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(runs) != 2 {
		t.Fatalf("total=%d len=%d, want 2/2", total, len(runs))
	}
	if runs[0].RunID != "r2" || runs[1].RunID != "r1" {
		t.Fatalf("order = %s,%s; want r2,r1", runs[0].RunID, runs[1].RunID)
	}
	r1 := runs[1]
	if r1.Status != "ok" || r1.Command != "run" || len(r1.Args) != 2 {
		t.Fatalf("r1 = %+v", r1)
	}
	if r1.AgentRuns != 1 || r1.InputTokens != 1000 || r1.OutputTokens != 500 {
		t.Fatalf("r1 aggregates = %+v", r1)
	}
	// claude-sonnet-4-6: 1000/1M*3.00 + 500/1M*15.00 = 0.0105
	if r1.CostUSD < 0.0104 || r1.CostUSD > 0.0106 {
		t.Fatalf("r1 cost = %v, want ~0.0105", r1.CostUSD)
	}
	if r1.TasksDone != 1 || r1.TasksFailed != 0 {
		t.Fatalf("r1 tasks = %d/%d", r1.TasksDone, r1.TasksFailed)
	}
	r2 := runs[0]
	if r2.Status != "running" || r2.FinishedAt != nil || r2.DurationS != nil {
		t.Fatalf("r2 = %+v", r2)
	}
	if r2.CostUSD != 0 { // unknown model prices to zero, not an error
		t.Fatalf("r2 cost = %v, want 0", r2.CostUSD)
	}
	if r2.TasksFailed != 1 {
		t.Fatalf("r2 tasks_failed = %d, want 1", r2.TasksFailed)
	}
}

func TestRuns_ActiveFilterAndPagination(t *testing.T) {
	db := seedTwoRuns(t)
	active := true
	runs, total, err := db.Runs(RunsFilter{Active: &active}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(runs) != 1 || runs[0].RunID != "r2" {
		t.Fatalf("active filter: total=%d runs=%+v", total, runs)
	}
	runs, total, err = db.Runs(RunsFilter{}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(runs) != 1 || runs[0].RunID != "r1" {
		t.Fatalf("pagination: total=%d runs=%+v", total, runs)
	}
}

func TestRun_ByIDAndUnknown(t *testing.T) {
	db := seedTwoRuns(t)
	run, err := db.Run("r1")
	if err != nil || run == nil || run.RunID != "r1" || run.TasksDone != 1 {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	run, err = db.Run("nope")
	if err != nil || run != nil {
		t.Fatalf("unknown id: run=%+v err=%v", run, err)
	}
}

func TestEvents_FiltersAndLogOrder(t *testing.T) {
	db := seedTwoRuns(t)
	events, total, err := db.Events("r1", "", "", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || len(events) != 4 {
		t.Fatalf("total=%d len=%d, want 4/4", total, len(events))
	}
	var first struct {
		Event string `json:"event"`
	}
	if err := json.Unmarshal(events[0], &first); err != nil || first.Event != "run_start" {
		t.Fatalf("first event = %s (err %v), want run_start first (log order)", events[0], err)
	}
	events, total, err = db.Events("r1", "agent_run", "T1", 50, 0)
	if err != nil || total != 1 || len(events) != 1 {
		t.Fatalf("filtered: total=%d err=%v", total, err)
	}
}

func TestAgentRuns_FiltersCostNewestFirst(t *testing.T) {
	db := seedTwoRuns(t)
	recs, total, err := db.AgentRuns(AgentRunsFilter{}, 50, 0)
	if err != nil || total != 2 {
		t.Fatalf("total=%d err=%v", total, err)
	}
	if recs[0].RunID != "r2" { // newest first
		t.Fatalf("order: first = %+v", recs[0])
	}
	recs, _, err = db.AgentRuns(AgentRunsFilter{Role: "coder"}, 50, 0)
	if err != nil || len(recs) != 1 || recs[0].TaskID != "T1" {
		t.Fatalf("role filter: %+v err=%v", recs, err)
	}
	if recs[0].CostUSD < 0.0104 || recs[0].CostUSD > 0.0106 {
		t.Fatalf("cost = %v", recs[0].CostUSD)
	}
	if recs[0].TimedOut || recs[0].RateLimited {
		t.Fatalf("bools decoded wrong (fixture has both false): %+v", recs[0])
	}
}

func TestCostStats_GroupsAndSortsDescending(t *testing.T) {
	db := seedTwoRuns(t)
	rows, err := db.CostStats("role")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Key != "coder" { // coder cost > critic (unknown model = 0)
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].AgentRuns != 1 || rows[0].InputTokens != 1000 {
		t.Fatalf("rows[0] = %+v", rows[0])
	}
	if _, err := db.CostStats("run"); err != nil {
		t.Fatal(err)
	}
}
