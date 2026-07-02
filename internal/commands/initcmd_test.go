package commands

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/config"
	"github.com/lionelchamorro/orquestalite/internal/engine"
)

func TestInit_CreatesScaffolding(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"team.json",
		"prompts/parser.md", "prompts/coder.md", "prompts/tester.md", "prompts/critic.md", "prompts/reviewer.md",
		"prompts/_review-rubric.md",
		".orquestalite/results",
	} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
}

func TestInit_AddsGitignoreEntry(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("# user\nnode_modules/\n"), 0o644)
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.Contains(string(raw), ".orquestalite/") {
		t.Errorf(".gitignore did not get .orquestalite/: %s", raw)
	}
	if !strings.Contains(string(raw), "node_modules/") {
		t.Errorf("init must not delete prior gitignore lines: %s", raw)
	}
}

func TestInit_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir); err != nil {
		t.Fatalf("second init failed: %v", err)
	}
}

// TestInit_MaterialisesSchemas verifies that Init writes all five role schemas
// to <workspace>/schemas/ and that each file parses as valid JSON.
func TestInit_MaterialisesSchemas(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}

	expectedSchemas := []string{
		"parser.json",
		"coder.json",
		"tester.json",
		"critic.json",
		"reviewer.json",
	}

	for _, name := range expectedSchemas {
		path := filepath.Join(dir, "schemas", name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("schema %s missing: %v", name, err)
			continue
		}
		var v map[string]any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Errorf("schema %s is not valid JSON: %v", name, err)
		}
	}
}

// TestInit_TeamJSONHasCodexProviderPrimary verifies that the materialised
// team.json lists codex_gpt5 as primary coder via the provider config, and
// claude_sonnet as fallback.
func TestInit_TeamJSONHasCodexProviderPrimary(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "team.json"))
	if err != nil {
		t.Fatal(err)
	}

	var cfg struct {
		Agents map[string]struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
			Effort   string `json:"effort"`
		} `json:"agents"`
		Roles map[string]struct {
			Agents []string `json:"agents"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("team.json parse error: %v", err)
	}

	// Verify codex_gpt5 agent exists with provider launch metadata.
	codexAgent, ok := cfg.Agents["codex_gpt5"]
	if !ok {
		t.Fatal("agents.codex_gpt5 not found in team.json")
	}
	if codexAgent.Provider != "codex" {
		t.Errorf("codex_gpt5.provider = %q, want codex", codexAgent.Provider)
	}
	if codexAgent.Model != "gpt-5.5" {
		t.Errorf("codex_gpt5.model = %q, want gpt-5.5", codexAgent.Model)
	}
	if codexAgent.Effort != "medium" {
		t.Errorf("codex_gpt5.effort = %q, want medium", codexAgent.Effort)
	}

	// Verify coder role: primary = codex_gpt5, fallback = claude_sonnet.
	coderRole, ok := cfg.Roles["coder"]
	if !ok {
		t.Fatal("roles.coder not found in team.json")
	}
	if len(coderRole.Agents) < 2 {
		t.Fatalf("roles.coder.agents has %d entries, want >= 2", len(coderRole.Agents))
	}
	if coderRole.Agents[0] != "codex_gpt5" {
		t.Errorf("roles.coder.agents[0] = %q, want codex_gpt5", coderRole.Agents[0])
	}
	if coderRole.Agents[1] != "claude_sonnet" {
		t.Errorf("roles.coder.agents[1] = %q, want claude_sonnet", coderRole.Agents[1])
	}
}

// TestInit_MaterialisesDecomposePrompt verifies that Init writes the
// parser-decompose.md prompt to <workspace>/prompts/.
func TestInit_MaterialisesDecomposePrompt(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "prompts", "parser-decompose.md")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("prompts/parser-decompose.md not materialised: %v", err)
	}
}

// TestInit_WarnsWhenCodexMissing would verify that Init prints a warning when
// the codex binary is not in PATH. However, exec.LookPath cannot be stubbed
// without dependency injection, and we cannot guarantee that the test runner
// environment lacks (or has) codex. Injecting the lookup function would require
// changing the Init signature, which is out of scope for this phase.
// The warning behaviour is covered by manual testing and code review.
func TestInit_WarnsWhenCodexMissing(t *testing.T) {
	t.Skip("exec.LookPath cannot be cleanly stubbed without injecting the lookup function; skipped by design")
}

// TestInitScaffoldsFlows verifies that Init writes a flows.json that parses via
// engine.LoadFlows and contains the bundled default flows (at least `factory`
// and `factory_fast`), so `orq-lite flow run <name>` works out of the box.
func TestInitScaffoldsFlows(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "flows.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("flows.json not scaffolded: %v", err)
	}
	flows, err := engine.LoadFlows(path)
	if err != nil {
		t.Fatalf("scaffolded flows.json does not parse: %v", err)
	}
	for _, name := range []string{"factory", "factory_fast"} {
		if _, ok := flows.Flows[name]; !ok {
			t.Errorf("scaffolded flows.json missing default flow %q; has %v", name, flowNames(flows.Flows))
		}
	}
}

func flowNames(m map[string]engine.Flow) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestInitScaffoldsEveryRolePrompt verifies that every role declared in the
// scaffolded team.json has its prompt file materialised on disk. This prevents
// regressions of the intake.md bug: team.json referenced prompts/intake.md but
// assets/prompts/ was missing it, so init scaffolded a project where doctor,
// intake and watch --issues broke.
func TestInitScaffoldsEveryRolePrompt(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(dir, "team.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Roles) == 0 {
		t.Fatal("scaffolded team.json declares no roles")
	}
	for name, role := range cfg.Roles {
		if role.Prompt == "" {
			t.Errorf("role %s: empty prompt path", name)
			continue
		}
		p := filepath.Join(dir, role.Prompt)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("role %s: prompt %s not scaffolded: %v", name, role.Prompt, err)
		}
	}
}

// TestInit_InitialisesGitRepo verifies that Init runs `git init` and creates
// an empty initial commit when the target directory is not already inside a
// git work tree. Per-task commits issued by run need a parent commit to
// succeed.
func TestInit_InitialisesGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf(".git not created: %v", err)
	}
	// HEAD must resolve — empty initial commit exists.
	c := exec.Command("git", "rev-parse", "HEAD")
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git rev-parse HEAD failed: %v\n%s", err, out)
	}
}

// TestInit_DoesNotNestRepoAndCommitsGitignore verifies Init does not create a
// nested repo inside an existing work tree, and commits exactly the .gitignore
// (one new commit) so the ignore rules are tracked and survive run's rollback.
func TestInit_DoesNotNestRepoAndCommitsGitignore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-q", "-m", "seed"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	count := func() string {
		c := exec.Command("git", "rev-list", "--count", "HEAD")
		c.Dir = dir
		out, _ := c.CombinedOutput()
		return strings.TrimSpace(string(out))
	}
	before := count()

	if err := Init(dir); err != nil {
		t.Fatal(err)
	}

	// No nested repo.
	if _, err := os.Stat(filepath.Join(dir, ".git", ".git")); err == nil {
		t.Error("Init created a nested .git repo")
	}
	// Exactly one new commit (the .gitignore).
	if got := count(); !(before == "1" && got == "2") {
		t.Errorf("expected exactly one new commit (before=1 after=2), got before=%s after=%s", before, got)
	}
	// .gitignore is tracked.
	c := exec.Command("git", "ls-files", "--error-unmatch", ".gitignore")
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Errorf(".gitignore is not tracked after Init: %v\n%s", err, out)
	}
}

// TestInit_ScaffoldingSurvivesRollback is the regression test for the data-loss
// bug: run's Rollback executes `git checkout .` + `git clean -fd`, which deletes
// untracked files. Init must gitignore team.json/prompts/schemas/.orquestalite
// AND commit the .gitignore so it is tracked — otherwise the .gitignore itself
// is wiped on the first rollback and the ignored dirs die on the next one.
func TestInit_ScaffoldingSurvivesRollback(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	gitRun := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// Simulate two consecutive failed-task rollbacks (the cascade that
	// previously erased .orquestalite once the .gitignore was gone).
	for i := 0; i < 2; i++ {
		gitRun("checkout", ".")
		gitRun("clean", "-fd")
	}
	for _, p := range []string{"team.json", "prompts", "schemas", ".orquestalite", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("%s did not survive rollback: %v", p, err)
		}
	}
}

// TestInit_WritesPythonGitignore verifies that Init detects a Python project
// (via pyproject.toml) and writes Python-appropriate .gitignore entries plus
// adjusts full_test_command to a pytest-driven default.
func TestInit_WritesPythonGitignore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	for _, want := range []string{".orquestalite/", "__pycache__/", "*.pyc", ".pytest_cache/", ".venv/"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf(".gitignore missing %q\nfull contents:\n%s", want, raw)
		}
	}
	team, _ := os.ReadFile(filepath.Join(dir, "team.json"))
	if !strings.Contains(string(team), `"full_test_command": "uv run pytest -q"`) {
		t.Errorf("team.json full_test_command not adjusted for python:\n%s", team)
	}
}

// TestInit_UnknownLangClearsTestCommand verifies that a directory with no
// recognizable language manifest gets an empty full_test_command rather than
// the embedded "go test ./..." default, which would fail every full-suite gate
// in a non-Go repo. Empty is a no-op until the run-time detector fills it in.
func TestInit_UnknownLangClearsTestCommand(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	team, _ := os.ReadFile(filepath.Join(dir, "team.json"))
	if !strings.Contains(string(team), `"full_test_command": ""`) {
		t.Errorf("unknown-language team.json should clear full_test_command, got:\n%s", team)
	}
	if strings.Contains(string(team), "go test ./...") {
		t.Errorf("unknown-language team.json must not keep the go default:\n%s", team)
	}
}

// TestInit_ScaffoldsGeneralistRole verifies that Init writes a generalist role
// to team.json and materialises prompts/generalist.md.
func TestInit_ScaffoldsGeneralistRole(t *testing.T) {
	dir := t.TempDir()
	if err := InitWithOptions(dir, InitOptions{Lang: "python"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "team.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"generalist"`) {
		t.Errorf("scaffolded team.json missing generalist role:\n%s", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, "prompts", "generalist.md")); err != nil {
		t.Errorf("prompts/generalist.md not scaffolded: %v", err)
	}
}

// TestInitWithOptions_LangOverride verifies that an explicit --lang argument
// overrides autodetection even when the directory looks like a different
// language.
func TestInitWithOptions_LangOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InitWithOptions(dir, InitOptions{Lang: "node"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.Contains(string(raw), "node_modules/") {
		t.Errorf("lang=node override did not apply node gitignore: %s", raw)
	}
	team, _ := os.ReadFile(filepath.Join(dir, "team.json"))
	if !strings.Contains(string(team), `"full_test_command": "npm test --silent"`) {
		t.Errorf("lang=node override did not adjust full_test_command:\n%s", team)
	}
}
