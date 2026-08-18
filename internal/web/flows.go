package web

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/activity/builtin"
	"github.com/collectiveai-team/orquesta-lite/internal/config"
	"github.com/collectiveai-team/orquesta-lite/internal/flow"
)

type flowInput struct {
	Schema   string `json:"schema"`
	Default  any    `json:"default"`
	Required bool   `json:"required"`
}

type flowEntry struct {
	Name       string               `json:"name"`
	Pack       string               `json:"pack,omitempty"`
	PackDigest string               `json:"pack_digest,omitempty"`
	Inputs     map[string]flowInput `json:"inputs"`
	Roles      []string             `json:"roles"`
	Preflight  map[string]string    `json:"preflight"`
}

func webBuiltinSpecs() []activity.Spec {
	return []activity.Spec{
		(&builtin.AgentExecutor{}).Spec(), (&builtin.CommandExecutor{}).Spec(),
		(&builtin.GateExecutor{}).Spec(), (builtin.GateAssertExecutor{}).Spec(),
		(&builtin.ArtifactExecutor{}).Spec(), (builtin.ApprovalExecutor{}).Spec(),
	}
}

// handleFlows serves only strict v2 flows discovered in local catalogs and
// verified installed packs. Invalid documents are omitted from the launch
// catalog; validation details remain available through `orq-lite flow validate`.
func (s *Server) handleFlows(w http.ResponseWriter, r *http.Request) {
	entries := []flowEntry{}
	cfg, cfgErr := config.LoadDynamic(filepath.Join(s.Dir, "team.json"))
	seen := map[string]bool{}
	roots := []string{s.Dir, filepath.Join(s.Dir, ".orquestalite", "packs")}
	for _, searchRoot := range roots {
		_ = filepath.WalkDir(searchRoot, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || filepath.Ext(path) != ".json" || filepath.Base(filepath.Dir(path)) != "flows" {
				return nil
			}
			document, loadErr := flow.Load(path)
			if loadErr != nil || document.Kind != flow.KindFlow {
				return nil
			}
			root := flow.PackRootForPath(path)
			catalog := flow.NewDirectoryCatalog(root, webBuiltinSpecs())
			ir, diagnostics := flow.Compile(document, catalog)
			if diagnostics.HasErrors() {
				return nil
			}
			name := "flow:" + document.Metadata.Name + "@" + document.Metadata.Version
			packLabel, packDigest := "", ""
			if pack, packErr := flow.LoadPack(root); packErr == nil {
				if pinErr := flow.PinPack(ir, pack); pinErr != nil {
					return nil
				}
				name = pack.Name + "/" + document.Metadata.Name + "@" + document.Metadata.Version
				packLabel = pack.Name + "@" + pack.Version
				packDigest = string(pack.Snapshot().Digest)
			}
			if seen[name+"|"+packLabel] {
				return nil
			}
			seen[name+"|"+packLabel] = true
			inputs := make(map[string]flowInput, len(document.Inputs))
			for inputName, spec := range document.Inputs {
				var defaultValue any
				if spec.Default != nil {
					defaultValue = spec.Default.Literal
				}
				inputs[inputName] = flowInput{Schema: spec.Schema, Default: defaultValue, Required: spec.Default == nil}
			}
			roles := referencedAgentRoles(ir)
			preflight := map[string]string{}
			for _, role := range roles {
				preflight[role] = rolePreflight(s.Dir, cfg, cfgErr, role)
			}
			entries = append(entries, flowEntry{Name: name, Pack: packLabel, PackDigest: packDigest, Inputs: inputs, Roles: roles, Preflight: preflight})
			return nil
		})
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name < entries[right].Name })
	writeJSON(w, http.StatusOK, map[string]any{"flows": entries})
}

func referencedAgentRoles(ir *flow.IR) []string {
	seen := map[string]bool{}
	var walk func(*flow.IR)
	walk = func(current *flow.IR) {
		if current == nil {
			return
		}
		for _, step := range current.Steps {
			if step.Activity != nil && step.Activity.Name == "agent.invoke" {
				if role, ok := step.With["role"].Literal.(string); ok && role != "" {
					seen[role] = true
				}
			}
			if step.Subflow != nil {
				walk(step.Subflow)
			}
		}
	}
	walk(ir)
	roles := make([]string, 0, len(seen))
	for role := range seen {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles
}

func rolePreflight(dir string, cfg *config.Config, cfgErr error, role string) string {
	if cfgErr != nil {
		return "missing_role"
	}
	roleSpec, ok := cfg.Roles[role]
	if !ok {
		return "missing_role"
	}
	if _, err := os.Stat(filepath.Join(dir, roleSpec.Prompt)); err != nil {
		return "missing_prompt"
	}
	return "ok"
}
