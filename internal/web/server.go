// Package web serves the orquestalite dashboard: a read-only view over the
// runtime state (.orquestalite/tasks.json, factory.json, run.log) with a
// live SSE stream of run events. The UI is embedded in the binary so the
// dashboard works anywhere the CLI does, including inside a container.
package web

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/cost"
	"github.com/lionelchamorro/orquestalite/internal/gitx"
)

// taskIDRe and shaRe gate the values handed to git: a task id reaches the
// handler from the URL, and the commit sha is read back from run.log. Both are
// validated so neither can smuggle a leading "-" (and thus a git flag) through.
var (
	taskIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	shaRe    = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)
)

//go:embed static
var staticFS embed.FS

// Server exposes the project's orchestration state over HTTP.
type Server struct {
	Dir string // project directory containing .orquestalite/

	costMu      sync.Mutex
	costCached  []byte
	costFetched time.Time
}

func (s *Server) statePath(name string) string {
	return filepath.Join(s.Dir, ".orquestalite", name)
}

// resultRoles is the fixed set of roles whose result file may be served via
// /api/result/{role}. Whitelisting keeps the {role} path segment from being
// used for traversal into arbitrary files under .orquestalite/results.
var resultRoles = map[string]bool{
	"planner": true, "parser": true, "coder": true, "tester": true,
	"critic": true, "reviewer": true, "verifier": true, "orchestrator": true,
}

// Handler builds the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tasks", s.handleTasks)
	mux.HandleFunc("GET /api/factory", s.handleFactory)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/cost", s.handleCost)
	mux.HandleFunc("GET /api/result/{role}", s.handleResult)
	mux.HandleFunc("GET /api/diff/{task}", s.handleDiff)

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // embedded FS layout is fixed at compile time
	}
	// The office gameboard is a distinct screen mode served at a clean path;
	// its assets (gameboard.js, vendor/*) come from the static file server.
	mux.HandleFunc("GET /gameboard", s.handleGameboard)
	mux.Handle("GET /", http.FileServerFS(static))
	return mux
}

// handleGameboard serves the office gameboard shell at a clean URL.
func (s *Server) handleGameboard(w http.ResponseWriter, r *http.Request) {
	raw, err := staticFS.ReadFile("static/gameboard.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(raw)
}

// handleResult serves one role's latest result document
// (.orquestalite/results/<role>.json), used by the gameboard's JSON/summary
// views. Unknown roles yield "null" rather than an error so the UI can render
// an empty state uniformly.
func (s *Server) handleResult(w http.ResponseWriter, r *http.Request) {
	role := r.PathValue("role")
	if !resultRoles[role] {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte("null"))
		return
	}
	s.serveJSONFile(w, filepath.Join(s.statePath("results"), role+".json"), "null")
}

// handleDiff returns the code changes that landed for one task: the git diff of
// the commit recorded for that task in run.log, plus the agent that produced it.
// {"available": false} when the task has no commit yet or the repo is absent.
func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")

	write := func(v any) {
		raw, err := json.Marshal(v)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(raw)
	}

	task := r.PathValue("task")
	if !taskIDRe.MatchString(task) {
		write(map[string]any{"available": false})
		return
	}
	sha, agent := s.findTaskCommit(task)
	if !shaRe.MatchString(sha) {
		write(map[string]any{"available": false})
		return
	}
	diff, err := gitx.ShowCommit(s.Dir, sha)
	if err != nil {
		write(map[string]any{"available": false, "reason": err.Error()})
		return
	}
	short := sha
	if len(short) > 8 {
		short = short[:8]
	}
	write(map[string]any{
		"available": true,
		"task":      task,
		"commit":    sha,
		"short":     short,
		"agent":     agent,
		"diff":      diff,
	})
}

// findTaskCommit scans run.log for a task's landed commit and the coder agent
// that produced it. The latest task_done wins (a task may complete more than
// once across re-runs); agent falls back to any agent_run for the task.
func (s *Server) findTaskCommit(task string) (sha, agent string) {
	f, err := os.Open(s.statePath("run.log"))
	if err != nil {
		return "", ""
	}
	defer f.Close()

	var anyAgent string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var e struct {
			Event     string `json:"event"`
			TaskID    string `json:"task_id"`
			CommitSHA string `json:"commit_sha"`
			Role      string `json:"role"`
			Agent     string `json:"agent"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.TaskID != task {
			continue
		}
		switch e.Event {
		case "task_done":
			sha = e.CommitSHA
		case "agent_run":
			anyAgent = e.Agent
			if e.Role == "coder" {
				agent = e.Agent
			}
		}
	}
	if agent == "" {
		agent = anyAgent
	}
	return sha, agent
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

// handleCost serves the per-task spend rollup (run.log joined against agtop).
// agtop invocations are expensive, so the result is cached for a minute;
// when agtop is unavailable the endpoint reports {"available": false}.
func (s *Server) handleCost(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")

	s.costMu.Lock()
	defer s.costMu.Unlock()
	if time.Since(s.costFetched) < time.Minute && s.costCached != nil {
		_, _ = w.Write(s.costCached)
		return
	}

	payload := func() any {
		runs, err := cost.RunsFromLog(s.statePath("run.log"))
		if err != nil || len(runs) == 0 {
			return map[string]any{"available": false}
		}
		sessions, err := cost.Collect(r.Context())
		if err != nil {
			return map[string]any{"available": false, "reason": err.Error()}
		}
		rep := cost.Rollup(runs, sessions)
		return map[string]any{
			"available": true,
			"total_usd": rep.TotalUSD,
			"runs":      rep.Runs,
			"priced":    rep.Priced,
		}
	}()

	raw, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.costCached = raw
	s.costFetched = time.Now()
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
