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

	// Replace {{PROMPT}} placeholder in every token.
	args := make([]string, len(s.Cmd))
	for i, tok := range s.Cmd {
		args[i] = strings.ReplaceAll(tok, "{{PROMPT}}", s.Prompt)
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
