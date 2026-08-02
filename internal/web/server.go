// Package web serves a read-only dashboard over the durable v2 runtime.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"path/filepath"
	"sync"
	"time"
)

//go:embed static
var staticFS embed.FS

type Server struct {
	Dir string

	doctorMu      sync.Mutex
	doctorCached  []byte
	doctorFetched time.Time
}

func (s *Server) statePath(name string) string {
	return filepath.Join(s.Dir, ".orquestalite", name)
}

func writeJSON(out http.ResponseWriter, status int, value any) {
	out.Header().Set("Content-Type", "application/json")
	out.Header().Set("Cache-Control", "no-store")
	raw, err := json.Marshal(value)
	if err != nil {
		http.Error(out, err.Error(), http.StatusInternalServerError)
		return
	}
	out.WriteHeader(status)
	_, _ = out.Write(raw)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/flows", s.handleFlows)
	mux.HandleFunc("GET /api/doctor", s.handleDoctor)
	mux.HandleFunc("GET /api/workflows", s.handleWorkflows)
	mux.HandleFunc("GET /api/workflows/{id}", s.handleWorkflow)
	mux.HandleFunc("GET /api/workflows/{id}/steps", s.handleWorkflowSteps)
	mux.HandleFunc("GET /api/workflows/{id}/approvals", s.handleWorkflowApprovals)
	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /", http.FileServerFS(static))
	return mux
}

func (s *Server) handleEvents(out http.ResponseWriter, request *http.Request) {
	flusher, ok := out.(http.Flusher)
	if !ok {
		http.Error(out, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	out.Header().Set("Content-Type", "text/event-stream")
	out.Header().Set("Cache-Control", "no-store")
	out.Header().Set("Connection", "keep-alive")
	tail := newLogTail(s.statePath("run.log"), 100)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	send := func(lines []string) bool {
		for _, line := range lines {
			if _, err := out.Write([]byte("data: " + line + "\n\n")); err != nil {
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
		case <-request.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := out.Write([]byte(": ping\n\n")); err != nil {
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

func Serve(ctx context.Context, addr, dir string) error {
	server := &http.Server{Addr: addr, Handler: (&Server{Dir: dir}).Handler()}
	errors := make(chan error, 1)
	go func() { errors <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		return nil
	case err := <-errors:
		return err
	}
}
