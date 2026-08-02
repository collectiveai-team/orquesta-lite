package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func getJSON(t *testing.T, server *Server, path string, target any) int {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
		t.Fatalf("%s returned invalid JSON: %v\n%s", path, err, recorder.Body.String())
	}
	return recorder.Code
}

func stateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".orquestalite"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestIndexServed(t *testing.T) {
	recorder := httptest.NewRecorder()
	(&Server{Dir: stateDir(t)}).Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "orquestalite") {
		t.Fatalf("code=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestLegacyDashboardEndpointsAreGone(t *testing.T) {
	server := (&Server{Dir: stateDir(t)}).Handler()
	for _, path := range []string{"/api/tasks", "/api/factory", "/api/runs", "/api/result/coder"} {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s code=%d, want 404", path, recorder.Code)
		}
	}
}

func TestEventsSSEReplaysAndStreams(t *testing.T) {
	dir := stateDir(t)
	logPath := filepath.Join(dir, ".orquestalite", "run.log")
	if err := os.WriteFile(logPath, []byte(`{"event":"workflow_started"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testServer := httptest.NewServer((&Server{Dir: dir}).Handler())
	defer testServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, testServer.URL+"/api/events", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, "workflow_started") {
		t.Fatalf("replay line=%q err=%v", line, err)
	}
	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString(`{"event":"workflow_succeeded"}` + "\n")
	_ = file.Close()
	for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); {
		line, err = reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(line, "workflow_succeeded") {
			return
		}
	}
	t.Fatal("appended event never arrived")
}

func TestLogTailRotationAndPartialLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	if err := os.WriteFile(path, []byte("{\"a\":1}\n{\"a\":2}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tail := newLogTail(path, 1)
	if got := tail.Next(); len(got) != 1 || !strings.Contains(got[0], `"a":2`) {
		t.Fatalf("replay=%v", got)
	}
	if err := os.WriteFile(path, []byte(`{"event":"x"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := tail.Next(); len(got) != 0 {
		t.Fatalf("partial line emitted: %v", got)
	}
	file, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = file.WriteString(",\"done\":true}\n")
	_ = file.Close()
	if got := tail.Next(); len(got) != 1 || !strings.Contains(got[0], `"done":true`) {
		t.Fatalf("completed line=%v", got)
	}
}
