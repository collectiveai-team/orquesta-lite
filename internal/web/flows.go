package web

import (
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/lionelchamorro/orquestalite/internal/config"
	"github.com/lionelchamorro/orquestalite/internal/engine"
)

type flowInput struct {
	Type     string `json:"type"`
	Default  any    `json:"default"`
	Required bool   `json:"required"`
}

type flowEntry struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Inputs      map[string]flowInput `json:"inputs"`
	Roles       []string             `json:"roles"`
	Preflight   map[string]string    `json:"preflight"`
}

// handleFlows serves the workspace's flows.json parsed with the same loader
// `orq-lite flow run` uses, so a companion app can build a launch form
// without filesystem access. Anything the parser rejects degrades to an
// empty catalog with a log line — never an error response.
func (s *Server) handleFlows(w http.ResponseWriter, r *http.Request) {
	entries := []flowEntry{}
	flows, err := engine.LoadFlows(filepath.Join(s.Dir, "flows.json"))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("web: flows.json unreadable, serving empty catalog: %v", err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"flows": entries})
		return
	}
	cfg, cfgErr := config.Load(filepath.Join(s.Dir, "team.json"))

	names := make([]string, 0, len(flows.Flows))
	for name := range flows.Flows {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		flow := flows.Flows[name]
		inputs := map[string]flowInput{}
		for in, spec := range flow.Inputs {
			inputs[in] = flowInput{Type: spec.Type, Default: spec.Default, Required: !spec.HasDefault}
		}
		roles := flow.ReferencedRoles()
		preflight := map[string]string{}
		for _, role := range roles {
			preflight[role] = rolePreflight(s.Dir, cfg, cfgErr, role)
		}
		entries = append(entries, flowEntry{
			Name:        name,
			Description: flow.Description,
			Inputs:      inputs,
			Roles:       roles,
			Preflight:   preflight,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"flows": entries})
}

// rolePreflight classifies launch readiness for one referenced role. An
// unloadable team.json means no role can be resolved, so every role reports
// missing_role.
func rolePreflight(dir string, cfg *config.Config, cfgErr error, role string) string {
	if cfgErr != nil {
		return "missing_role"
	}
	r, ok := cfg.Roles[role]
	if !ok {
		return "missing_role"
	}
	if _, err := os.Stat(filepath.Join(dir, r.Prompt)); err != nil {
		return "missing_prompt"
	}
	return "ok"
}
