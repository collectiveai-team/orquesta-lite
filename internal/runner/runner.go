package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Spec describes how to invoke an agent subprocess.
type Spec struct {
	Cmd              []string
	Prompt           string
	ResultPath       string
	Timeout          time.Duration
	RateLimitPattern string
	// TemplateVars holds additional {{KEY}} substitutions applied after
	// {{PROMPT}} is resolved. Keys are the bare names without braces.
	TemplateVars map[string]string
}

// Result holds the captured output and derived state after RunAgent returns.
type Result struct {
	Stdout       string
	Stderr       string
	TimedOut     bool
	RateLimited  bool
	ResultExists bool
	ExitCode     int
	Duration     time.Duration
	// CodexHeader is non-nil when stdout begins with an "OpenAI Codex" banner.
	// Keys include: workdir, model, provider, approval, sandbox.
	CodexHeader map[string]string
}

// StderrTail returns the last n bytes of Stderr, adjusted to a valid UTF-8
// boundary so the returned string is always valid UTF-8.
func (r *Result) StderrTail(n int) string {
	return tailString(r.Stderr, n)
}

// StdoutTail returns the last n bytes of Stdout, adjusted to a valid UTF-8
// boundary so the returned string is always valid UTF-8.
func (r *Result) StdoutTail(n int) string {
	return tailString(r.Stdout, n)
}

// TailString returns the last n bytes of s, walking back to a UTF-8 boundary.
// It is exported so other packages (e.g. commands) can reuse the same helper.
func TailString(s string, n int) string {
	return tailString(s, n)
}

// tailString is the unexported implementation used within this package.
func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	raw := s[len(s)-n:]
	// Advance past any partial UTF-8 leading byte so we start on a boundary.
	for i, b := range []byte(raw) {
		if b&0xC0 != 0x80 { // not a continuation byte
			return raw[i:]
		}
	}
	return raw
}

// codexHeaderKeys are the keys extracted from a codex startup banner.
var codexHeaderKeys = map[string]bool{
	"workdir":  true,
	"model":    true,
	"provider": true,
	"approval": true,
	"sandbox":  true,
}

// parseCodexHeader scans stdout for the "OpenAI Codex" banner and extracts
// key: value pairs between the two "--------" separator lines.
// Returns nil when no banner is found.
func parseCodexHeader(stdout string) map[string]string {
	if !strings.HasPrefix(stdout, "OpenAI Codex") {
		return nil
	}
	lines := strings.SplitN(stdout, "\n", 20) // header is always near the top
	inBlock := false
	result := make(map[string]string)
	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		if trimmed == "--------" {
			if !inBlock {
				inBlock = true
				continue
			}
			// Second separator — done.
			break
		}
		if !inBlock {
			continue
		}
		idx := strings.IndexByte(trimmed, ':')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:idx])
		val := strings.TrimSpace(trimmed[idx+1:])
		if codexHeaderKeys[key] {
			result[key] = val
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// RunAgent executes the agent described by s.
//
// The {{PROMPT}} token in every element of s.Cmd is replaced with s.Prompt
// before the command is launched. The existing result file (if any) is removed
// first so callers can reliably detect "agent did not write". The subprocess is
// killed when s.Timeout elapses.
//
// RunAgent returns (*Result, nil) even when the subprocess exits non-zero;
// callers inspect Result.ExitCode, Result.TimedOut, and Result.RateLimited.
func RunAgent(ctx context.Context, s Spec) (*Result, error) {
	if len(s.Cmd) == 0 {
		return nil, errors.New("empty cmd")
	}

	// Replace {{PROMPT}} placeholder in every token first (existing contract).
	args := make([]string, len(s.Cmd))
	for i, tok := range s.Cmd {
		args[i] = strings.ReplaceAll(tok, "{{PROMPT}}", s.Prompt)
	}
	// Then substitute any additional {{KEY}} vars from TemplateVars.
	// Unknown keys are left as-is; no error is returned.
	for key, val := range s.TemplateVars {
		placeholder := "{{" + key + "}}"
		for i, tok := range args {
			args[i] = strings.ReplaceAll(tok, placeholder, val)
		}
	}

	// Pre-clear stale result file so we can detect "did not write".
	_ = os.Remove(s.ResultPath)

	cctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, args[0], args[1:]...)

	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)

	res := &Result{
		Stdout:   outBuf.String(),
		Stderr:   errBuf.String(),
		Duration: dur,
		ExitCode: cmd.ProcessState.ExitCode(),
	}
	res.CodexHeader = parseCodexHeader(res.Stdout)

	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
	}

	if s.RateLimitPattern != "" {
		re, errRe := regexp.Compile("(?i)" + s.RateLimitPattern)
		if errRe == nil && (re.MatchString(res.Stderr) || re.MatchString(res.Stdout)) {
			res.RateLimited = true
		}
	}

	if _, statErr := os.Stat(s.ResultPath); statErr == nil {
		res.ResultExists = true
	}

	// Suppress the exec error — callers inspect the Result fields instead.
	_ = err
	return res, nil
}
