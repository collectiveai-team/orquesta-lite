// Package web serves the orquestalite dashboard: a read-only view over the
// runtime state (.orquestalite/tasks.json, factory.json, run.log) with a
// live SSE stream of run events. The UI is embedded in the binary so the
// dashboard works anywhere the CLI does, including inside a container.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

//go:embed static
var staticFS embed.FS

// Server exposes the project's orchestration state over HTTP.
type Server struct {
	Dir string // project directory containing .orquestalite/
}

func (s *Server) statePath(name string) string {
	return filepath.Join(s.Dir, ".orquestalite", name)
}

// Handler builds the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tasks", s.handleTasks)
	mux.HandleFunc("GET /api/factory", s.handleFactory)
	mux.HandleFunc("GET /api/events", s.handleEvents)

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // embedded FS layout is fixed at compile time
	}
	mux.Handle("GET /", http.FileServerFS(static))
	return mux
}

// handleTasks serves tasks.json verbatim (empty task list when absent).
func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	s.serveJSONFile(w, s.statePath("tasks.json"), `{"tasks":[]}`)
}

// handleFactory serves factory.json verbatim (null when absent).
func (s *Server) handleFactory(w http.ResponseWriter, r *http.Request) {
	s.serveJSONFile(w, s.statePath("factory.json"), "null")
}

func (s *Server) serveJSONFile(w http.ResponseWriter, path, fallback string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			_, _ = w.Write([]byte(fallback))
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !json.Valid(raw) {
		// A concurrent writer may be mid-write; serve the fallback rather
		// than corrupt JSON.
		_, _ = w.Write([]byte(fallback))
		return
	}
	_, _ = w.Write(raw)
}

// handleEvents streams run.log lines as Server-Sent Events. The last
// replayLines lines are sent immediately so a fresh page shows recent
// history, then new lines are pushed as the orchestrator appends them.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")

	tail := newLogTail(s.statePath("run.log"), 100)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	send := func(lines []string) bool {
		for _, line := range lines {
			if _, err := w.Write([]byte("data: " + line + "\n\n")); err != nil {
				return false
			}
		}
		if len(lines) > 0 {
			flusher.Flush()
		}
		return true
	}

	if !send(tail.Next()) {
		return
	}
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if !send(tail.Next()) {
				return
			}
		}
	}
}

// Serve runs the dashboard until ctx is cancelled.
func Serve(ctx context.Context, addr, dir string) error {
	s := &Server{Dir: dir}
	srv := &http.Server{Addr: addr, Handler: s.Handler()}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		return err
	}
}
