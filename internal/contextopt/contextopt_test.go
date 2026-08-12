package contextopt

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/config"
)

// fakeBinary writes an executable that exits 0 for --version, standing in for
// the real filter binary.
func fakeBinary(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func off() *bool { b := false; return &b }

func TestOmittedBlockEnablesBoth(t *testing.T) {
	var co config.ContextOptimization
	if !co.ProxyEnabled() || !co.FilterEnabled() {
		t.Fatal("an omitted block must enable both tools")
	}
	if co.ProxyURL() != config.DefaultProxyURL || co.FilterBinary() != config.DefaultFilterBinary {
		t.Fatalf("defaults not applied: %s %s", co.ProxyURL(), co.FilterBinary())
	}
}

// Disabling must be inert: no env, and nothing written into the project.
func TestDisabledWritesNothingAndReturnsNoEnv(t *testing.T) {
	dir := t.TempDir()
	co := config.ContextOptimization{
		CompressionProxy: config.CompressionProxy{Enabled: off()},
		CommandFilter:    config.CommandFilter{Enabled: off()},
	}
	st := Activate(co, dir)
	if st.Env() != nil {
		t.Fatal("disabled tools must yield a nil env so the subprocess inherits as before")
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude")); !os.IsNotExist(err) {
		t.Fatal("disabled command filter must not create .claude/")
	}
	if st.ProxyReachable || st.FilterVerified {
		t.Fatal("disabled tools must not report as active")
	}
}

// A missing binary is the common case on a fresh machine: warn, do not fail.
func TestMissingBinaryFailsOpen(t *testing.T) {
	dir := t.TempDir()
	co := config.ContextOptimization{
		CompressionProxy: config.CompressionProxy{Enabled: off()},
		CommandFilter:    config.CommandFilter{Binary: filepath.Join(dir, "does-not-exist")},
	}
	st := Activate(co, dir)
	if st.FilterVerified {
		t.Fatal("a missing binary must not verify")
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatal("no hook may be written when the binary is missing")
	}
	if !strings.Contains(strings.Join(st.Notes, " "), "not found") {
		t.Fatalf("the reason must be reported, got %v", st.Notes)
	}
}

// The regression that silently invalidated a measurement round: a binary that
// cannot run. It must be caught before the hook is installed, otherwise every
// rewritten shell command dies with exit 127.
func TestNonExecutableBinaryIsRejectedBeforeInstallingHook(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "rtk")
	if err := os.WriteFile(bad, []byte("not executable"), 0o644); err != nil {
		t.Fatal(err)
	}
	co := config.ContextOptimization{
		CompressionProxy: config.CompressionProxy{Enabled: off()},
		CommandFilter:    config.CommandFilter{Binary: bad},
	}
	st := Activate(co, dir)
	if st.FilterVerified {
		t.Fatal("a non-executable binary must not verify")
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatal("hook must not be installed when the binary cannot run")
	}
}

func TestWorkingBinaryInstallsHookAndPutsDirOnPath(t *testing.T) {
	dir := t.TempDir()
	bin := fakeBinary(t, dir, "rtk")
	co := config.ContextOptimization{
		CompressionProxy: config.CompressionProxy{Enabled: off()},
		CommandFilter:    config.CommandFilter{Binary: bin},
	}
	st := Activate(co, dir)
	if !st.FilterVerified {
		t.Fatalf("expected verification to pass, notes: %v", st.Notes)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "hook claude") || !strings.Contains(string(raw), hookMarker) {
		t.Fatalf("hook not written as expected: %s", raw)
	}
	// The hook rewrites to a bare name, so the directory must reach the subprocess.
	var found bool
	for _, kv := range st.Env() {
		if strings.HasPrefix(kv, "PATH=") && strings.Contains(kv, filepath.Dir(bin)) {
			found = true
		}
	}
	if !found {
		t.Fatal("the binary's directory must be prepended to PATH")
	}
}

// A project may already own .claude/settings.json. Clobbering someone's hooks
// to enable an optimization is a worse trade than skipping the optimization.
func TestExistingProjectHooksArePreserved(t *testing.T) {
	dir := t.TempDir()
	bin := fakeBinary(t, dir, "rtk")
	claude := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claude, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{
  "permissions": {"allow": ["Bash(ls:*)"]},
  "hooks": {"PreToolUse": [{"matcher": "*", "hooks": [{"type": "command", "command": "keep-me"}]}]}
}`
	if err := os.WriteFile(filepath.Join(claude, "settings.json"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	co := config.ContextOptimization{
		CompressionProxy: config.CompressionProxy{Enabled: off()},
		CommandFilter:    config.CommandFilter{Binary: bin},
	}
	if st := Activate(co, dir); !st.FilterVerified {
		t.Fatalf("expected activation, notes: %v", st.Notes)
	}

	raw, err := os.ReadFile(filepath.Join(claude, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("settings.json is no longer valid JSON: %v", err)
	}
	if _, ok := got["permissions"]; !ok {
		t.Error("unrelated keys must survive")
	}
	entries := got["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(entries) != 2 {
		t.Fatalf("expected the pre-existing hook plus ours, got %d", len(entries))
	}
	if !strings.Contains(string(raw), "keep-me") {
		t.Error("the pre-existing hook command must be preserved")
	}
}

// Activating twice must not stack duplicate hooks.
func TestActivateIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	bin := fakeBinary(t, dir, "rtk")
	co := config.ContextOptimization{
		CompressionProxy: config.CompressionProxy{Enabled: off()},
		CommandFilter:    config.CommandFilter{Binary: bin},
	}
	Activate(co, dir)
	Activate(co, dir)
	raw, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if n := strings.Count(string(raw), hookMarker); n != 1 {
		t.Fatalf("expected exactly one of our hooks, found %d", n)
	}
}

func TestUnreachableProxyFailsOpen(t *testing.T) {
	co := config.ContextOptimization{
		// port 1 is reserved and never listening
		CompressionProxy: config.CompressionProxy{URL: "http://127.0.0.1:1"},
		CommandFilter:    config.CommandFilter{Enabled: off()},
	}
	st := Activate(co, t.TempDir())
	if st.ProxyReachable {
		t.Fatal("port 1 must not be reachable")
	}
	for _, kv := range st.Env() {
		if strings.HasPrefix(kv, "ANTHROPIC_BASE_URL=") {
			t.Fatal("an unreachable proxy must not be injected")
		}
	}
	if !strings.Contains(strings.Join(st.Notes, " "), "not reachable") {
		t.Fatalf("the reason must be reported, got %v", st.Notes)
	}
}

func TestReachableProxyIsInjected(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	co := config.ContextOptimization{
		CompressionProxy: config.CompressionProxy{URL: "http://" + ln.Addr().String()},
		CommandFilter:    config.CommandFilter{Enabled: off()},
	}
	st := Activate(co, t.TempDir())
	if !st.ProxyReachable {
		t.Fatalf("a listening socket must be reachable, notes: %v", st.Notes)
	}
	var found bool
	for _, kv := range st.Env() {
		if kv == "ANTHROPIC_BASE_URL=http://"+ln.Addr().String() {
			found = true
		}
	}
	if !found {
		t.Fatalf("proxy URL missing from env: %v", st.Env())
	}
}

// Malformed pre-existing settings must not be silently overwritten.
func TestInvalidExistingSettingsIsReportedNotClobbered(t *testing.T) {
	dir := t.TempDir()
	bin := fakeBinary(t, dir, "rtk")
	claude := filepath.Join(dir, ".claude")
	os.MkdirAll(claude, 0o755)
	broken := []byte("{ not json")
	os.WriteFile(filepath.Join(claude, "settings.json"), broken, 0o644)

	co := config.ContextOptimization{
		CompressionProxy: config.CompressionProxy{Enabled: off()},
		CommandFilter:    config.CommandFilter{Binary: bin},
	}
	st := Activate(co, dir)
	if st.FilterVerified {
		t.Fatal("must not claim success when settings.json could not be merged")
	}
	after, _ := os.ReadFile(filepath.Join(claude, "settings.json"))
	if string(after) != string(broken) {
		t.Fatal("the user's file must be left exactly as it was")
	}
}
