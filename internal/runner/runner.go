package runner

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/providers"
)

// Spec describes how to invoke an agent subprocess.
type Spec struct {
	Cmd                        []string
	Provider                   string
	Model                      string
	Effort                     string
	DangerouslySkipPermissions bool
	SafeMode                   bool
	ResumeSessionID            string
	ForkSession                bool
	Prompt                     string
	ResultPath                 string
	Timeout                    time.Duration
	RateLimitPattern           string
	// TemplateVars holds additional {{KEY}} substitutions applied after
	// {{PROMPT}} is resolved. Keys are the bare names without braces.
	TemplateVars map[string]string
	// Env is the full environment for the subprocess. Nil inherits the parent's,
	// which is the historical behavior. Non-nil is set verbatim, so callers must
	// include os.Environ() themselves — see contextopt.Status.Env.
	Env []string
}

// Result holds the captured output and derived state after RunAgent returns.
type Result struct {
	Stdout      string
	Stderr      string
	TimedOut    bool
	RateLimited bool
	// AuthFailed is set when the agent CLI tried to authenticate interactively
	// (e.g. opened a browser OAuth flow) instead of running headless — a sign
	// its cached credentials are missing or expired. Such an agent will keep
	// failing this session, so it is treated as non-recoverable and skipped.
	AuthFailed   bool
	ResultExists bool
	ExitCode     int
	Duration     time.Duration
	SessionID    string
	FinalText    string
	Events       []providers.Event
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

// RunAgent executes the agent described by s. Legacy cmd agents still receive
// {{PROMPT}} argv substitution; provider agents receive the prompt on stdin.
// The existing result file (if any) is removed first so callers can reliably
// detect "agent did not write". The subprocess is killed when s.Timeout elapses.
//
// RunAgent returns (*Result, nil) even when the subprocess exits non-zero;
// callers inspect Result.ExitCode, Result.TimedOut, and Result.RateLimited.
func RunAgent(ctx context.Context, s Spec) (*Result, error) {
	launch, provider, err := buildLaunch(ctx, s)
	if err != nil {
		return nil, err
	}

	// Pre-clear stale result file so we can detect "did not write".
	_ = os.Remove(s.ResultPath)

	cctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, launch.Args[0], launch.Args[1:]...)
	if len(s.Env) > 0 {
		cmd.Env = s.Env
	}
	if launch.Stdin != "" {
		cmd.Stdin = strings.NewReader(launch.Stdin)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	res := &Result{}
	stdoutDone := make(chan struct{})
	stderrDone := make(chan struct{})
	go scanStdout(stdout, provider, res, stdoutDone)
	go scanStderr(stderr, res, stderrDone)

	err = cmd.Wait()
	<-stdoutDone
	<-stderrDone

	res.Duration = time.Since(start)
	res.ExitCode = -1
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	res.CodexHeader = parseCodexHeader(res.Stdout)

	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
	}

	if provider != nil {
		res.RateLimited = detectRateLimit(s.RateLimitPattern, providerErrorText(res), res.Stderr)
	} else {
		res.RateLimited = detectRateLimit(s.RateLimitPattern, res.Stdout, res.Stderr)
	}
	res.AuthFailed = detectAuthPrompt(res.Stdout, res.Stderr)

	if _, statErr := os.Stat(s.ResultPath); statErr == nil {
		res.ResultExists = true
	}

	// Suppress the exec error — callers inspect the Result fields instead.
	_ = err
	return res, nil
}

func buildLaunch(ctx context.Context, s Spec) (providers.Launch, providers.Provider, error) {
	if s.Provider != "" {
		if len(s.Cmd) > 0 {
			return providers.Launch{}, nil, errors.New("spec cannot specify both cmd and provider")
		}
		provider, err := providers.New(s.Provider)
		if err != nil {
			return providers.Launch{}, nil, err
		}
		launch, err := provider.Build(ctx, s.Prompt, providers.Options{
			Model:                s.Model,
			Effort:               s.Effort,
			DangerouslySkipPerms: s.DangerouslySkipPermissions,
			SafeMode:             s.SafeMode,
			ResumeSessionID:      s.ResumeSessionID,
			ForkSession:          s.ForkSession,
		})
		if err != nil {
			return providers.Launch{}, nil, err
		}
		if len(launch.Args) == 0 {
			return providers.Launch{}, nil, errors.New("provider built empty args")
		}
		return launch, provider, nil
	}

	if len(s.Cmd) == 0 {
		return providers.Launch{}, nil, errors.New("empty cmd")
	}

	args := make([]string, len(s.Cmd))
	for i, tok := range s.Cmd {
		args[i] = strings.ReplaceAll(tok, "{{PROMPT}}", s.Prompt)
	}
	for key, val := range s.TemplateVars {
		placeholder := "{{" + key + "}}"
		for i, tok := range args {
			args[i] = strings.ReplaceAll(tok, placeholder, val)
		}
	}
	return providers.Launch{Args: args}, nil, nil
}

func scanStdout(r io.Reader, p providers.Provider, res *Result, done chan<- struct{}) {
	defer close(done)

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		res.Stdout += line + "\n"
		if p == nil {
			continue
		}
		for _, ev := range p.ParseLine(line) {
			res.Events = append(res.Events, ev)
			switch ev.Type {
			case providers.EventResult:
				res.FinalText = ev.Result
			case providers.EventSessionID:
				res.SessionID = ev.SessionID
			case providers.EventError:
				if ev.Result != "" {
					res.FinalText = ev.Result
				}
			}
		}
	}
}

func scanStderr(r io.Reader, res *Result, done chan<- struct{}) {
	defer close(done)

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		res.Stderr += scanner.Text() + "\n"
	}
}

func detectRateLimit(pattern, stdout, stderr string) bool {
	if pattern == "" {
		return false
	}
	re, errRe := regexp.Compile("(?i)" + pattern)
	return errRe == nil && (re.MatchString(stderr) || re.MatchString(stdout))
}

// authPromptRe matches the signatures an agent CLI emits when it falls back to
// interactive authentication (no valid headless credential) or when its
// credential tier is no longer accepted at all. Seen with gemini-cli: an OAuth
// browser flow plus a "[Y/n]" continue prompt that, with no TTY, resolves to a
// cancellation; and "IneligibleTierError: This client is no longer supported for
// Gemini Code Assist for individuals" (reasonCode UNSUPPORTED_CLIENT) once the
// free OAuth tier is discontinued — an unrecoverable auth state that must bench
// the agent immediately rather than be retried as a generic missing result.
//
// Kept deliberately narrow and CLI-specific. Broad phrases like "not
// authenticated" or "login required" are intentionally NOT matched: they appear
// in ordinary application output (e.g. a FastAPI 401 body is literally
// {"detail":"Not authenticated"}) and in code the agent is editing, which would
// misclassify a perfectly healthy agent. As a second guard, classify only
// treats a match as auth_failed when the agent wrote no result (see classify).
var authPromptRe = regexp.MustCompile(`(?i)opening authentication page|authentication cancelled|fatalcancellationerror|reauthenticate|run (?:codex|claude|gemini) login|ineligibletiererror|unsupported_client|no longer supported for gemini`)

func detectAuthPrompt(stdout, stderr string) bool {
	return authPromptRe.MatchString(stderr) || authPromptRe.MatchString(stdout)
}

func providerErrorText(res *Result) string {
	var b strings.Builder
	for _, ev := range res.Events {
		if ev.Type != providers.EventError {
			continue
		}
		if ev.Result != "" {
			b.WriteString(ev.Result)
			b.WriteByte('\n')
		}
		if ev.Text != "" {
			b.WriteString(ev.Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
