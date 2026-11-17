package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/workflow"
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
	if _, _, err = store.CreateRunOnce(context.Background(), workflow.CreateRunParams{ID: "r1", FlowRef: "flow:demo@1", DefinitionHash: "hash", IR: json.RawMessage(`{}`), Inputs: json.RawMessage(`{}`), Policy: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	server := &Server{Dir: dir}
	for _, path := range []string{"/api/workflows", "/api/workflows/r1", "/api/workflows/r1/steps", "/api/workflows/r1/approvals", "/api/workflows/r1/external"} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s code=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
		if recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s missing no-store", path)
		}
	}
	missing := httptest.NewRecorder()
	server.Handler().ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/workflows/missing/external", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing external workflow code=%d body=%s", missing.Code, missing.Body.String())
	}
	mutation := httptest.NewRecorder()
	server.Handler().ServeHTTP(mutation, httptest.NewRequest(http.MethodPost, "/api/workflows/r1/external", nil))
	if mutation.Code != http.StatusMethodNotAllowed {
		t.Fatalf("external dashboard must remain read-only, code=%d", mutation.Code)
	}
}
