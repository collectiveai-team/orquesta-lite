package web

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func writeRunLog(t *testing.T, dir, content string) {
	t.Helper()
	path := filepath.Join(dir, ".orquestalite", "run.log")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const queryFixtureLog = `{"ts":"2026-07-01T10:00:00Z","event":"run_start","run_id":"r1","command":"run","args":["--feature","F1"],"orq_version":"v0.2.0"}
{"ts":"2026-07-01T10:00:05Z","event":"agent_run","run_id":"r1","role":"coder","agent":"sonnet","task_id":"T1","cycle":1,"attempt":1,"provider":"claude","model":"claude-sonnet-4-6","duration_s":42,"exit_code":0,"timed_out":false,"rate_limited":false,"input_tokens":1000,"output_tokens":500,"cached_input_tokens":200,"artifacts_dir":".orquestalite/runs/r1/a"}
{"ts":"2026-07-01T10:00:06Z","event":"task_done","run_id":"r1","task_id":"T1","commit_sha":"abc"}
{"ts":"2026-07-01T10:00:07Z","event":"run_end","run_id":"r1","status":"ok","duration_s":7,"orq_version":"v0.2.0"}
{"ts":"2026-07-01T11:00:00Z","event":"run_start","run_id":"r2","command":"factory","orq_version":"v0.2.0"}
`

func getJSON(t *testing.T, srv *Server, url string, into any) int {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%s: Cache-Control = %q, want no-store", url, rec.Header().Get("Cache-Control"))
	}
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("%s: bad JSON %q: %v", url, rec.Body.String(), err)
	}
	return rec.Code
}

func TestAPIRuns_NewestFirstWithActiveFilter(t *testing.T) {
	dir := stateDir(t)
	writeRunLog(t, dir, queryFixtureLog)
	srv := &Server{Dir: dir}

	var resp struct {
		Runs []struct {
			RunID   string   `json:"run_id"`
			Status  string   `json:"status"`
			Args    []string `json:"args"`
			CostUSD float64  `json:"cost_usd"`
		} `json:"runs"`
		Total int `json:"total"`
	}
	if code := getJSON(t, srv, "/api/runs", &resp); code != 200 {
		t.Fatalf("code = %d", code)
	}
	if resp.Total != 2 || len(resp.Runs) != 2 || resp.Runs[0].RunID != "r2" {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Runs[1].CostUSD == 0 {
		t.Fatalf("r1 cost missing: %+v", resp.Runs[1])
	}

	if code := getJSON(t, srv, "/api/runs?active=true", &resp); code != 200 {
		t.Fatalf("code = %d", code)
	}
	if resp.Total != 1 || resp.Runs[0].RunID != "r2" || resp.Runs[0].Status != "running" {
		t.Fatalf("active resp = %+v", resp)
	}

	// Garbage limit/offset fall back to defaults, never error.
	if code := getJSON(t, srv, "/api/runs?limit=banana&offset=-3", &resp); code != 200 || resp.Total != 2 {
		t.Fatalf("garbage pagination: code=%d resp=%+v", code, resp)
	}
}

func TestAPIRun_ByIDAnd404(t *testing.T) {
	dir := stateDir(t)
	writeRunLog(t, dir, queryFixtureLog)
	srv := &Server{Dir: dir}

	var run struct {
		RunID     string `json:"run_id"`
		TasksDone int    `json:"tasks_done"`
	}
	if code := getJSON(t, srv, "/api/runs/r1", &run); code != 200 || run.RunID != "r1" || run.TasksDone != 1 {
		t.Fatalf("code=%d run=%+v", code, run)
	}
	var errResp struct {
		Error string `json:"error"`
	}
	if code := getJSON(t, srv, "/api/runs/nope", &errResp); code != 404 || errResp.Error == "" {
		t.Fatalf("code=%d resp=%+v", code, errResp)
	}
}

func TestAPIRuns_EmptyStateServesEmptyList(t *testing.T) {
	srv := &Server{Dir: stateDir(t)} // .orquestalite exists but no run.log
	var resp struct {
		Runs  []any `json:"runs"`
		Total int   `json:"total"`
	}
	if code := getJSON(t, srv, "/api/runs", &resp); code != 200 {
		t.Fatalf("code = %d", code)
	}
	if resp.Runs == nil || resp.Total != 0 {
		t.Fatalf("resp = %+v, want empty non-null runs", resp)
	}
}
