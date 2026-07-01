package engine

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

func osWrite(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}

// recordingAgent is a mock AgentRunner that returns scripted results keyed by
// role and (optionally) TASK_ID. It records every invocation for assertions.
type recordingAgent struct {
	t        *testing.T
	scripts  map[string][]map[string]any // role -> sequence of results
	resolved map[string]map[string]any   // (role|role:task) -> single result
	calls    []agentCall
}

type agentCall struct {
	role string
	vars map[string]string
	rc   RunContext
}

func (r *recordingAgent) RunRole(_ context.Context, role string, vars map[string]string, rc RunContext) (map[string]any, error) {
	key := role
	if rc.TaskID != "" {
		if _, ok := r.resolved[role+":"+rc.TaskID]; ok {
			key = role + ":" + rc.TaskID
		}
	}
	if seq, ok := r.scripts[key]; ok && len(seq) > 0 {
		res := seq[0]
		r.scripts[key] = seq[1:]
		r.calls = append(r.calls, agentCall{role, vars, rc})
		return res, nil
	}
	if res, ok := r.resolved[key]; ok {
		r.calls = append(r.calls, agentCall{role, vars, rc})
		return res, nil
	}
	r.t.Fatalf("recordingAgent: unexpected role %q (task %s)", role, rc.TaskID)
	return nil, fmt.Errorf("unexpected")
}

// recordingCommand is a mock CommandRunner. It matches command substrings to
// scripted outcomes (stdout/exitCode). Unmatched commands fail the test.
type recordingCommand struct {
	t           *testing.T
	scripts     []cmdScript
	calls       []string
	continueAll bool
}

type cmdScript struct {
	match  string
	stdout string
	exit   int
	fail   bool
}

func (r *recordingCommand) Run(_ context.Context, dir, command string) (string, int, error) {
	r.calls = append(r.calls, command)
	for _, s := range r.scripts {
		if strings.Contains(command, s.match) {
			if s.fail && !r.continueAll {
				return s.stdout, s.exit, fmt.Errorf("exit %d", s.exit)
			}
			return s.stdout, s.exit, nil
		}
	}
	r.t.Fatalf("recordingCommand: unexpected command %q", command)
	return "", 1, fmt.Errorf("unexpected")
}

func callsContaining(calls []string, substr string) []string {
	var out []string
	for _, c := range calls {
		if strings.Contains(c, substr) {
			out = append(out, c)
		}
	}
	return out
}

// ---- Unit: Context + interpolation ----

func TestContextNestedGetSet(t *testing.T) {
	c := NewContext()
	c.Set("feature.branch_name", "feat-x")
	got, ok := c.Get("feature.branch_name")
	if !ok || got != "feat-x" {
		t.Fatalf("got %v ok=%v", got, ok)
	}
	if _, ok := c.Get("feature.missing"); ok {
		t.Fatal("unknown key should miss")
	}
}

func TestInterpolation_NestedAndLiteral(t *testing.T) {
	c := NewContext()
	c.Set("inputs.base_branch", "main")
	c.Set("feature.branch_name", "feat-x")
	c.Set("coder_res.files_changed", []any{"a.go", "b.go"})
	out := c.Interpolate("git checkout -b {feature.branch_name} {inputs.base_branch} && echo $HOME {unknown}")
	want := "git checkout -b feat-x main && echo $HOME {unknown}"
	if out != want {
		t.Fatalf("got %q want %q", out, want)
	}
	out2 := c.Interpolate("FILES={coder_res.files_changed}")
	if out2 != "FILES=a.go, b.go" {
		t.Fatalf("got %q", out2)
	}
}

// ---- Unit: eval ----

func TestEvalExpr(t *testing.T) {
	c := NewContext()
	c.Set("task_verified", true)
	c.Set("lint_res.pass", true)
	c.Set("tester_res.pass", false)
	e := New(&Flow{}, &recordingAgent{t: t}, &recordingCommand{t: t})
	c2 := c
	cases := []struct {
		expr string
		want bool
	}{
		{"{task_verified} == true", true},
		{"true && false", false},
		{"{lint_res.pass} && {tester_res.pass}", false},
		{"{lint_res.pass} || {tester_res.pass}", true},
		{"!{tester_res.pass}", true},
		{"(true || false) && !false", true},
		{"\"ok\" == \"ok\"", true},
	}
	for _, tc := range cases {
		got, err := e.evalExpr(c2, tc.expr)
		if err != nil {
			t.Fatalf("expr %q: %v", tc.expr, err)
		}
		if got != tc.want {
			t.Fatalf("expr %q: got %v want %v", tc.expr, got, tc.want)
		}
	}
}

// ---- Integration: factory_fast reproduces RunFastBatch behaviour ----
//
// The flow parses a features.md, loops over features, runs parser -> coder ->
// lint -> tester -> critic -> commit -> reviewer, and creates a PR per feature.
// All agents/commands are mocked; we assert the call ordering and that the
// final commit+PR happened.

func fastBatchFlow() *Flow {
	return &Flow{
		Inputs: map[string]InputSpec{
			"features_path": {Default: "features.md"},
			"base_branch":   {Default: "main"},
		},
		Steps: []Step{
			{Type: "action", Action: "factory_extract_features", Inputs: map[string]any{"path": "{inputs.features_path}"}, Outputs: map[string]string{"features_queue": "."}},
			{Type: "loop", Iterator: "{features_queue}", As: "feature", Body: []Step{
				{Type: "command", Command: "git checkout -b {feature.branch_name} {inputs.base_branch}"},
				{Type: "agent", Agent: "parser", Inputs: map[string]any{"PLAN": "{feature.plan}"}, Outputs: map[string]string{"tasks": "tasks"}},
				{Type: "action", Action: "format_fast_batch_prompt", Inputs: map[string]any{"tasks": "{tasks}"}, Outputs: map[string]string{"batch_description": "batch_description"}},
				{Type: "agent", Agent: "coder", Inputs: map[string]any{"TASK_ID": "_fast", "DESCRIPTION": "{batch_description}"}, Outputs: map[string]string{"coder_res": "."}},
				{Type: "command", Command: "go vet ./...", OnFailure: "continue"},
				{Type: "agent", Agent: "tester", Inputs: map[string]any{"FILES": "{coder_res.files_changed}"}},
				{Type: "agent", Agent: "critic", Inputs: map[string]any{"FILES": "{coder_res.files_changed}"}},
				{Type: "command", Command: "go test ./..."},
				{Type: "command", Command: "git commit -m 'feat({feature.id}): {feature.title} batch'"},
				{Type: "agent", Agent: "reviewer", Inputs: map[string]any{"FEATURE": "{feature.description}"}},
				{Type: "command", Command: "gh pr create --base {inputs.base_branch} --head {feature.branch_name}"},
			}},
		},
	}
}

func TestFactoryFastFlow_ReproducesRunFastBatch(t *testing.T) {
	ctx := t.TempDir()
	writeFile(t, ctx+"/features.md", "## Login\nplan A\n## Export\nplan B\n")

	agent := &recordingAgent{
		t:       t,
		scripts: map[string][]map[string]any{},
		resolved: map[string]map[string]any{
			"parser":   {"tasks": []any{map[string]any{"title": "T1", "description": "D1"}}},
			"coder":    {"status": "completed", "files_changed": []any{"a.go"}},
			"tester":   {"status": "pass"},
			"critic":   {"status": "approved"},
			"reviewer": {"summary_of_cycle": "ok", "should_stop": true},
		},
	}
	cmd := &recordingCommand{
		t: t,
		scripts: []cmdScript{
			{match: "git checkout -b"},
			{match: "go vet", fail: true, exit: 1},
			{match: "go test"},
			{match: "git commit"},
			{match: "gh pr create", stdout: "https://pr/1"},
		},
		continueAll: true,
	}

	e := New(fastBatchFlow(), agent, cmd, WithDir(ctx))
	if err := e.Run(context.Background(), map[string]any{"features_path": ctx + "/features.md"}); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(agent.calls) == 0 {
		t.Fatal("no agent calls recorded")
	}
	// Two features -> parser/coder/tester/critic/reviewer called twice each.
	roles := map[string]int{}
	for _, c := range agent.calls {
		roles[c.role]++
	}
	for _, role := range []string{"parser", "coder", "tester", "critic", "reviewer"} {
		if roles[role] != 2 {
			t.Fatalf("role %s called %d times, want 2", role, roles[role])
		}
	}
	// One PR created per feature.
	prs := callsContaining(cmd.calls, "gh pr create")
	if len(prs) != 2 {
		t.Fatalf("want 2 pr create calls, got %d", len(prs))
	}
	// Commits are present and mention the feature id (F001/F002).
	commits := callsContaining(cmd.calls, "git commit")
	if len(commits) != 2 {
		t.Fatalf("want 2 commits, got %d", len(commits))
	}
	for _, c := range commits {
		if !strings.Contains(c, "feat(") {
			t.Fatalf("commit missing feature id: %q", c)
		}
	}
	// go vet failure must NOT abort the flow (OnFailure: continue).
	if len(callsContaining(cmd.calls, "go vet")) != 2 {
		t.Fatalf("go vet should run once per feature")
	}
}

// ---- Integration: factory flow (per-task fix loop via retry_until) ----
//
// For one feature with one task, retry_until re-runs coder -> lint -> tester
// -> critic -> eval until verified. We script tester to fail once then pass,
// proving the retry loop converges exactly like loops.RunFix.

func factoryFlow(maxRetries int) *Flow {
	return &Flow{
		Inputs: map[string]InputSpec{
			"features_path": {Default: "features.md"},
			"base_branch":   {Default: "main"},
		},
		Steps: []Step{
			{Type: "action", Action: "factory_extract_features", Inputs: map[string]any{"path": "{inputs.features_path}"}, Outputs: map[string]string{"features_queue": "."}},
			{Type: "loop", Iterator: "{features_queue}", As: "feature", Body: []Step{
				{Type: "command", Command: "git checkout -b {feature.branch_name} {inputs.base_branch}"},
				{Type: "agent", Agent: "parser", Inputs: map[string]any{"PLAN": "{feature.plan}"}, Outputs: map[string]string{"tasks": "tasks"}},
				{Type: "loop", Iterator: "{tasks}", As: "task", Body: []Step{
					{Type: "retry_until", Condition: "{task_verified} == true", MaxRetries: maxRetries, Body: []Step{
						{Type: "agent", Agent: "coder", Inputs: map[string]any{"TASK_ID": "{task.id}", "DESCRIPTION": "{task.description}"}, Outputs: map[string]string{"coder_res": "."}},
						{Type: "command", Command: "go vet ./...", OnFailure: "continue", Outputs: map[string]string{"lint_res": "."}},
						{Type: "agent", Agent: "tester", Inputs: map[string]any{"FILES": "{coder_res.files_changed}"}, Outputs: map[string]string{"tester_res": "."}},
						{Type: "agent", Agent: "critic", Inputs: map[string]any{"FILES": "{coder_res.files_changed}"}, Outputs: map[string]string{"critic_res": "."}},
						{Type: "eval", Expression: "{lint_res.pass} && {tester_res.pass} && {critic_res.pass}", Outputs: map[string]string{"task_verified": "."}},
					}},
					{Type: "command", Command: "go test ./..."},
					{Type: "command", Command: "git commit -m 'feat({task.id}): {task.title}'"},
				}},
				{Type: "command", Command: "gh pr create --base {inputs.base_branch} --head {feature.branch_name}"},
			}},
		},
	}
}

func TestFactoryFlow_RetryUntilConverges(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir+"/features.md", "## Feature A\nplan A\n")

	// tester fails on the first script entry, then passes — forcing one retry.
	testerSeq := []map[string]any{
		{"status": "fail", "command_run": "go test ./...", "failures": []any{map[string]any{"test": "T1", "message": "boom"}}},
		{"status": "pass", "command_run": "go test ./...", "failures": []any{}},
	}
	agent := &recordingAgent{
		t:       t,
		scripts: map[string][]map[string]any{"tester": testerSeq},
		resolved: map[string]map[string]any{
			"parser": {"tasks": []any{map[string]any{"id": "T001", "title": "T1", "description": "D1"}}},
			"coder":  {"status": "completed", "files_changed": []any{"a.go"}},
			"critic": {"status": "approved"},
		},
	}
	cmd := &recordingCommand{
		t: t,
		scripts: []cmdScript{
			{match: "git checkout -b"},
			{match: "go vet"},
			{match: "go test"},
			{match: "git commit"},
			{match: "gh pr create", stdout: "https://pr/1"},
		},
	}

	e := New(factoryFlow(5), agent, cmd, WithDir(dir))
	if err := e.Run(context.Background(), map[string]any{"features_path": dir + "/features.md"}); err != nil {
		t.Fatalf("run: %v", err)
	}

	// coder must have run twice (one per retry_until iteration).
	coderCount := 0
	testerCount := 0
	criticCount := 0
	for _, c := range agent.calls {
		switch c.role {
		case "coder":
			coderCount++
		case "tester":
			testerCount++
		case "critic":
			criticCount++
		}
	}
	if coderCount != 2 || testerCount != 2 || criticCount != 2 {
		t.Fatalf("retry counts: coder=%d tester=%d critic=%d (want 2 each)", coderCount, testerCount, criticCount)
	}
	// Single commit + single PR per feature.
	if len(callsContaining(cmd.calls, "git commit")) != 1 {
		t.Fatalf("want 1 commit, got %d", len(callsContaining(cmd.calls, "git commit")))
	}
	if len(callsContaining(cmd.calls, "gh pr create")) != 1 {
		t.Fatalf("want 1 pr, got %d", len(callsContaining(cmd.calls, "gh pr create")))
	}
}

func TestFactoryFlow_RetryExhaustion(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir+"/features.md", "## Feature A\nplan A\n")

	agent := &recordingAgent{
		t: t,
		resolved: map[string]map[string]any{
			"parser": {"tasks": []any{map[string]any{"id": "T001", "title": "T1", "description": "D1"}}},
			"coder":  {"status": "completed", "files_changed": []any{"a.go"}},
			"tester": {"status": "fail", "command_run": "go test", "failures": []any{map[string]any{"test": "T1"}}},
			"critic": {"status": "approved"},
		},
	}
	cmd := &recordingCommand{t: t, scripts: []cmdScript{
		{match: "git checkout -b"},
		{match: "go vet"},
	}}

	e := New(factoryFlow(2), agent, cmd, WithDir(dir))
	err := e.Run(context.Background(), map[string]any{"features_path": dir + "/features.md"})
	if err == nil {
		t.Fatal("expected error on retry exhaustion")
	}
	if !strings.Contains(err.Error(), "not satisfied after 2") {
		t.Fatalf("unexpected error: %v", err)
	}
	testerCount := 0
	for _, c := range agent.calls {
		if c.role == "tester" {
			testerCount++
		}
	}
	if testerCount != 2 {
		t.Fatalf("tester ran %d times, want 2 (capped by max_retries)", testerCount)
	}
}

// ---- helpers ----

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := osWrite(path, body); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
