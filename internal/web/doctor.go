package web

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/doctor"
)

// handleDoctor exposes the CLI's preflight checks so a companion app can
// gate launches on a red preflight. Same pattern as the cost cache: results
// are cached for 30s because a UI may poll this. Checks run under a 2s
// budget — anything slower degrades to warn inside doctor.Run rather than
// blocking the request.
func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")

	s.doctorMu.Lock()
	defer s.doctorMu.Unlock()
	if time.Since(s.doctorFetched) < 30*time.Second && s.doctorCached != nil {
		_, _ = w.Write(s.doctorCached)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	checks := doctor.Run(ctx, s.Dir)
	ok := true
	for _, c := range checks {
		if c.Status == doctor.StatusError {
			ok = false
			break
		}
	}
	raw, err := json.Marshal(map[string]any{"ok": ok, "checks": checks})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.doctorCached = raw
	s.doctorFetched = time.Now()
	_, _ = w.Write(raw)
}
