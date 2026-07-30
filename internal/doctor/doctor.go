// Package doctor runs preflight checks against a project directory and
// reports the results as a slice of Check values. It is shared verbatim
// between the CLI (internal/commands/doctorcmd.go) and the HTTP endpoint
// (GET /api/doctor) so the two never disagree.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lionelchamorro/orquestalite/internal/config"
	"github.com/lionelchamorro/orquestalite/internal/eventdb"
	"github.com/lionelchamorro/orquestalite/internal/gitx"
)

// Status of one check. These exact strings are the GET /api/doctor contract;
// the CLI maps them to PASS/WARN/FAIL for display.
type Status string

const (
	StatusOK    Status = "ok"
	StatusWarn  Status = "warn"
	StatusError Status = "error"
)

// Check is the result of one preflight check.
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
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

// Run executes all preflight checks against dir and returns one Check per
// concern. ctx is the budget for checks that shell out — callers may pass a
// ~2 s timeout and such checks must degrade to StatusWarn on ctx.Err() rather
// than block. Today every check is exec.LookPath + stat + env reads, so none
// consult ctx yet; it is reserved for future exec-based checks.
func Run(ctx context.Context, dir string) []Check {
	var checks []Check
	add := func(status Status, name, detail string) {
		checks = append(checks, Check{Status: status, Name: name, Detail: detail})
	}

	// git
	if _, err := exec.LookPath("git"); err != nil {
		add(StatusError, "git", "not on PATH")
	} else if !gitx.IsRepo(dir) {
		add(StatusWarn, "git repo", "not a git repository — tasks complete but commits are skipped; factory mode requires one")
	} else if clean, err := gitx.IsCleanTree(dir); err == nil && !clean {
		add(StatusWarn, "git tree", "work tree has uncommitted changes — factory mode requires a clean tree")
	} else {
		add(StatusOK, "git", "repository present, tree clean")
	}

	// eventdb: the sqlite read-model behind the query API
	dbPath := filepath.Join(dir, ".orquestalite", "orq.db")
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		add(StatusWarn, "eventdb", "orq.db not found — run `orq-lite index` or `orq-lite serve` to build it")
	} else if db, err := eventdb.Open(dbPath); err != nil {
		add(StatusError, "eventdb", err.Error())
	} else {
		_ = db.Close()
		add(StatusOK, "eventdb", dbPath)
	}

	// team.json
	cfg, err := config.Load(filepath.Join(dir, "team.json"))
	if err != nil {
		add(StatusError, "team.json", err.Error())
		return checks // everything below depends on the config
	}
	if _, err := cfg.ResolveAll(); err != nil {
		add(StatusError, "team.json", "resolve: "+err.Error())
		return checks
	}
	add(StatusOK, "team.json", "loads and resolves")
	if missing := cfg.MissingOrchestratedRoles(); len(missing) > 0 {
		add(StatusWarn, "legacy roles", "missing "+strings.Join(missing, ", ")+" — BLOCKING for plan/run/factory; safe to ignore if you only use `flow run`")
	}

	// prompts referenced by roles
	missing := missingPromptFiles(dir, cfg)
	if len(missing) > 0 {
		add(StatusError, "prompts", "missing: "+strings.Join(missing, ", "))
	} else {
		add(StatusOK, "prompts", "all role prompt files exist")
	}

	// agent binaries + credentials, per provider actually used
	for _, provider := range usedProviders(cfg) {
		if _, err := exec.LookPath(provider); err != nil {
			add(StatusError, "provider:"+provider, "not on PATH")
			continue
		}
		add(StatusOK, "provider:"+provider, "on PATH")

		cred, ok := credentialPaths[provider]
		if !ok {
			continue
		}
		if os.Getenv(cred.envVar) != "" {
			add(StatusOK, "credentials:"+provider, "via "+cred.envVar)
			continue
		}
		if f := firstExistingHomeFile(cred.files); f != "" {
			add(StatusOK, "credentials:"+provider, "credentials at ~/"+f)
		} else {
			add(StatusWarn, "credentials:"+provider, fmt.Sprintf("no credentials found (~/%s or %s) — log in with the CLI once", cred.files[0], cred.envVar))
		}
	}

	// conventions_file (optional house-style injection)
	if cfg.ConventionsFile != "" {
		if _, err := os.Stat(filepath.Join(dir, cfg.ConventionsFile)); errors.Is(err, os.ErrNotExist) {
			add(StatusWarn, "conventions_file", cfg.ConventionsFile+" not found — agents will infer style from the codebase instead")
		} else {
			add(StatusOK, "conventions_file", cfg.ConventionsFile)
		}
	}

	// full_test_command
	if cfg.FullTestCommand == "" {
		add(StatusWarn, "full_test_command", "empty — no full-suite gate before commits")
	} else {
		bin := strings.Fields(cfg.FullTestCommand)[0]
		if _, err := exec.LookPath(bin); err != nil {
			add(StatusError, "full_test_command", fmt.Sprintf("%q not on PATH (command: %s)", bin, cfg.FullTestCommand))
		} else {
			add(StatusOK, "full_test_command", cfg.FullTestCommand)
		}
	}

	// lint_argv / test_argv — the argv gates a v2 flow reads through the
	// read-only `config.` namespace. A flow that references one the project
	// does not declare is rejected before its run is even created, so doctor
	// has to say so: it is the command an adopter runs before their first
	// flow, and reporting "all checks passed" on a project whose next command
	// cannot start is the failure this check exists to prevent.
	for _, gate := range []struct {
		name string
		argv []string
	}{{"lint_argv", cfg.LintArgv}, {"test_argv", cfg.TestArgv}} {
		switch {
		case len(gate.argv) == 0:
			add(StatusWarn, gate.name, "not set — flows referencing config."+gate.name+" (the governed pack's gates) refuse to start; set it to an argv array, e.g. [\"go\", \"test\", \"./...\"]")
		case gate.argv[0] == "":
			add(StatusError, gate.name, "first element is empty — a gate that runs nothing is worse than one that fails")
		default:
			if _, err := exec.LookPath(gate.argv[0]); err != nil {
				add(StatusError, gate.name, fmt.Sprintf("%q not on PATH (argv: %s)", gate.argv[0], strings.Join(gate.argv, " ")))
			} else {
				add(StatusOK, gate.name, strings.Join(gate.argv, " "))
			}
		}
	}

	// optional tooling
	if _, err := exec.LookPath("agtop"); err != nil {
		add(StatusWarn, "binary:agtop", "not on PATH — cost tracking disabled")
	} else {
		add(StatusOK, "binary:agtop", "cost tracking available")
	}
	if _, err := exec.LookPath("gh"); err != nil {
		add(StatusWarn, "binary:gh", "not on PATH — factory --pr disabled")
	} else {
		add(StatusOK, "binary:gh", "PR creation available")
	}
	if _, err := exec.LookPath("agent-browser"); err != nil {
		add(StatusWarn, "binary:agent-browser", "not on PATH — visual features fall back to playwright/curl; install with `npm i -g agent-browser && agent-browser install`")
	} else {
		add(StatusOK, "binary:agent-browser", "browser-driven visual verification available")
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

// ProviderHasUsableCredentials reports whether a provider can authenticate
// non-interactively: either its API-key env var is set, or a cached login file
// exists. Providers without a known credential profile are assumed usable (we
// cannot tell). Shared by the doctor check and the run-time static preflight.
func ProviderHasUsableCredentials(provider string) bool {
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
