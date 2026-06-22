// Package sessions persists the provider session id of each agent run, keyed by
// task → role → agent, so a later run of the same agent on the same task can
// resume that conversation instead of starting from scratch.
//
// The store lives at .orquestalite/sessions.json. It is keyed by agent (not
// just provider) on purpose: resuming only makes sense when the very same agent
// runs again — if the fix loop falls back to a different agent or provider,
// there is no entry for it and the run starts fresh.
package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Store is a concurrency-safe, disk-backed map of task → role → agent → session.
type Store struct {
	mu   sync.Mutex
	path string
	data map[string]map[string]map[string]string
}

// Load reads the session store for a project dir, returning an empty store when
// the file is absent or unreadable (a corrupt/old file is never fatal — the
// worst case is that runs start fresh).
func Load(dir string) *Store {
	s := &Store{
		path: filepath.Join(dir, ".orquestalite", "sessions.json"),
		data: map[string]map[string]map[string]string{},
	}
	if raw, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(raw, &s.data)
		if s.data == nil {
			s.data = map[string]map[string]map[string]string{}
		}
	}
	return s
}

// Get returns the stored session id for (task, role, agent), or "" if none.
func (s *Store) Get(task, role, agent string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[task][role][agent]
}

// Set records (task, role, agent) → id and persists the store. A no-op write
// (same id already stored) skips the disk flush.
func (s *Store) Set(task, role, agent, id string) error {
	if id == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[task] == nil {
		s.data[task] = map[string]map[string]string{}
	}
	if s.data[task][role] == nil {
		s.data[task][role] = map[string]string{}
	}
	if s.data[task][role][agent] == id {
		return nil
	}
	s.data[task][role][agent] = id
	return s.save()
}

// Delete removes a stored session (e.g. after a resume attempt failed, so the
// same agent doesn't keep retrying a stale/expired session). A no-op when none.
func (s *Store) Delete(task, role, agent string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[task] == nil || s.data[task][role] == nil {
		return nil
	}
	if _, ok := s.data[task][role][agent]; !ok {
		return nil
	}
	delete(s.data[task][role], agent)
	return s.save()
}

// save writes the store atomically (temp file + rename) so a crash mid-write
// can never leave a half-written sessions.json.
func (s *Store) save() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
