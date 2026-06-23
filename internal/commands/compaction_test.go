package commands

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/agenthealth"
	"github.com/lionelchamorro/orquestalite/internal/config"
	"github.com/lionelchamorro/orquestalite/internal/eventlog"
	"github.com/lionelchamorro/orquestalite/internal/fallback"
	"github.com/lionelchamorro/orquestalite/internal/invoke"
	"github.com/lionelchamorro/orquestalite/internal/runner"
)

func TestCompactedMemory(t *testing.T) {
	cases := []struct {
		name   string
		before int
		raw    string
		want   string
		ok     bool
	}{
		{"shrinks", 100, "## Backend\n- pg", "## Backend\n- pg\n", true},
		{"trailing newlines normalized", 100, "x\n\n\n", "x\n", true},
		{"empty rejected", 100, "   \n", "", false},
		{"not smaller rejected", 5, "aaaaaaaa", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := compactedMemory(c.before, c.raw)
			if ok != c.ok || got != c.want {
				t.Fatalf("compactedMemory(%d, %q) = (%q, %v), want (%q, %v)", c.before, c.raw, got, ok, c.want, c.ok)
			}
		})
	}
}

// fakeCompactorRunner writes a fixed compactor result, standing in for a real
// agent so maybeCompactMemory can be exercised end-to-end.
type fakeCompactorRunner struct {
	result string
	calls  int
}

func (r *fakeCompactorRunner) Run(_ context.Context, spec runner.Spec) (*runner.Result, error) {
	r.calls++
	if err := os.MkdirAll(filepath.Dir(spec.ResultPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(spec.ResultPath, []byte(r.result), 0o644); err != nil {
		return nil, err
	}
	return &runner.Result{ResultExists: true, ExitCode: 0, Duration: time.Second}, nil
}

func compactionDeps(t *testing.T, fake *fakeCompactorRunner, thresholdChars int, withRole bool) (*liveDeps, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "memory-compactor.md"), []byte("compact {{MEMORY}} into {{RESULT_PATH}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	memPath := filepath.Join(dir, ".orquestalite", "memory.md")
	if err := os.MkdirAll(filepath.Dir(memPath), 0o755); err != nil {
		t.Fatal(err)
	}
	logger, err := eventlog.Open(filepath.Join(dir, ".orquestalite", "run.log"), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	specs := map[string]config.RoleSpec{}
	if withRole {
		specs["compactor"] = config.RoleSpec{
			Agents:     []config.AgentSpec{{Name: "fake", Cmd: []string{"fake"}}},
			PromptPath: "prompts/memory-compactor.md",
			ResultPath: ".orquestalite/results/compactor.json",
			Timeout:    time.Minute,
		}
	}
	inv := &invoke.RoleInvoker{
		Specs:    specs,
		Dir:      dir,
		Fallback: fallback.NewCaller(fallback.Config{}),
		Log:      logger,
		Health:   agenthealth.New(2),
		MemPath:  memPath,
		Runner:   fake,
	}
	cfg := &config.Config{}
	cfg.Limits.MemoryCompactChars = thresholdChars
	return &liveDeps{cfg: cfg, dir: dir, log: logger, inv: inv, memPath: memPath}, memPath
}

func TestMaybeCompactMemory_RewritesWhenOverThreshold(t *testing.T) {
	fake := &fakeCompactorRunner{result: `{"memory":"## Backend\n- uses postgres","kept_notes":1}`}
	d, memPath := compactionDeps(t, fake, 50, true)
	big := strings.Repeat("## [old note]\nverbose per-task chatter that is no longer needed\n\n", 40)
	if err := os.WriteFile(memPath, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}

	d.maybeCompactMemory(context.Background())

	got, err := os.ReadFile(memPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "## Backend\n- uses postgres\n" {
		t.Fatalf("memory not compacted, got %q", got)
	}
	if fake.calls != 1 {
		t.Fatalf("compactor calls = %d, want 1", fake.calls)
	}
}

func TestMaybeCompactMemory_NoopUnderThreshold(t *testing.T) {
	fake := &fakeCompactorRunner{result: `{"memory":"x","kept_notes":0}`}
	d, memPath := compactionDeps(t, fake, 100000, true) // huge threshold
	if err := os.WriteFile(memPath, []byte("small memory\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d.maybeCompactMemory(context.Background())

	if fake.calls != 0 {
		t.Fatalf("compactor should not run under threshold, calls=%d", fake.calls)
	}
	got, _ := os.ReadFile(memPath)
	if string(got) != "small memory\n" {
		t.Fatalf("memory should be unchanged, got %q", got)
	}
}

func TestMaybeCompactMemory_NoopWhenRoleAbsent(t *testing.T) {
	fake := &fakeCompactorRunner{result: `{"memory":"x"}`}
	d, memPath := compactionDeps(t, fake, 10, false) // tiny threshold but no role
	if err := os.WriteFile(memPath, []byte(strings.Repeat("z", 500)), 0o644); err != nil {
		t.Fatal(err)
	}

	d.maybeCompactMemory(context.Background())

	if fake.calls != 0 {
		t.Fatalf("no compactor role configured: must not run, calls=%d", fake.calls)
	}
}
