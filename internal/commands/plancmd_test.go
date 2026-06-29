package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/results"
	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

type stubParserCaller struct {
	out results.ParserResult
}

func (s *stubParserCaller) RunParser(ctx context.Context, plan string) (*results.ParserResult, error) {
	return &s.out, nil
}

func TestPlan_WritesTasksJSON(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.md")
	_ = os.WriteFile(planPath, []byte("# build x\nadd login flow"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, ".orquestalite"), 0o755)

	stub := &stubParserCaller{out: results.ParserResult{Tasks: []results.ParserTask{
		{Title: "scaffold", Description: "set up repo", Priority: 1},
		{Title: "add login route", Description: "POST /login", Priority: 2},
	}}}

	if err := Plan(context.Background(), dir, planPath, false, stub); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, ".orquestalite", "tasks.json"))
	var tl tasks.TaskList
	if err := json.Unmarshal(raw, &tl); err != nil {
		t.Fatal(err)
	}
	if len(tl.Tasks) != 2 || tl.Tasks[0].ID != "T001" || tl.Tasks[1].ID != "T002" {
		t.Fatalf("got tasks: %+v", tl.Tasks)
	}
	if tl.Tasks[0].Status != tasks.StatusPending {
		t.Errorf("status not initialised")
	}
}

func TestPlan_AppendPreservesExisting(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".orquestalite"), 0o755)
	prev := &tasks.TaskList{Tasks: []tasks.Task{{ID: "T001", Title: "old", Status: tasks.StatusDone}}}
	raw, _ := json.MarshalIndent(prev, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, ".orquestalite", "tasks.json"), raw, 0o644)

	planPath := filepath.Join(dir, "plan.md")
	_ = os.WriteFile(planPath, []byte("more"), 0o644)
	stub := &stubParserCaller{out: results.ParserResult{Tasks: []results.ParserTask{{Title: "new", Description: "y", Priority: 1}}}}

	if err := Plan(context.Background(), dir, planPath, true, stub); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(filepath.Join(dir, ".orquestalite", "tasks.json"))
	var tl tasks.TaskList
	_ = json.Unmarshal(out, &tl)
	if len(tl.Tasks) != 2 || tl.Tasks[1].ID != "T002" {
		t.Fatalf("append failed: %+v", tl.Tasks)
	}
}

func TestPlan_MapsSquadFromParserTask(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.md")
	_ = os.WriteFile(planPath, []byte("plan"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, ".orquestalite"), 0o755)

	stub := &stubParserCaller{out: results.ParserResult{Tasks: []results.ParserTask{
		{Title: "scaffold", Description: "set up repo", Priority: 1, Squad: tasks.SquadSetup},
		{Title: "add feature", Description: "feature", Priority: 2},
	}}}

	if err := Plan(context.Background(), dir, planPath, false, stub); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, ".orquestalite", "tasks.json"))
	var tl tasks.TaskList
	if err := json.Unmarshal(raw, &tl); err != nil {
		t.Fatal(err)
	}
	if tl.Tasks[0].Squad != tasks.SquadSetup {
		t.Errorf("T001 squad = %q, want %q", tl.Tasks[0].Squad, tasks.SquadSetup)
	}
	if tl.Tasks[1].SquadOrDefault() != tasks.SquadFull {
		t.Errorf("T002 should default to full, got %q", tl.Tasks[1].SquadOrDefault())
	}
}

func TestPlan_CarriesSkillsOntoTasks(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.md")
	_ = os.WriteFile(planPath, []byte("plan needing tdd"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, ".orquestalite"), 0o755)

	stub := &stubParserCaller{out: results.ParserResult{Tasks: []results.ParserTask{
		{Title: "scaffold", Description: "set up repo", Priority: 1, Skills: []string{"tdd"}},
		{Title: "add login route", Description: "POST /login", Priority: 2},
	}}}

	if err := Plan(context.Background(), dir, planPath, false, stub); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, ".orquestalite", "tasks.json"))
	var tl tasks.TaskList
	if err := json.Unmarshal(raw, &tl); err != nil {
		t.Fatal(err)
	}
	if len(tl.Tasks[0].Skills) != 1 || tl.Tasks[0].Skills[0] != "tdd" {
		t.Errorf("T001 skills = %v, want [tdd]", tl.Tasks[0].Skills)
	}
	if len(tl.Tasks[1].Skills) != 0 {
		t.Errorf("T002 skills = %v, want empty", tl.Tasks[1].Skills)
	}
}

func TestPlanWithLiveCallerPreflightsOnlyParserAgents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell script test, skipping on windows")
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".orquestalite", "results"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "parser.md"), []byte("parse {{PLAN}}"), 0o644); err != nil {
		t.Fatal(err)
	}

	cliPath := filepath.Join(dir, "fakecli.sh")
	if err := os.WriteFile(cliPath, []byte(fakeCLI), 0o755); err != nil {
		t.Fatalf("write fakecli.sh: %v", err)
	}
	writePlanPreflightTeamJSON(t, dir, cliPath)

	planPath := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(planPath, []byte("# plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := PlanWithLiveCaller(context.Background(), dir, planPath, false); err != nil {
		t.Fatalf("PlanWithLiveCaller: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, ".orquestalite", "run.log"))
	if err != nil {
		t.Fatalf("read run.log: %v", err)
	}
	log := string(raw)
	if !strings.Contains(log, `"agent":"parser_missing"`) {
		t.Fatalf("run.log missing parser preflight skip:\n%s", log)
	}
	if strings.Contains(log, `"agent":"coder_missing"`) {
		t.Fatalf("plan preflight reported non-parser agent:\n%s", log)
	}
}

func writePlanPreflightTeamJSON(t *testing.T, dir, cliPath string) {
	t.Helper()

	resultDir := filepath.Join(dir, ".orquestalite", "results")
	missing := func(name string) string {
		return filepath.Join(dir, name)
	}
	parserResult := filepath.Join(resultDir, "parser.json")
	roleResult := func(kind string) string {
		return filepath.Join(".orquestalite", "results", kind+".json")
	}

	type agentDef struct {
		Cmd []string `json:"cmd"`
	}
	type roleDef struct {
		Agents         []string `json:"agents"`
		Prompt         string   `json:"prompt"`
		ResultPath     string   `json:"result_path"`
		TimeoutSeconds int      `json:"timeout_seconds"`
	}
	type teamJSON struct {
		Agents           map[string]agentDef `json:"agents"`
		Roles            map[string]roleDef  `json:"roles"`
		Limits           map[string]int      `json:"limits"`
		RateLimitBackoff map[string]any      `json:"rate_limit_backoff"`
		FullTestCommand  string              `json:"full_test_command"`
	}

	team := teamJSON{
		Agents: map[string]agentDef{
			"parser_missing": {Cmd: []string{missing("parser-missing"), "{{PROMPT}}"}},
			"parser_ok":      {Cmd: []string{"sh", cliPath, "-p", "{{PROMPT}}", "--result", parserResult, "--kind", "parser"}},
			"coder_missing":  {Cmd: []string{missing("coder-missing"), "{{PROMPT}}"}},
			"tester_ok":      {Cmd: []string{"sh", "-c", "true # {{PROMPT}}"}},
			"critic_ok":      {Cmd: []string{"sh", "-c", "true # {{PROMPT}}"}},
			"reviewer_ok":    {Cmd: []string{"sh", "-c", "true # {{PROMPT}}"}},
		},
		Roles: map[string]roleDef{
			"parser":   {Agents: []string{"parser_missing", "parser_ok"}, Prompt: "prompts/parser.md", ResultPath: roleResult("parser"), TimeoutSeconds: 30},
			"coder":    {Agents: []string{"coder_missing"}, Prompt: "prompts/coder.md", ResultPath: roleResult("coder"), TimeoutSeconds: 30},
			"tester":   {Agents: []string{"tester_ok"}, Prompt: "prompts/tester.md", ResultPath: roleResult("tester"), TimeoutSeconds: 30},
			"critic":   {Agents: []string{"critic_ok"}, Prompt: "prompts/critic.md", ResultPath: roleResult("critic"), TimeoutSeconds: 30},
			"reviewer": {Agents: []string{"reviewer_ok"}, Prompt: "prompts/reviewer.md", ResultPath: roleResult("reviewer"), TimeoutSeconds: 30},
		},
		Limits: map[string]int{
			"max_review_cycles":  1,
			"max_fix_iterations": 1,
		},
		RateLimitBackoff: map[string]any{
			"initial_seconds": 1,
			"factor":          2,
			"max_seconds":     4,
			"default_pattern": "rate_?limit",
		},
		FullTestCommand: "true",
	}

	raw, err := json.MarshalIndent(team, "", "  ")
	if err != nil {
		t.Fatalf("marshal team.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "team.json"), raw, 0o644); err != nil {
		t.Fatalf("write team.json: %v", err)
	}
}
