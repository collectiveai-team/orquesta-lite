package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gatedFlow runs its lint gate from project config instead of a literal argv.
const gatedFlow = `{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"gated","version":"1"},"steps":[{"id":"lint","uses":"activity:gate.run@1","with":{"argv":{"$ref":"config.lint_argv"}}}]}`

func writeTeamJSON(t *testing.T, dir string, fields map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "team.json")
	if err = os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func installGatedPack(t *testing.T, dir string) {
	t.Helper()
	installFixturePack(t, dir, "gates", "1", map[string]string{"flows/gated@1.json": gatedFlow})
}

func TestLoadGateConfigExposesOnlyTheWhitelist(t *testing.T) {
	dir := t.TempDir()
	path := writeTeamJSON(t, dir, map[string]any{
		"lint_argv":    []string{"true"},
		"test_argv":    []string{"true"},
		"lint_command": "uv run ruff check .",
		"agents":       map[string]any{"secret": "value"},
	})
	config := loadGateConfig(path)
	if len(config) != 2 {
		t.Fatalf("config=%v", config)
	}
	for _, hidden := range []string{"lint_command", "agents"} {
		if _, ok := config[hidden]; ok {
			t.Errorf("%s must not be reachable from a flow", hidden)
		}
	}
}

func TestLoadGateConfigToleratesAMissingTeamFile(t *testing.T) {
	if config := loadGateConfig(filepath.Join(t.TempDir(), "absent.json")); len(config) != 0 {
		t.Fatalf("config=%v", config)
	}
}

// A config.* reference that would explode mid-run is rejected at startup. An
// empty argv counts as a failure on purpose: a gate that silently runs nothing
// is worse than one that fails loudly.
func TestFlowRunFailsFastOnBadGateConfig(t *testing.T) {
	cases := map[string]struct {
		team map[string]any
		want string
	}{
		"missing key":  {map[string]any{"test_argv": []string{"true"}}, `does not declare "lint_argv"`},
		"string value": {map[string]any{"lint_argv": "uv run ruff check ."}, "must be an array of strings"},
		"empty array":  {map[string]any{"lint_argv": []string{}}, "empty array"},
		"non-strings":  {map[string]any{"lint_argv": []any{"ruff", 3}}, "must be a string"},
		"no team.json": {nil, `does not declare "lint_argv"`},
	}
	for name, tc := range cases {
		dir := t.TempDir()
		installGatedPack(t, dir)
		if tc.team != nil {
			writeTeamJSON(t, dir, tc.team)
		}
		var out bytes.Buffer
		err := FlowCLI(context.Background(), dir, []string{"run", "gates/gated@1"}, &out)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err=%v, want it to mention %q", name, err, tc.want)
			continue
		}
		// Nothing may have been started.
		if _, statErr := os.Stat(filepath.Join(dir, ".orquestalite", "workflows.db")); statErr == nil {
			t.Errorf("%s: a run was created despite invalid config", name)
		}
	}
}

// The payoff: the gate runs the project's configured command, not one baked
// into the pack's flow JSON.
func TestFlowRunExecutesTheConfiguredGateCommand(t *testing.T) {
	dir := t.TempDir()
	installGatedPack(t, dir)
	marker := filepath.Join(dir, "gate-ran")
	writeTeamJSON(t, dir, map[string]any{
		"lint_argv": []string{"/bin/sh", "-c", "echo ran > " + marker},
		"test_argv": []string{"/usr/bin/true"},
	})
	var out bytes.Buffer
	if err := FlowCLI(context.Background(), dir, []string{"run", "gates/gated@1"}, &out); err != nil {
		t.Fatalf("err=%v out=%s", err, out.String())
	}
	if !strings.Contains(out.String(), "status=succeeded") {
		t.Fatalf("out=%s", out.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the configured gate command did not run: %v", err)
	}
}

// A configured gate that fails must fail the run — the reference must not
// weaken the gate into advisory output.
func TestConfiguredGateFailureFailsTheRun(t *testing.T) {
	dir := t.TempDir()
	installGatedPack(t, dir)
	writeTeamJSON(t, dir, map[string]any{"lint_argv": []string{"/usr/bin/false"}})
	var out bytes.Buffer
	if err := FlowCLI(context.Background(), dir, []string{"run", "gates/gated@1"}, &out); err == nil {
		t.Fatalf("a failing gate must fail the run: out=%s", out.String())
	}
}
