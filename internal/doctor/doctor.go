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
	"regexp"
	"sort"
	"strings"

	"github.com/collectiveai-team/orquesta-lite/internal/config"
	"github.com/collectiveai-team/orquesta-lite/internal/contextopt"
	"github.com/collectiveai-team/orquesta-lite/internal/flow"
	"github.com/collectiveai-team/orquesta-lite/internal/gitx"
	"github.com/collectiveai-team/orquesta-lite/internal/opencodeattach"
	"github.com/collectiveai-team/orquesta-lite/internal/providers"
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
// than block.
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

	packRoot := filepath.Join(dir, ".orquestalite", "packs", "development", "5")
	if pack, packErr := flow.LoadPack(packRoot); packErr != nil {
		add(StatusError, "pack:development", packErr.Error())
	} else {
		add(StatusOK, "pack:development", pack.Name+"@"+pack.Version+" verified")
	}

	// prompts referenced by roles
	missing := missingPromptFiles(dir, cfg)
	if len(missing) > 0 {
		add(StatusError, "prompts", "missing: "+strings.Join(missing, ", "))
	} else {
		add(StatusOK, "prompts", "all role prompt files exist")
	}

	// agent binaries + credentials, per provider actually used
	for _, providerName := range usedProviders(cfg) {
		path, err := exec.LookPath(providerName)
		if err != nil {
			add(StatusError, "provider:"+providerName, "not on PATH")
			continue
		}
		provider, err := providers.New(providerName)
		if err != nil {
			add(StatusError, "provider:"+providerName, err.Error())
			continue
		}
		status, detail := checkProviderCLI(ctx, path, provider, usedAgents(cfg, providerName), cfg.Limits.SessionResumeEnabled())
		add(status, "provider:"+providerName, detail)

		cred, ok := credentialPaths[providerName]
		if !ok {
			continue
		}
		if os.Getenv(cred.envVar) != "" {
			add(StatusOK, "credentials:"+providerName, "via "+cred.envVar)
			continue
		}
		if f := firstExistingHomeFile(cred.files); f != "" {
			add(StatusOK, "credentials:"+providerName, "credentials at ~/"+f)
		} else {
			add(StatusWarn, "credentials:"+providerName, fmt.Sprintf("no credentials found (~/%s or %s) — log in with the CLI once", cred.files[0], cred.envVar))
		}
	}

	checks = append(checks, checkAttach(ctx, cfg.Attach)...)

	// context optimization — external tools that shrink what each invocation
	// carries. Neither is vendored, so absence is a warning and never a
	// failure: the run proceeds unoptimized. Reporting them here matters
	// because a silently skipped optimization looks exactly like one that ran.
	{
		co := cfg.Runtime.ContextOptimization
		st := contextopt.Activate(co, dir)
		if !st.ProxyEnabled {
			add(StatusOK, "compression_proxy", "disabled in team.json")
		} else if st.ProxyReachable {
			add(StatusOK, "compression_proxy", "reachable at "+st.ProxyURL)
		} else {
			add(StatusWarn, "compression_proxy", fmt.Sprintf(
				"enabled but nothing is listening at %s — runs proceed without it; see GUIDE.md to start it", st.ProxyURL))
		}
		switch {
		case !st.FilterEnabled:
			add(StatusOK, "command_filter", "disabled in team.json")
		case st.FilterVerified:
			add(StatusOK, "command_filter", "verified: "+st.FilterBinary)
		case st.FilterBinary == "":
			add(StatusWarn, "command_filter", fmt.Sprintf(
				"enabled but %q is not on PATH — runs proceed without it; see GUIDE.md to install it", co.FilterBinary()))
		default:
			add(StatusWarn, "command_filter", fmt.Sprintf(
				"%s found but did not verify — runs proceed without it", st.FilterBinary))
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
	}
	sort.Strings(missing)
	return missing
}

// checkAttach reports whether a configured opencode attach server is reachable.
//
// Attach failures are fatal at run start by design, so the whole point of this
// check is to move that failure somewhere diagnosable: `doctor` before the run,
// not minute twelve of one. It is deliberately a standalone function rather than
// inline in Run — a future provider-CLI check would want to absorb it, and a
// move is a cheaper merge than a rewrite.
func checkAttach(ctx context.Context, attach config.Attach) []Check {
	if !attach.Enabled() {
		return nil
	}
	client, err := opencodeattach.NewClient(attach.URL)
	if err != nil {
		return []Check{{Status: StatusError, Name: "attach", Detail: err.Error()}}
	}
	if err := client.Ping(ctx); err != nil {
		return []Check{{Status: StatusError, Name: "attach", Detail: fmt.Sprintf(
			"%v — runs will fail to start; run `opencode serve` or remove attach.url from team.json", err)}}
	}
	return []Check{{Status: StatusOK, Name: "attach", Detail: "opencode server reachable at " + client.URL()}}
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

func usedAgents(cfg *config.Config, providerName string) []config.Agent {
	seen := map[string]bool{}
	var agents []config.Agent
	for _, role := range cfg.Roles {
		for _, name := range append(append([]string{}, role.Agents...), role.EscalationLadder...) {
			agent, ok := cfg.Agents[name]
			if !ok || seen[name] || agent.Provider != providerName {
				continue
			}
			seen[name] = true
			agents = append(agents, agent)
		}
	}
	return agents
}

var (
	ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	helpFlag   = regexp.MustCompile(`--?[A-Za-z][A-Za-z0-9-]*`)
	helpGap    = regexp.MustCompile(`\s{2,}`)
)

func checkProviderCLI(ctx context.Context, executable string, provider providers.Provider, agents []config.Agent, resume bool) (Status, string) {
	required := map[string]bool{}
	for _, agent := range agents {
		if err := provider.ValidateExtraArgs(agent.ExtraArgs); err != nil {
			return StatusError, err.Error()
		}
		opts := providers.Options{
			Model:                agent.Model,
			Effort:               agent.Effort,
			DangerouslySkipPerms: agent.DangerouslySkipPermissions,
			SafeMode:             agent.SafeMode,
			ExtraArgs:            agent.ExtraArgs,
		}
		if resume {
			opts.ResumeSessionID = "doctor-session"
		}
		launch, err := provider.Build(ctx, "doctor prompt", opts)
		if err != nil {
			return StatusError, "cannot build provider argv: " + err.Error()
		}
		for _, arg := range launch.Args[1:] {
			if flag := emittedFlag(arg); flag != "" {
				required[flag] = true
			}
		}
	}

	help := provider.CLIHelp()
	if len(help.Args) < 2 {
		return StatusError, "provider has no CLI help contract"
	}
	cmd := exec.CommandContext(ctx, executable, help.Args[1:]...)
	raw, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return StatusWarn, "could not verify CLI flags before timeout: " + ctx.Err().Error()
	}
	if err != nil {
		return StatusError, fmt.Sprintf("could not verify CLI flags with %q: %v", strings.Join(help.Args, " "), err)
	}
	text := ansiEscape.ReplaceAllString(strings.ReplaceAll(string(raw), "\r\n", "\n"), "")
	if help.Synopsis != "" && !strings.Contains(text, help.Synopsis) {
		return StatusError, fmt.Sprintf("could not verify CLI flags: %q help did not contain synopsis %q", strings.Join(help.Args, " "), help.Synopsis)
	}
	declared := declaredHelpFlags(text)
	if len(declared) == 0 {
		return StatusError, fmt.Sprintf("could not verify CLI flags: %q produced no parseable options", strings.Join(help.Args, " "))
	}
	var missing []string
	for flag := range required {
		if !declared[flag] {
			missing = append(missing, flag)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return StatusError, "CLI help does not declare emitted flags: " + strings.Join(missing, ", ")
	}
	return StatusOK, "on PATH; emitted flags verified against --help"
}

func emittedFlag(arg string) string {
	if arg == "-" || len(arg) < 2 || arg[0] != '-' {
		return ""
	}
	if i := strings.IndexByte(arg, '='); i >= 0 {
		arg = arg[:i]
	}
	if len(arg) >= 3 && arg[1] != '-' {
		if (arg[1] >= 'A' && arg[1] <= 'Z') || (arg[1] >= 'a' && arg[1] <= 'z') {
			return arg[:2]
		}
		return ""
	}
	if len(arg) < 3 || !((arg[2] >= 'A' && arg[2] <= 'Z') || (arg[2] >= 'a' && arg[2] <= 'z')) {
		return ""
	}
	return arg
}

func declaredHelpFlags(help string) map[string]bool {
	flags := map[string]bool{}
	for _, line := range strings.Split(help, "\n") {
		declaration := strings.TrimSpace(line)
		if !strings.HasPrefix(declaration, "-") {
			continue
		}
		if i := helpGap.FindStringIndex(declaration); i != nil {
			declaration = declaration[:i[0]]
		}
		for _, flag := range helpFlag.FindAllString(declaration, -1) {
			flags[flag] = true
		}
	}
	return flags
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
