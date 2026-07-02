package web

import (
	"net/http"
	"strconv"

	"github.com/lionelchamorro/orquestalite/internal/eventdb"
)

// pageParams parses limit/offset with the contract defaults: limit=50
// (max 500), offset=0. Unparseable or out-of-range values fall back to the
// defaults rather than erroring.
func pageParams(r *http.Request) (limit, offset int) {
	limit, offset = 50, 0
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
		if limit > 500 {
			limit = 500
		}
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v > 0 {
		offset = v
	}
	return limit, offset
}

func (s *Server) queryDB(w http.ResponseWriter) (*eventdb.DB, bool) {
	db, err := s.eventDB()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return nil, false
	}
	return db, true
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	db, ok := s.queryDB(w)
	if !ok {
		return
	}
	limit, offset := pageParams(r)
	var active *bool
	switch r.URL.Query().Get("active") {
	case "true":
		v := true
		active = &v
	case "false":
		v := false
		active = &v
	}
	runs, total, err := db.Runs(eventdb.RunsFilter{Active: active}, limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs, "total": total})
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	db, ok := s.queryDB(w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	run, err := db.Run(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if run == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown run id: " + id})
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	db, ok := s.queryDB(w)
	if !ok {
		return
	}
	limit, offset := pageParams(r)
	q := r.URL.Query()
	events, total, err := db.Events(r.PathValue("id"), q.Get("type"), q.Get("task_id"), limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "total": total})
}

func (s *Server) handleAgentRuns(w http.ResponseWriter, r *http.Request) {
	db, ok := s.queryDB(w)
	if !ok {
		return
	}
	limit, offset := pageParams(r)
	q := r.URL.Query()
	recs, total, err := db.AgentRuns(eventdb.AgentRunsFilter{
		RunID:  q.Get("run_id"),
		TaskID: q.Get("task_id"),
		Role:   q.Get("role"),
		Agent:  q.Get("agent"),
	}, limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_runs": recs, "total": total})
}

func (s *Server) handleCostStats(w http.ResponseWriter, r *http.Request) {
	db, ok := s.queryDB(w)
	if !ok {
		return
	}
	by := r.URL.Query().Get("by")
	switch by {
	case "run", "agent", "task", "role":
	default:
		by = "run"
	}
	rows, err := db.CostStats(by)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"by": by, "rows": rows})
}
