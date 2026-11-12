package flow

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/collectiveai-team/orquesta-lite/internal/checks"
	"github.com/collectiveai-team/orquesta-lite/internal/config"
	"github.com/collectiveai-team/orquesta-lite/internal/promptbudget"
	"github.com/collectiveai-team/orquesta-lite/internal/risk"
	"github.com/collectiveai-team/orquesta-lite/internal/supervision"
)

// VerificationSpec is opt-in and versioned independently from flow syntax.
// The repository owns its checks; the compiler embeds their resolved content.
type VerificationSpec struct {
	Version       int                            `json:"version"`
	Checks        string                         `json:"checks"`
	Risk          risk.Policy                    `json:"risk"`
	Correction    risk.CorrectionLimits          `json:"correction"`
	PromptBudgets map[string]promptbudget.Policy `json:"prompt_budgets"`
	Supervision   supervision.Policy             `json:"supervision"`
	Roles         map[string]RoleModelPolicy     `json:"roles"`
	MemoryProject string                         `json:"memory_project,omitempty"`
}

// Models are keyed by configured agent name, including every fallback. An
// economy role requires an explicit reason before selecting a higher class.
type RoleModelPolicy struct {
	Purpose                   string            `json:"purpose"`
	Class                     string            `json:"class"`
	Models                    map[string]string `json:"models"`
	OverrideReason            string            `json:"override_reason,omitempty"`
	RequireNativeModelControl bool              `json:"require_native_model_control,omitempty"`
}

type Verification struct {
	VerificationSpec
	ResolvedChecks checks.Resolved            `json:"resolved_checks"`
	ResolvedRoles  map[string]config.RoleSpec `json:"resolved_roles,omitempty"`
	PromptHashes   map[string]string          `json:"prompt_hashes,omitempty"`
}

func (v VerificationSpec) Validate() error {
	if v.Version != 1 {
		return fmt.Errorf("verification.version must be 1")
	}
	clean := filepath.ToSlash(filepath.Clean(v.Checks))
	if v.Checks == "" || clean != v.Checks || filepath.IsAbs(v.Checks) || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("verification.checks must be a repository-relative JSON profile")
	}
	if err := v.Risk.Validate(); err != nil {
		return err
	}
	if err := v.Correction.Validate(); err != nil {
		return err
	}
	if err := v.Supervision.Validate(); err != nil {
		return err
	}
	for name, policy := range v.Roles {
		if !identifier.MatchString(name) {
			return fmt.Errorf("verification: invalid role %q", name)
		}
		if policy.Purpose != "implementation" && policy.Purpose != "review" && policy.Purpose != "exploration" && policy.Purpose != "transcription" {
			return fmt.Errorf("role %s: invalid purpose", name)
		}
		if policy.Class != "economy" && policy.Class != "standard" && policy.Class != "premium" {
			return fmt.Errorf("role %s: explicit model class required", name)
		}
		if (policy.Purpose == "exploration" || policy.Purpose == "transcription") && policy.Class != "economy" && strings.TrimSpace(policy.OverrideReason) == "" {
			return fmt.Errorf("role %s: exploration/transcription require economy models or an explicit override_reason", name)
		}
		if len(policy.Models) == 0 {
			return fmt.Errorf("role %s: explicit models required", name)
		}
		for agent, model := range policy.Models {
			if agent == "" || strings.TrimSpace(model) == "" {
				return fmt.Errorf("role %s: empty agent/model binding", name)
			}
		}
		budget, ok := v.PromptBudgets[name]
		if !ok {
			return fmt.Errorf("role %s: prompt budget required", name)
		}
		if err := budget.Validate(); err != nil {
			return fmt.Errorf("role %s: %w", name, err)
		}
	}
	for name, budget := range v.PromptBudgets {
		if _, ok := v.Roles[name]; !ok {
			return fmt.Errorf("prompt budget references undeclared role %s", name)
		}
		if err := budget.Validate(); err != nil {
			return err
		}
	}
	if strings.ContainsAny(v.MemoryProject, "\x00\r\n") {
		return fmt.Errorf("memory_project must be a single line")
	}
	return nil
}

type verificationResolver interface {
	ResolveChecks(string) (*checks.Resolved, error)
}

func compileVerification(spec *VerificationSpec, catalog Catalog) (*Verification, error) {
	if spec == nil {
		return nil, nil
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	resolver, ok := catalog.(verificationResolver)
	if !ok {
		return nil, fmt.Errorf("catalog cannot resolve repository checks")
	}
	resolved, err := resolver.ResolveChecks(spec.Checks)
	if err != nil {
		return nil, err
	}
	return &Verification{VerificationSpec: *spec, ResolvedChecks: *resolved}, nil
}

// Rehash includes host-resolved verification resources in the definition hash.
func (ir *IR) Rehash() error {
	raw, err := json.Marshal(ir)
	if err == nil {
		ir.Digest = digestBytes(raw)
	}
	return err
}

func validateVerifiedStructure(ir *IR) error {
	found := map[string]bool{}
	var walk func(*IR) error
	walk = func(current *IR) error {
		for _, step := range current.Steps {
			if step.Activity != nil {
				found[step.Activity.Name] = true
				if step.Activity.Name == "agent.invoke" {
					role, ok := step.With["role"].Literal.(string)
					if !ok {
						return fmt.Errorf("verified agent roles must be statically declared")
					}
					policy, ok := ir.Verification.Roles[role]
					if !ok {
						return fmt.Errorf("role %s has no model policy", role)
					}
					if policy.Purpose == "review" {
						schema, _ := step.With["outputSchema"].Literal.(string)
						if schema != "schema:review-claims@3" {
							return fmt.Errorf("verified review role %s requires review-claims@3", role)
						}
					}
					schema, _ := step.With["outputSchema"].Literal.(string)
					if schema == "schema:review-claims@3" && policy.Purpose != "review" {
						return fmt.Errorf("review claims require a read-only review role")
					}
				}
			}
			if step.Subflow != nil {
				if err := walk(step.Subflow); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(ir); err != nil {
		return err
	}
	for _, name := range []string{"checks.prepare", "checks.run", "risk.classify", "review.evaluate", "unit.close"} {
		if !found[name] {
			return fmt.Errorf("verified workflow requires %s", name)
		}
	}
	return nil
}
