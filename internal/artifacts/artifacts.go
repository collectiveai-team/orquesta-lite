// Package artifacts persists the full prompt and raw output of every agent
// invocation, so the exact prompt sent and the exact stdout/stderr returned are
// recoverable after the run — not just the truncated tails in run.log. One
// directory per (task, role, cycle, attempt); a re-invocation that collides
// with an existing one gets a .rN suffix so earlier work is never overwritten.
//
// Layout (under .orquestalite/runs/<run_id>/):
//
//	agents/<taskKey>/<role>.c<cycle>.a<attempt>/
//	    prompt.md      the final interpolated prompt (memory + vars + skills)
//	    stdout.log     the agent's full stdout
//	    stderr.log     the agent's full stderr
//	    meta.json      agent, model, provider, duration_s, exit_code, session_id,
//	                   cmd_line
//
// These are runtime artifacts (gitignored via .orquestalite/), distinct from
// the result contracts under .orquestalite/results/ that drive control flow.
package artifacts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Store writes invocation artifacts under a per-run root
// (.orquestalite/runs/<run_id>). A nil Store is functionally a no-op everywhere
// it is checked, so callers that do not persist artifacts simply leave it nil.
type Store struct {
	Root string
}

// Invocation is the bundle of data captured for one agent call.
type Invocation struct {
	Prompt    string
	Stdout    string
	Stderr    string
	Agent     string
	Model     string
	Provider  string
	DurationS int
	ExitCode  int
	SessionID string
	CmdLine   string
}

// safeKey normalises a task ID (or synthetic "-") into a filesystem-safe path
// component. Empty becomes "_" so per-cycle roles never collide.
func safeKey(taskKey string) string {
	if taskKey == "" {
		return "-"
	}
	out := strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(taskKey)
	return out
}

// Dir returns the directory for one invocation, creating it. On a collision
// (the dir already exists — e.g. a rerun of the same role at the same
// cycle/attempt), it appends .r2, .r3, … so earlier artifacts survive.
//
// The return path is absolute (Root is assumed absolute, which it is when wired
// from the project dir).
func (s *Store) Dir(taskKey, role string, cycle, attempt int) (string, error) {
	if s == nil || s.Root == "" {
		return "", nil
	}
	base := filepath.Join(s.Root, "agents", safeKey(taskKey), fmt.Sprintf("%s.c%d.a%d", role, cycle, attempt))
	dir := base
	for n := 2; ; n++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		} else if err != nil {
			return "", err
		}
		dir = fmt.Sprintf("%s.r%d", base, n)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// SaveInvocation writes prompt.md, stdout.log, stderr.log, and meta.json into
// dir. It never panics on a nil Store or empty dir (no-op).
func (s *Store) SaveInvocation(dir string, inv Invocation) error {
	if s == nil || dir == "" {
		return nil
	}
	if err := writeBytes(filepath.Join(dir, "prompt.md"), []byte(inv.Prompt)); err != nil {
		return fmt.Errorf("write prompt.md: %w", err)
	}
	if err := writeBytes(filepath.Join(dir, "stdout.log"), []byte(inv.Stdout)); err != nil {
		return fmt.Errorf("write stdout.log: %w", err)
	}
	if err := writeBytes(filepath.Join(dir, "stderr.log"), []byte(inv.Stderr)); err != nil {
		return fmt.Errorf("write stderr.log: %w", err)
	}
	meta := map[string]any{
		"agent":      inv.Agent,
		"model":      inv.Model,
		"provider":   inv.Provider,
		"duration_s": inv.DurationS,
		"exit_code":  inv.ExitCode,
		"session_id": inv.SessionID,
		"cmd_line":   inv.CmdLine,
	}
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := writeBytes(filepath.Join(dir, "meta.json"), raw); err != nil {
		return fmt.Errorf("write meta.json: %w", err)
	}
	return nil
}

func writeBytes(path string, b []byte) error {
	return os.WriteFile(path, b, 0o644)
}

// Prune keeps the newest keepRuns run directories under <Root's parent> (i.e.
// .orquestalite/runs/), deleting the oldest when more than keepRuns exist. A
// non-positive keepRuns disables pruning. It is a no-op when the runs dir does
// not exist, so a fresh repository never errors.
func (s *Store) Prune(keepRuns int) error {
	if s == nil || keepRuns <= 0 || s.Root == "" {
		return nil
	}
	runsDir := filepath.Dir(s.Root)
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var runDirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "r") {
			continue
		}
		runDirs = append(runDirs, filepath.Join(runsDir, name))
	}
	if len(runDirs) <= keepRuns {
		return nil
	}
	// Oldest first: run IDs are sortable by name (compact UTC prefix).
	sort.Strings(runDirs)
	for _, d := range runDirs[:len(runDirs)-keepRuns] {
		_ = os.RemoveAll(d)
	}
	return nil
}

// RelativeDir returns dir relative to the project root, for the artifacts_dir
// field on agent_run events and the dashboard. Returns "" for an empty dir.
func RelativeDir(dir, projectDir string) string {
	if dir == "" {
		return ""
	}
	rel, err := filepath.Rel(projectDir, dir)
	if err != nil {
		return dir
	}
	return filepath.ToSlash(rel)
}