package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/config"
	"github.com/lionelchamorro/orquestalite/internal/invoke"
)

type roleSmokeResult struct {
	NotesForMemory *string `json:"notes_for_memory"`
}

func (r roleSmokeResult) MemoryNote() *string { return r.NotesForMemory }

func (d *liveDeps) callRole(ctx context.Context, roleName, prompt string, role config.Role) error {
	promptPath := filepath.Join(".orquestalite", "tmp-prompts", roleName+".md")
	if err := os.MkdirAll(filepath.Join(d.dir, filepath.Dir(promptPath)), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(d.dir, promptPath), []byte(prompt), 0o644); err != nil {
		return err
	}
	spec, err := d.roleSpecForTest(roleName, role, promptPath)
	if err != nil {
		return err
	}
	inv := d.invokerWithRole(roleName, spec)
	_, err = invoke.Role(ctx, inv, roleName, invoke.RoleCall{}, invoke.RunContext{TaskID: "-", Attempt: 1}, parseRoleSmokeResult)
	return err
}

func (d *liveDeps) roleSpecForTest(roleName string, role config.Role, promptPath string) (config.RoleSpec, error) {
	agents, err := d.agentSpecsForTest(roleName, "agents", role.Agents)
	if err != nil {
		return config.RoleSpec{}, err
	}
	escalation, err := d.agentSpecsForTest(roleName, "escalation_ladder", role.EscalationLadder)
	if err != nil {
		return config.RoleSpec{}, err
	}
	return config.RoleSpec{
		Agents:           agents,
		PromptPath:       promptPath,
		ResultPath:       role.ResultPath,
		Timeout:          time.Duration(role.TimeoutSeconds) * time.Second,
		EscalationLadder: escalation,
		DecomposePrompt:  role.DecomposePrompt,
	}, nil
}

func (d *liveDeps) agentSpecsForTest(roleName, field string, names []string) ([]config.AgentSpec, error) {
	specs := make([]config.AgentSpec, 0, len(names))
	for _, name := range names {
		ag, ok := d.cfg.Agents[name]
		if !ok {
			return nil, fmt.Errorf("role %q %s references unknown agent %q", roleName, field, name)
		}
		specs = append(specs, config.AgentSpec{
			Name:        name,
			Provider:    ag.Provider,
			Model:       ag.Model,
			Effort:      ag.Effort,
			SkipPerms:   ag.DangerouslySkipPermissions,
			RatePattern: ag.RateLimitPattern,
			Cmd:         append([]string(nil), ag.Cmd...),
		})
	}
	return specs, nil
}

func (d *liveDeps) invokerWithRole(roleName string, spec config.RoleSpec) *invoke.RoleInvoker {
	inv := &invoke.RoleInvoker{}
	if d.inv != nil {
		*inv = *d.inv
	}
	if inv.Specs == nil {
		inv.Specs = map[string]config.RoleSpec{}
	} else {
		specs := make(map[string]config.RoleSpec, len(inv.Specs)+1)
		for k, v := range inv.Specs {
			specs[k] = v
		}
		inv.Specs = specs
	}
	inv.Specs[roleName] = spec
	inv.Dir = d.dir
	inv.Fallback = d.fc
	inv.Log = d.log
	inv.Health = d.health
	inv.MemPath = d.memPath
	inv.DefaultRateLimitPattern = d.cfg.RateLimitBackoff.DefaultPattern
	inv.AgentHealthThreshold = agentHealthThreshold
	if inv.Runner == nil {
		inv.Runner = invoke.ExecRunner{}
	}
	return inv
}

func parseRoleSmokeResult(path string) (*roleSmokeResult, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r roleSmokeResult
	if len(strings.TrimSpace(string(raw))) == 0 {
		return &r, nil
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
