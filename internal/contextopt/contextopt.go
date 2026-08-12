// Package contextopt activates the external context-reduction tools for a run.
//
// Two tools, two mechanisms:
//
//   - The compression proxy is a daemon. orq-lite does not start or supervise
//     it; it probes the configured address and, when reachable, points the agent
//     subprocess at it through ANTHROPIC_BASE_URL.
//   - The command filter is a one-shot binary invoked by a PreToolUse hook. Its
//     hook is written into the *project's* .claude/settings.json — never the
//     user's global config — and its directory is prepended to the subprocess
//     PATH, because the hook rewrites commands to `rtk <cmd>` with no path.
//
// Everything here fails open. A missing binary, an unreachable proxy or a
// failed verification degrades to an unoptimized run; none of them fails one.
// That is deliberate: these are optimizations, and an optimization that can
// abort a run is worse than no optimization.
package contextopt

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/config"
)

// Status describes what was activated, for logging and for `doctor`.
type Status struct {
	ProxyEnabled   bool
	ProxyURL       string
	ProxyReachable bool
	FilterEnabled  bool
	FilterBinary   string // resolved absolute path, empty when unresolved
	FilterVerified bool
	// Notes explains every decision, including the ones that turned a tool off.
	// Callers surface these; a silently skipped optimization is indistinguishable
	// from one that never ran.
	Notes []string
}

// Env returns the environment for an agent subprocess: the parent environment
// plus whatever the active tools require. Returns nil when nothing is active,
// so callers can leave cmd.Env unset and inherit as before.
func (s Status) Env() []string {
	var extra []string
	if s.ProxyEnabled && s.ProxyReachable {
		extra = append(extra, "ANTHROPIC_BASE_URL="+s.ProxyURL)
	}
	if s.FilterEnabled && s.FilterVerified && s.FilterBinary != "" {
		dir := filepath.Dir(s.FilterBinary)
		extra = append(extra, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	if len(extra) == 0 {
		return nil
	}
	base := os.Environ()
	// Later entries win in exec, so appending is enough to override PATH.
	return append(base, extra...)
}

// Summary renders a one-line operator summary.
func (s Status) Summary() string {
	part := func(on, ok bool, name string) string {
		switch {
		case !on:
			return name + "=off"
		case ok:
			return name + "=on"
		default:
			return name + "=unavailable"
		}
	}
	return part(s.ProxyEnabled, s.ProxyReachable, "compression_proxy") + " " +
		part(s.FilterEnabled, s.FilterVerified, "command_filter")
}

// Activate resolves the configured tools against the machine and the project.
// projectDir is where the command filter's hook is written. It never returns an
// error: an unusable tool is reported through Status.Notes instead.
func Activate(cfg config.ContextOptimization, projectDir string) Status {
	st := Status{
		ProxyEnabled:  cfg.ProxyEnabled(),
		ProxyURL:      cfg.ProxyURL(),
		FilterEnabled: cfg.FilterEnabled(),
	}

	if st.ProxyEnabled {
		if err := probe(st.ProxyURL); err != nil {
			st.Notes = append(st.Notes, fmt.Sprintf(
				"compression proxy at %s is not reachable (%v) — running without it; see GUIDE.md to start it",
				st.ProxyURL, err))
		} else {
			st.ProxyReachable = true
			st.Notes = append(st.Notes, "compression proxy active at "+st.ProxyURL)
		}
	} else {
		st.Notes = append(st.Notes, "compression proxy disabled in team.json")
	}

	if !st.FilterEnabled {
		st.Notes = append(st.Notes, "command filter disabled in team.json")
		return st
	}

	bin, err := resolveBinary(cfg.FilterBinary())
	if err != nil {
		st.Notes = append(st.Notes, fmt.Sprintf(
			"command filter binary %q not found (%v) — running without it; see GUIDE.md to install it",
			cfg.FilterBinary(), err))
		return st
	}
	st.FilterBinary = bin

	// The hook rewrites `git status` to `rtk git status` with no path. If the
	// binary does not resolve by name in the subprocess, every rewritten command
	// dies with exit 127 and the agent retries blind — a failure mode that
	// silently invalidated a whole measurement round. Verify before enabling.
	if err := verifyRewrite(bin); err != nil {
		st.Notes = append(st.Notes, fmt.Sprintf(
			"command filter %s did not verify (%v) — running without it rather than breaking every shell call",
			bin, err))
		return st
	}

	if err := installProjectHook(projectDir, bin); err != nil {
		st.Notes = append(st.Notes, fmt.Sprintf(
			"could not write the command-filter hook into %s (%v) — running without it",
			filepath.Join(projectDir, ".claude", "settings.json"), err))
		return st
	}

	st.FilterVerified = true
	st.Notes = append(st.Notes, "command filter active via "+bin)
	return st
}

// probe dials the proxy's host:port. A TCP connect is enough and avoids
// assuming the daemon exposes any particular HTTP route.
func probe(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	host := u.Host
	if host == "" {
		return fmt.Errorf("no host in %q", raw)
	}
	if u.Port() == "" {
		host = net.JoinHostPort(host, "80")
	}
	conn, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		return err
	}
	return conn.Close()
}

// resolveBinary returns an absolute path for the filter binary. A value with a
// separator is taken as-is; a bare name is looked up on PATH.
func resolveBinary(name string) (string, error) {
	if strings.ContainsRune(name, os.PathSeparator) {
		abs, err := filepath.Abs(name)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return "", err
		}
		if info.IsDir() || info.Mode()&0o111 == 0 {
			return "", fmt.Errorf("%s is not executable", abs)
		}
		return abs, nil
	}
	return exec.LookPath(name)
}

// verifyRewrite runs the binary the way the hook will: by name, with the
// binary's directory on PATH. Exit 127 means the rewrite would fail at runtime.
func verifyRewrite(bin string) error {
	cmd := exec.Command(bin, "--version")
	cmd.Env = append(os.Environ(),
		"PATH="+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

const hookMarker = "orq-lite:command-filter"

// installProjectHook merges a PreToolUse hook into the project's
// .claude/settings.json. It merges rather than overwrites: a project may
// already carry its own hooks, and clobbering them to enable an optimization
// would be a worse trade than skipping the optimization.
//
// The command carries an absolute path even though the *rewritten* command does
// not, so the hook itself works regardless of the caller's PATH.
func installProjectHook(projectDir, bin string) error {
	dir := filepath.Join(projectDir, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "settings.json")

	settings := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return fmt.Errorf("existing %s is not valid JSON: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	entries, _ := hooks["PreToolUse"].([]any)

	// Replace our own entry if present; leave every other entry untouched.
	kept := entries[:0:0]
	for _, e := range entries {
		if blob, err := json.Marshal(e); err == nil && strings.Contains(string(blob), hookMarker) {
			continue
		}
		kept = append(kept, e)
	}
	kept = append(kept, map[string]any{
		"matcher": "Bash",
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": bin + " hook claude",
			"_source": hookMarker,
		}},
	})

	hooks["PreToolUse"] = kept
	settings["hooks"] = hooks

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}
