package web

import (
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/lionelchamorro/orquestalite/internal/workflow"
)

func (s *Server) openWorkflowStore() (*workflow.Store, error) {
	path := filepath.Join(s.Dir, ".orquestalite", "workflows.db")
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	return workflow.Open(path)
}

func (s *Server) handleWorkflows(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if parsed, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && parsed > 0 && parsed <= 500 {
		limit = parsed
	}
	store, err := s.openWorkflowStore()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"workflows": []any{}})
		return
	}
	defer store.Close()
	runs, err := store.ListRuns(r.Context(), limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflows": runs})
}

func (s *Server) handleWorkflow(w http.ResponseWriter, r *http.Request) {
	store, err := s.openWorkflowStore()
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workflow store not found"})
		return
	}
	defer store.Close()
	run, err := store.GetRun(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workflow not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleWorkflowSteps(w http.ResponseWriter, r *http.Request) {
	store, err := s.openWorkflowStore()
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workflow store not found"})
		return
	}
	defer store.Close()
	steps, err := store.ListSteps(r.Context(), r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"steps": steps})
}

func (s *Server) handleWorkflowApprovals(w http.ResponseWriter, r *http.Request) {
	store, err := s.openWorkflowStore()
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workflow store not found"})
		return
	}
	defer store.Close()
	approvals, err := store.ListApprovals(r.Context(), r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": approvals})
}
