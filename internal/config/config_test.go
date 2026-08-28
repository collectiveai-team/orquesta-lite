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

func TestExtraArgsResolveOnlyForProviderAgents(t *testing.T) {
	path := writeConfig(t, `{
      "agents":{"ok":{"provider":"opencode","extra_args":["--thinking"]}},
      "roles":{"used":{"agents":["ok"],"prompt":"used.md","result_path":"used.json","timeout_seconds":1}},
      "rate_limit_backoff":{"initial_seconds":1,"factor":2,"max_seconds":2}
    }`)
	config, err := LoadDynamic(path)
	if err != nil {
		t.Fatal(err)
	}
	roles, err := config.ResolveRoles([]string{"used"})
	if err != nil {
		t.Fatal(err)
	}
	if got := roles["used"].Agents[0].ExtraArgs; len(got) != 1 || got[0] != "--thinking" {
		t.Fatalf("extra args = %v", got)
	}
}

func TestLoadDynamicRejectsInvalidReferencedCommand(t *testing.T) {
	for name, agent := range map[string]string{
		"missing prompt marker": `{"cmd":["fake"]}`,
		"cmd with extra args":   `{"cmd":["fake","{{PROMPT}}"],"extra_args":["--verbose"]}`,
		"cmd and provider":      `{"cmd":["fake","{{PROMPT}}"],"provider":"opencode"}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := writeConfig(t, `{
          "agents":{"bad":`+agent+`},
          "roles":{"used":{"agents":["bad"],"prompt":"used.md","result_path":"used.json","timeout_seconds":1}}
        }`)
			config, err := LoadDynamic(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := config.ResolveRoles([]string{"used"}); err == nil {
				t.Fatal("expected referenced agent validation error")
			}
		})
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

func TestConfigRejectsBadAttachURL(t *testing.T) {
	// A typo'd attach URL has to fail at load. Attach failures are fatal at run
	// start by design, so the cost of deferring this is a run that dies on
	// startup with a connection error instead of a config file that says why.
	body := `{"agents":{"a":{"provider":"opencode"}},"roles":{"r":{"agents":["a"],"prompt":"p","result_path":"r","timeout_seconds":1}},"rate_limit_backoff":{"initial_seconds":1,"factor":2,"max_seconds":2},"attach":{"url":"127.0.0.1:4096"}}`
	if _, err := Load(writeConfig(t, body)); err == nil {
		t.Fatal("a scheme-less attach.url was accepted")
	}
}

func TestConfigAcceptsValidAttachURL(t *testing.T) {
	body := `{"agents":{"a":{"provider":"opencode"}},"roles":{"r":{"agents":["a"],"prompt":"p","result_path":"r","timeout_seconds":1}},"rate_limit_backoff":{"initial_seconds":1,"factor":2,"max_seconds":2},"attach":{"url":"http://127.0.0.1:4096"}}`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Attach.Enabled() {
		t.Fatal("attach.url set but Enabled() is false")
	}
}

// Attach is opt-in: a team.json that says nothing about it must stay valid and
// leave every agent running detached, exactly as before.
func TestConfigAttachDisabledByDefault(t *testing.T) {
	if (Attach{}).Enabled() {
		t.Fatal("attach enabled with no url")
	}
	if err := (Attach{}).Validate(); err != nil {
		t.Fatalf("empty attach failed validation: %v", err)
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
