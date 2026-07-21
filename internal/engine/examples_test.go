package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/config"
)

// TestExampleConfigsParse guards the reference configs under examples/: every
// bundled flows.json must parse into the engine's flow model and every
// team.json must load, validate, and resolve. Catches drift between the
// examples and the engine/config schemas.
func TestExampleConfigsParse(t *testing.T) {
	root := filepath.Join("..", "..", "examples")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read examples dir: %v", err)
	}
	checked := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())

		if flowsPath := filepath.Join(dir, "flows.json"); fileExists(flowsPath) {
			flows, err := LoadFlows(flowsPath)
			if err != nil {
				t.Errorf("%s: %v", flowsPath, err)
			} else if len(flows.Flows) == 0 {
				t.Errorf("%s: declares no flows", flowsPath)
			}
			checked++
		}

		if teamPath := filepath.Join(dir, "team.json"); fileExists(teamPath) {
			cfg, err := config.Load(teamPath)
			if err != nil {
				t.Errorf("%s: %v", teamPath, err)
			} else if _, err := cfg.ResolveAll(); err != nil {
				// ResolveAll validates all declared roles without enforcing the
				// legacy parser/coder/tester/critic/reviewer set, so v2-pack-only
				// team.json files (e.g. governed-pack) resolve correctly.
				t.Errorf("%s: resolve: %v", teamPath, err)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no example configs found — wrong examples path?")
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// TestRalphLoopFlowConverges drives examples/ralph-loop/flows.json end to end
// with scripted agents: round 1 the coder takes two tasks then reports the plan
// exhausted, the adversarial reviewer files a finding; round 2 the coder fixes
// it and the reviewer approves. Exercises the nested retry_until convergence,
// the printf `review_round` capture, and the feedback threading into prompts.
func TestRalphLoopFlowConverges(t *testing.T) {
	flows, err := LoadFlows(filepath.Join("..", "..", "examples", "ralph-loop", "flows.json"))
	if err != nil {
		t.Fatalf("load flows: %v", err)
	}
	flow, ok := flows.Flows["ralph_loop"]
	if !ok {
		t.Fatal("ralph_loop flow not found")
	}

	agent := &recordingAgent{t: t, scripts: map[string][]map[string]any{
		"ralph_coder": {
			{"status": "in_progress", "task": "greet pkg", "summary": "implemented greet"},
			{"status": "in_progress", "task": "greet CLI", "summary": "implemented cli"},
			{"status": "completed", "task": "", "summary": "plan exhausted"},
			{"status": "in_progress", "task": "reviewer finding", "summary": "fixed empty-name edge"},
			{"status": "completed", "task": "", "summary": "plan exhausted again"},
		},
		"ralph_reviewer": {
			{"status": "changes_requested", "summary": "empty name panics Greet", "tasks_added": 1},
			{"status": "approved", "summary": "all findings addressed", "tasks_added": 0},
		},
	}}
	command := &recordingCommand{t: t, scripts: []cmdScript{
		{match: "'%s' '1'", stdout: "1"},
		{match: "'%s' '2'", stdout: "2"},
		{match: "go test", stdout: "ok", exit: 0},
	}}

	if err := New(&flow, agent, command).Run(context.Background(), nil); err != nil {
		t.Fatalf("run: %v", err)
	}

	var coder, reviewer []agentCall
	for _, c := range agent.calls {
		switch c.role {
		case "ralph_coder":
			coder = append(coder, c)
		case "ralph_reviewer":
			reviewer = append(reviewer, c)
		}
	}
	if len(coder) != 5 || len(reviewer) != 2 {
		t.Fatalf("got %d coder / %d reviewer calls, want 5 / 2", len(coder), len(reviewer))
	}

	// First pass starts blank: no prior summary, no test output, no feedback.
	first := coder[0].vars
	for _, k := range []string{"PREVIOUS_SUMMARY", "TEST_OUTPUT", "REVIEWER_FEEDBACK"} {
		if first[k] != "" {
			t.Errorf("first coder pass: %s = %q, want empty", k, first[k])
		}
	}
	// retry_until threads the previous attempt into the next one.
	if got := coder[1].vars["PREVIOUS_SUMMARY"]; got != "implemented greet" {
		t.Errorf("second pass PREVIOUS_SUMMARY = %q", got)
	}
	if got := coder[1].vars["TEST_OUTPUT"]; got != "ok" {
		t.Errorf("second pass TEST_OUTPUT = %q", got)
	}
	// Round 2 opens with the reviewer's feedback and the captured round number.
	round2 := coder[3].vars
	if got := round2["REVIEWER_FEEDBACK"]; got != "empty name panics Greet" {
		t.Errorf("round-2 coder REVIEWER_FEEDBACK = %q", got)
	}
	if got := round2["REVIEW_ROUND"]; got != "2" {
		t.Errorf("round-2 coder REVIEW_ROUND = %q", got)
	}
	if got := reviewer[0].vars["ROUND"]; got != "1" {
		t.Errorf("reviewer round 1 ROUND = %q", got)
	}
	if got := reviewer[1].vars["ROUND"]; got != "2" {
		t.Errorf("reviewer round 2 ROUND = %q", got)
	}
	if got := reviewer[1].vars["CODER_SUMMARY"]; got != "plan exhausted again" {
		t.Errorf("reviewer round 2 CODER_SUMMARY = %q", got)
	}
}
