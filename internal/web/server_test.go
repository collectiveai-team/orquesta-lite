package web

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func stateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".orquestalite"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAPITasks_FallbackWhenAbsent(t *testing.T) {
	srv := &Server{Dir: stateDir(t)}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/tasks", nil))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"tasks":[]}` {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestAPITasks_ServesFile(t *testing.T) {
	dir := stateDir(t)
	body := `{"tasks":[{"id":"T001","title":"x","status":"done"}]}`
	if err := os.WriteFile(filepath.Join(dir, ".orquestalite", "tasks.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &Server{Dir: dir}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/tasks", nil))
	if !strings.Contains(rec.Body.String(), "T001") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestAPITasks_MidWriteServesFallback(t *testing.T) {
	dir := stateDir(t)
	if err := os.WriteFile(filepath.Join(dir, ".orquestalite", "tasks.json"), []byte(`{"tasks":[{"id":`), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &Server{Dir: dir}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/tasks", nil))
	if strings.TrimSpace(rec.Body.String()) != `{"tasks":[]}` {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestAPIFactory_NullWhenAbsent(t *testing.T) {
	srv := &Server{Dir: stateDir(t)}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/factory", nil))
	if strings.TrimSpace(rec.Body.String()) != "null" {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestIndexServed(t *testing.T) {
	srv := &Server{Dir: stateDir(t)}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "orquestalite") {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestGameboardServed(t *testing.T) {
	srv := &Server{Dir: stateDir(t)}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/gameboard", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "gameboard.js") {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestAPIAttemptDiff_ServesArtifactFromAgentDiffEvent(t *testing.T) {
	dir := stateDir(t)
	artifactsDir := filepath.Join(dir, ".orquestalite", "runs", "r20260701T120000Z-abcd", "agents", "T001", "coder.c1.a2")
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wantDiff := "diff --git a/x.txt b/x.txt\n+hello\n"
	if err := os.WriteFile(filepath.Join(artifactsDir, "attempt.diff"), []byte(wantDiff), 0o644); err != nil {
		t.Fatal(err)
	}
	logLine := `{"event":"agent_diff","task_id":"T001","role":"coder","cycle":1,"attempt":2,"artifacts_dir":".orquestalite/runs/r20260701T120000Z-abcd/agents/T001/coder.c1.a2"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".orquestalite", "run.log"), []byte(logLine), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &Server{Dir: dir}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/attempt-diff/T001/coder/1/2", nil))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"available":true`) || !strings.Contains(body, `+hello`) {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestAPIResult_ServesRoleFile(t *testing.T) {
	dir := stateDir(t)
	resultsDir := filepath.Join(dir, ".orquestalite", "results")
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"role":"coder","status":"completed"}`
	if err := os.WriteFile(filepath.Join(resultsDir, "coder.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &Server{Dir: dir}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/result/coder", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "completed") {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestAPIResult_NullWhenAbsent(t *testing.T) {
	srv := &Server{Dir: stateDir(t)}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/result/critic", nil))
	if strings.TrimSpace(rec.Body.String()) != "null" {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

// An unknown or traversal-style role segment must never read outside the
// whitelist; it returns "null" rather than reaching the filesystem.
func TestAPIResult_RejectsUnknownRole(t *testing.T) {
	dir := stateDir(t)
	// A real file that a traversal attempt might target.
	if err := os.WriteFile(filepath.Join(dir, ".orquestalite", "tasks.json"), []byte(`{"tasks":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &Server{Dir: dir}
	for _, role := range []string{"bogus", "..%2ftasks", "tasks"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/result/"+role, nil))
		if strings.TrimSpace(rec.Body.String()) != "null" {
			t.Fatalf("role=%q body=%q (expected null)", role, rec.Body.String())
		}
	}
}

func gitCommit(t *testing.T, dir, file, content, msg string) string {
	t.Helper()
	runGit := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		runGit("init", "-q")
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", file)
	runGit("commit", "-q", "-m", msg)
	return runGit("rev-parse", "HEAD")
}

func TestAPIDiff_ReturnsCommitDiffAndAgent(t *testing.T) {
	dir := stateDir(t)
	sha := gitCommit(t, dir, "rollup.go", "package capacity\n\nfunc Sum() int { return 1 }\n", "T001 rollup")
	log := `{"event":"agent_run","role":"coder","agent":"codex_gpt5","task_id":"T001"}` + "\n" +
		`{"event":"task_done","task_id":"T001","commit_sha":"` + sha + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".orquestalite", "run.log"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &Server{Dir: dir}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/diff/T001", nil))
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"available":true`) {
		t.Fatalf("code=%d body=%q", rec.Code, body)
	}
	for _, want := range []string{"rollup.go", "codex_gpt5", "func Sum"} {
		if !strings.Contains(body, want) {
			t.Fatalf("diff body missing %q: %q", want, body)
		}
	}
}

func TestAPIDiff_IncludesCoderSummaryFromArchive(t *testing.T) {
	dir := stateDir(t)
	sha := gitCommit(t, dir, "x.go", "package x\n", "T001 x")
	log := `{"event":"task_done","task_id":"T001","commit_sha":"` + sha + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".orquestalite", "run.log"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, ".orquestalite", "results", "by-task", "T001")
	if err := os.MkdirAll(archive, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archive, "coder.c1.a1.json"), []byte(`{"summary":"added x package"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &Server{Dir: dir}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/diff/T001", nil))
	if !strings.Contains(rec.Body.String(), "added x package") {
		t.Fatalf("diff response missing coder summary: %q", rec.Body.String())
	}
}

func TestAPITasksByFeature_ServesFeatureList(t *testing.T) {
	dir := stateDir(t)
	body := `{"tasks":[{"id":"T001","title":"done thing","status":"done"}]}`
	if err := os.WriteFile(filepath.Join(dir, ".orquestalite", "tasks-F003.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &Server{Dir: dir}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/tasks/F003", nil))
	if !strings.Contains(rec.Body.String(), "done thing") {
		t.Fatalf("body=%q", rec.Body.String())
	}
	// Bare /api/tasks still serves the canonical list, not the feature handler.
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, httptest.NewRequest("GET", "/api/tasks", nil))
	if strings.TrimSpace(rec2.Body.String()) != `{"tasks":[]}` {
		t.Fatalf("bare /api/tasks body=%q", rec2.Body.String())
	}
}

func TestAPITasksByFeature_EmptyWhenAbsent(t *testing.T) {
	srv := &Server{Dir: stateDir(t)}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/tasks/F999", nil))
	if strings.TrimSpace(rec.Body.String()) != `{"tasks":[]}` {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestAPIDiff_UnavailableWithoutCommit(t *testing.T) {
	dir := stateDir(t)
	if err := os.WriteFile(filepath.Join(dir, ".orquestalite", "run.log"),
		[]byte(`{"event":"task_start","task_id":"T002"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &Server{Dir: dir}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/diff/T002", nil))
	if !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestAPIDiff_RejectsMalformedTaskID(t *testing.T) {
	srv := &Server{Dir: stateDir(t)}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/diff/bad.id", nil))
	if !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestEventsSSE_ReplaysAndStreams(t *testing.T) {
	dir := stateDir(t)
	logPath := filepath.Join(dir, ".orquestalite", "run.log")
	if err := os.WriteFile(logPath, []byte(`{"event":"cycle_start","cycle":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer((&Server{Dir: dir}).Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/events", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}

	reader := bufio.NewReader(res.Body)
	line1, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line1, "cycle_start") {
		t.Fatalf("replay line = %q", line1)
	}

	// Append a new event; it must arrive over the open stream.
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"event":"task_done","task_id":"T001"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("stream closed early: %v", err)
		}
		if strings.Contains(line, "task_done") {
			return // success
		}
	}
	t.Fatal("appended event never arrived on the SSE stream")
}

func TestLogTail_RotationResets(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "run.log")
	if err := os.WriteFile(p, []byte("{\"a\":1}\n{\"a\":2}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tail := newLogTail(p, 1)
	first := tail.Next()
	if len(first) != 1 || !strings.Contains(first[0], `"a":2`) {
		t.Fatalf("replay = %v", first)
	}
	// Rotate: replace with a smaller file.
	if err := os.WriteFile(p, []byte("{\"b\":1}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := tail.Next()
	if len(got) != 1 || !strings.Contains(got[0], `"b":1`) {
		t.Fatalf("after rotation = %v", got)
	}
}

func TestLogTail_PartialLineBuffered(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "run.log")
	if err := os.WriteFile(p, []byte(`{"event":"x"`), 0o644); err != nil { // no newline yet
		t.Fatal(err)
	}
	tail := newLogTail(p, 10)
	if got := tail.Next(); len(got) != 0 {
		t.Fatalf("partial line must not be emitted: %v", got)
	}
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(",\"done\":true}\n")
	_ = f.Close()
	got := tail.Next()
	if len(got) != 1 || !strings.Contains(got[0], `"done":true`) {
		t.Fatalf("completed line = %v", got)
	}
}
