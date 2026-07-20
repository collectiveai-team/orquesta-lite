package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/workflow"
)

func TestWorkflowAPI(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, ".orquestalite")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := workflow.Open(filepath.Join(state, "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateRun(context.Background(), workflow.CreateRunParams{ID: "r1", FlowRef: "flow:demo@1", DefinitionHash: "hash", IR: json.RawMessage(`{}`), Inputs: json.RawMessage(`{}`), Policy: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	server := &Server{Dir: dir}
	for _, path := range []string{"/api/workflows", "/api/workflows/r1", "/api/workflows/r1/steps", "/api/workflows/r1/approvals"} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s code=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
		if recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s missing no-store", path)
		}
	}
}
