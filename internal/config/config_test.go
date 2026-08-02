package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "team.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const validTeam = `{
  "agents": {
    "primary": {"provider":"codex","model":"gpt-5.5"},
    "fallback": {"cmd":["fake","{{PROMPT}}"]}
  },
  "roles": {
    "coder": {"agents":["primary"],"escalation_ladder":["fallback"],"prompt":"prompts/coder.md","result_path":".orquestalite/results/coder.json","timeout_seconds":60}
  },
  "limits":{"resume_sessions":true},
  "rate_limit_backoff":{"initial_seconds":1,"factor":2,"max_seconds":8},
  "runtime":{"retention_runs":7,"artifact_max_bytes":1024},
  "lint_argv":["go","vet","./..."],
  "test_argv":["go","test","./..."]
}`

func TestLoadAndResolveDeclaredRoles(t *testing.T) {
	config, err := Load(writeConfig(t, validTeam))
	if err != nil {
		t.Fatal(err)
	}
	roles, err := config.ResolveAll()
	if err != nil {
		t.Fatal(err)
	}
	coder := roles["coder"]
	if len(coder.Agents) != 1 || coder.Agents[0].Provider != "codex" || len(coder.EscalationLadder) != 1 {
		t.Fatalf("coder = %+v", coder)
	}
	if coder.PromptPath != "prompts/coder.md" || coder.Timeout.Seconds() != 60 {
		t.Fatalf("coder = %+v", coder)
	}
}

func TestLoadDynamicValidatesOnlyReferencedRoles(t *testing.T) {
	path := writeConfig(t, `{
      "agents":{"ok":{"cmd":["fake","{{PROMPT}}"]}},
      "roles":{
        "used":{"agents":["ok"],"prompt":"used.md","result_path":"used.json","timeout_seconds":1},
        "broken":{"agents":["missing"],"prompt":"broken.md","result_path":"broken.json","timeout_seconds":1}
      }
    }`)
	config, err := LoadDynamic(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.ResolveRoles([]string{"used"}); err != nil {
		t.Fatalf("unreferenced role blocked resolution: %v", err)
	}
	if _, err := config.ResolveRoles([]string{"broken"}); err == nil {
		t.Fatal("referenced invalid role should fail")
	}
}

func TestValidationRejectsInvalidAgentAndBackoff(t *testing.T) {
	for name, body := range map[string]string{
		"unknown provider":      `{"agents":{"a":{"provider":"bogus"}},"roles":{"r":{"agents":["a"],"prompt":"p","result_path":"r","timeout_seconds":1}},"rate_limit_backoff":{"initial_seconds":1,"factor":2,"max_seconds":2}}`,
		"missing prompt marker": `{"agents":{"a":{"cmd":["fake"]}},"roles":{"r":{"agents":["a"],"prompt":"p","result_path":"r","timeout_seconds":1}},"rate_limit_backoff":{"initial_seconds":1,"factor":2,"max_seconds":2}}`,
		"bad backoff":           `{"agents":{"a":{"cmd":["fake","{{PROMPT}}"]}},"roles":{"r":{"agents":["a"],"prompt":"p","result_path":"r","timeout_seconds":1}},"rate_limit_backoff":{"initial_seconds":2,"factor":1,"max_seconds":1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, body)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestRuntimeAndSessionDefaults(t *testing.T) {
	if !(Limits{}).SessionResumeEnabled() {
		t.Fatal("session resume should default on")
	}
	if (Runtime{}).RetentionCeiling() != 20 || (Runtime{}).ArtifactLimit() != 8<<20 {
		t.Fatal("unexpected runtime defaults")
	}
	if (Runtime{RetentionRuns: 3, ArtifactMaxBytes: 99}).RetentionCeiling() != 3 || (Runtime{ArtifactMaxBytes: 99}).ArtifactLimit() != 99 {
		t.Fatal("runtime overrides ignored")
	}
}
