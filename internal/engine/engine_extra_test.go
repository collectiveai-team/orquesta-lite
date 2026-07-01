package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// ---- Unit: argv shell quoting ----

func TestResolveCommand_ArgvQuotesUnsafeValues(t *testing.T) {
	c := NewContext()
	c.Set("feature.title", "Add login; rm -rf /") // injection attempt
	c.Set("feature.branch", "feat/login")
	e := New(&Flow{}, &recordingAgent{t: t}, &recordingCommand{t: t})

	got, err := e.resolveCommand(c, Step{
		Args: []string{"git", "commit", "-m", "feat: {feature.title}", "--branch", "{feature.branch}"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `'git' 'commit' '-m' 'feat: Add login; rm -rf /' '--branch' 'feat/login'`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// A value containing a single quote must not break out of its quoting.
	c.Set("msg", "it's a test")
	got2, _ := e.resolveCommand(c, Step{Args: []string{"echo", "{msg}"}})
	if got2 != `'echo' 'it'\''s a test'` {
		t.Fatalf("single-quote escaping wrong: %s", got2)
	}
}

func TestResolveCommand_RejectsBothOrNeither(t *testing.T) {
	e := New(&Flow{}, &recordingAgent{t: t}, &recordingCommand{t: t})
	if _, err := e.resolveCommand(NewContext(), Step{Command: "ls", Args: []string{"ls"}}); err == nil {
		t.Fatal("want error when both command and args set")
	}
	if _, err := e.resolveCommand(NewContext(), Step{}); err == nil {
		t.Fatal("want error when neither command nor args set")
	}
}

// ---- Unit: blank interpolation for agent inputs ----

func TestInterpolateBlank_DropsUnresolved(t *testing.T) {
	c := NewContext()
	c.Set("known", "X")
	if got := c.InterpolateBlank("a={known} b={missing}"); got != "a=X b=" {
		t.Fatalf("got %q", got)
	}
	// Interpolate (non-blank) must still preserve unresolved tokens.
	if got := c.Interpolate("b={missing}"); got != "b={missing}" {
		t.Fatalf("Interpolate should preserve, got %q", got)
	}
}

// ---- Integration: retry threads attempt counter + feedback into the coder ----

func TestRetry_ThreadsAttemptAndFeedback(t *testing.T) {
	// coder records the FEEDBACK var it receives each attempt; tester fails once
	// then passes so the retry runs twice.
	flow := &Flow{Steps: []Step{
		{Type: "retry_until", Condition: "{ok} == true", MaxRetries: 5, Body: []Step{
			{Type: "agent", Agent: "coder", Inputs: map[string]any{
				"ATTEMPT": "{_attempt}", "FEEDBACK": "{tester_res.reason}",
			}, Outputs: map[string]string{"coder_res": "."}},
			{Type: "agent", Agent: "tester", Outputs: map[string]string{"tester_res": "."}},
			{Type: "eval", Expression: "{tester_res.pass}", Outputs: map[string]string{"ok": "."}},
		}},
	}}
	agent := &recordingAgent{
		t:       t,
		scripts: map[string][]map[string]any{"tester": {{"status": "fail", "reason": "boom"}, {"status": "pass"}}},
		resolved: map[string]map[string]any{
			"coder": {"status": "completed"},
		},
	}
	e := New(flow, agent, &recordingCommand{t: t})
	if err := e.Run(context.Background(), nil); err != nil {
		t.Fatalf("run: %v", err)
	}

	var coderCalls []agentCall
	for _, c := range agent.calls {
		if c.role == "coder" {
			coderCalls = append(coderCalls, c)
		}
	}
	if len(coderCalls) != 2 {
		t.Fatalf("coder ran %d times, want 2", len(coderCalls))
	}
	// First attempt: counter=1, no feedback yet (blanked).
	if coderCalls[0].vars["ATTEMPT"] != "1" || coderCalls[0].vars["FEEDBACK"] != "" {
		t.Fatalf("attempt 1 vars = %+v", coderCalls[0].vars)
	}
	if coderCalls[0].rc.Attempt != 1 {
		t.Fatalf("attempt 1 rc.Attempt = %d", coderCalls[0].rc.Attempt)
	}
	// Second attempt: counter=2, feedback from the failed tester is threaded in.
	if coderCalls[1].vars["ATTEMPT"] != "2" || coderCalls[1].vars["FEEDBACK"] != "boom" {
		t.Fatalf("attempt 2 vars = %+v", coderCalls[1].vars)
	}
	if coderCalls[1].rc.Attempt != 2 {
		t.Fatalf("attempt 2 rc.Attempt = %d", coderCalls[1].rc.Attempt)
	}
}

// ---- Integration: the SHIPPED flows.json factory_fast runs end-to-end ----
//
// Unlike the hand-built flows in engine_test.go, this loads the real config so
// the test fails if flows.json drifts (e.g. a renamed agent or a malformed
// argv step). All agents/commands are mocked.

func TestShippedFlowsJSON_FactoryFast(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir+"/features.md", "## Greeting API\nExpose GET /hello returning JSON.\n")

	flows, err := LoadFlows(filepath.Join("..", "..", "flows.json"))
	if err != nil {
		t.Fatalf("load flows.json: %v", err)
	}
	flow := flows.Flows["factory_fast"]

	agent := &recordingAgent{
		t:       t,
		scripts: map[string][]map[string]any{},
		resolved: map[string]map[string]any{
			"parser":   {"tasks": []any{map[string]any{"id": "T1", "title": "T1", "description": "D1"}}},
			"coder":    {"status": "completed", "files_changed": []any{"main.go"}},
			"tester":   {"status": "pass"},
			"critic":   {"status": "approved"},
			"reviewer": {"summary_of_cycle": "ok", "should_stop": true},
			"verifier": {"status": "pass"},
		},
	}
	// Match on single tokens because argv steps render as quoted words
	// ('git' 'commit' ...), so multi-word substrings won't match.
	cmd := &recordingCommand{
		t:           t,
		continueAll: true,
		scripts: []cmdScript{
			{match: "checkout"},
			{match: "go vet", fail: true, exit: 1},
			{match: "go test"},
			{match: "add"},
			{match: "commit"},
			{match: "push", fail: true, exit: 1}, // simulate unauthed remote
			{match: "create", fail: true, exit: 1},
		},
	}

	e := New(&flow, agent, cmd, WithDir(dir))
	if err := e.Run(context.Background(), map[string]any{"features_path": dir + "/features.md"}); err != nil {
		t.Fatalf("run shipped factory_fast: %v", err)
	}

	// verifier runs once globally at the end.
	if n := countRole(agent.calls, "verifier"); n != 1 {
		t.Fatalf("verifier called %d times, want 1", n)
	}
	// commit happened and the argv message is shell-quoted as one word.
	commits := callsContaining(cmd.calls, "commit")
	if len(commits) != 1 || !strings.Contains(commits[0], "'feat(F001): Greeting API batch implementation'") {
		t.Fatalf("commit not quoted as one arg: %v", commits)
	}
	// push + PR were attempted but tolerated (on_failure: continue) — the flow
	// still completed and reached the verifier above.
	if len(callsContaining(cmd.calls, "push")) != 1 {
		t.Fatalf("expected one push attempt, got %v", cmd.calls)
	}
}

func countRole(calls []agentCall, role string) int {
	n := 0
	for _, c := range calls {
		if c.role == role {
			n++
		}
	}
	return n
}
