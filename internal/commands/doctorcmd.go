package commands

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lionelchamorro/orquestalite/internal/config"
	"github.com/lionelchamorro/orquestalite/internal/gitx"
)

type checkLevel string

const (
	checkPass checkLevel = "PASS"
	checkWarn checkLevel = "WARN"
	checkFail checkLevel = "FAIL"
)

type check struct {
	Level  checkLevel
	Name   string
	Detail string
}

// credentialPaths maps a provider to the files (relative to $HOME) any one of
// which indicates an interactive login, and the API-key env var alternative.
var credentialPaths = map[string]struct {
	files  []string
	envVar string
}{
	"claude":   {files: []string{".claude.json", ".claude/.credentials.json"}, envVar: "ANTHROPIC_API_KEY"},
	"codex":    {files: []string{".codex/auth.json"}, envVar: "OPENAI_API_KEY"},
	"gemini":   {files: []string{".gemini/oauth_creds.json", ".gemini/google_accounts.json"}, envVar: "GEMINI_API_KEY"},
	"opencode": {files: []string{".local/share/opencode/auth.json"}, envVar: ""},
}

// Doctor preflights the whole setup — git state, team.json, prompts, agent
// CLIs, credentials, test command, optional tooling — before any money is
// spent on agent calls. Misconfiguration is the top source of wasted runs
// (see tasks/orq-lite-fastapi-sse-test-findings.md). Returns an error iff
// any FAIL-level check fires.
func Doctor(projectDir string, out io.Writer) error {
	checks := runDoctorChecks(projectDir)

	failed := 0
	for _, c := range checks {
		fmt.Fprintf(out, "[%s] %-22s %s\n", c.Level, c.Name, c.Detail)
		if c.Level == checkFail {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("doctor: %d check(s) failed", failed)
	}
	fmt.Fprintln(out, "\nall checks passed")
	return nil
}

func runDoctorChecks(dir string) []check {
	var checks []check
	add := func(level checkLevel, name, detail string) {
		checks = append(checks, check{Level: level, Name: name, Detail: detail})
	}

	// git
	if _, err := exec.LookPath("git"); err != nil {
		add(checkFail, "git", "not on PATH")
	} else if !gitx.IsRepo(dir) {
		add(checkWarn, "git repo", "not a git repository — tasks complete but commits are skipped; factory mode requires one")
	} else if clean, err := gitx.IsCleanTree(dir); err == nil && !clean {
		add(checkWarn, "git tree", "work tree has uncommitted changes — factory mode requires a clean tree")
	} else {
		add(checkPass, "git", "repository present, tree clean")
	}

	// team.json
	cfg, err := config.Load(filepath.Join(dir, "team.json"))
	if err != nil {
		add(checkFail, "team.json", err.Error())
		return checks // everything below depends on the config
	}
	if _, err := cfg.Resolve(); err != nil {
		add(checkFail, "team.json", "resolve: "+err.Error())
		return checks
	}
	add(checkPass, "team.json", "loads and resolves")

	// prompts referenced by roles
	missing := missingPromptFiles(dir, cfg)
	if len(missing) > 0 {
		add(checkFail, "prompts", "missing: "+strings.Join(missing, ", "))
	} else {
		add(checkPass, "prompts", "all role prompt files exist")
	}

	// agent binaries + credentials, per provider actually used
	for _, provider := range usedProviders(cfg) {
		if _, err := exec.LookPath(provider); err != nil {
			add(checkFail, provider+" CLI", "not on PATH")
			continue
		}
		add(checkPass, provider+" CLI", "on PATH")

		cred, ok := credentialPaths[provider]
		if !ok {
			continue
		}
		if os.Getenv(cred.envVar) != "" {
			add(checkPass, provider+" auth", "via "+cred.envVar)
			continue
		}
		if f := firstExistingHomeFile(cred.files); f != "" {
			add(checkPass, provider+" auth", "credentials at ~/"+f)
		} else {
			add(checkWarn, provider+" auth", fmt.Sprintf("no credentials found (~/%s or %s) — log in with the CLI once", cred.files[0], cred.envVar))
		}
	}

	// conventions_file (optional house-style injection)
	if cfg.ConventionsFile != "" {
		if _, err := os.Stat(filepath.Join(dir, cfg.ConventionsFile)); errors.Is(err, os.ErrNotExist) {
			add(checkWarn, "conventions_file", cfg.ConventionsFile+" not found — agents will infer style from the codebase instead")
		} else {
			add(checkPass, "conventions_file", cfg.ConventionsFile)
		}
	}

	// full_test_command
	if cfg.FullTestCommand == "" {
		add(checkWarn, "full_test_command", "empty — no full-suite gate before commits")
	} else {
		bin := strings.Fields(cfg.FullTestCommand)[0]
		if _, err := exec.LookPath(bin); err != nil {
			add(checkFail, "full_test_command", fmt.Sprintf("%q not on PATH (command: %s)", bin, cfg.FullTestCommand))
		} else {
			add(checkPass, "full_test_command", cfg.FullTestCommand)
		}
	}

	// optional tooling
	if _, err := exec.LookPath("agtop"); err != nil {
		add(checkWarn, "agtop", "not on PATH — cost tracking disabled")
	} else {
		add(checkPass, "agtop", "cost tracking available")
	}
	if _, err := exec.LookPath("gh"); err != nil {
		add(checkWarn, "gh", "not on PATH — factory --pr disabled")
	} else {
		add(checkPass, "gh", "PR creation available")
	}
	if _, err := exec.LookPath("agent-browser"); err != nil {
		add(checkWarn, "agent-browser", "not on PATH — visual features fall back to playwright/curl; install with `npm i -g agent-browser && agent-browser install`")
	} else {
		add(checkPass, "agent-browser", "browser-driven visual verification available")
	}

	return checks
}

func missingPromptFiles(dir string, cfg *config.Config) []string {
	seen := map[string]bool{}
	var missing []string
	checkFile := func(rel string) {
		if rel == "" || seen[rel] {
			return
		}
		seen[rel] = true
		if _, err := os.Stat(filepath.Join(dir, rel)); errors.Is(err, os.ErrNotExist) {
			missing = append(missing, rel)
		}
	}
	for _, role := range cfg.Roles {
		checkFile(role.Prompt)
		checkFile(role.DecomposePrompt)
		checkFile(role.CyclePrompt)
	}
	sort.Strings(missing)
	return missing
}

// usedProviders returns the sorted set of providers referenced by any role's
// agents or escalation ladder.
func usedProviders(cfg *config.Config) []string {
	used := map[string]bool{}
	for _, role := range cfg.Roles {
		for _, name := range append(append([]string{}, role.Agents...), role.EscalationLadder...) {
			if ag, ok := cfg.Agents[name]; ok && ag.Provider != "" {
				used[ag.Provider] = true
			}
		}
	}
	out := make([]string, 0, len(used))
	for p := range used {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// providerHasUsableCredentials reports whether a provider can authenticate
// non-interactively: either its API-key env var is set, or a cached login file
// exists. Providers without a known credential profile are assumed usable (we
// cannot tell). Shared by the doctor check and the run-time static preflight.
func providerHasUsableCredentials(provider string) bool {
	cred, ok := credentialPaths[provider]
	if !ok {
		return true
	}
	if os.Getenv(cred.envVar) != "" {
		return true
	}
	return firstExistingHomeFile(cred.files) != ""
}

func firstExistingHomeFile(rels []string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, rel := range rels {
		if _, err := os.Stat(filepath.Join(home, rel)); err == nil {
			return rel
		}
	}
	return ""
}
