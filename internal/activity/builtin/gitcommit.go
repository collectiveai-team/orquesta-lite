package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
)

// GitCommitExecutor lands one commit for work a flow has already gated green.
//
// It exists because a commit is not a gate. A gate that fails aborts the run,
// which is the right answer for a red test suite and the wrong one for a commit
// hook: most hook failures are a formatter rewriting the files it was handed,
// and the rest are findings an agent can act on if it ever sees them. So this
// activity reports failure in its output instead of raising it, and lets the
// flow decide — with `requireCommitted` for the step that must be the last word.
type GitCommitExecutor struct {
	DefaultDir string
}

func (g *GitCommitExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "git.commit", Version: "1", Effect: activity.EffectAtMostOnce}
}

type gitCommitInput struct {
	// Enabled carries the flow's condition as data rather than as a step `if`.
	// A skipped step resolves to nil, and `&&` does not short-circuit, so any
	// later step guarded on this one's output would fail to resolve and kill
	// the run. Running always and reporting a skip keeps those refs valid.
	Enabled          *bool  `json:"enabled,omitempty"`
	Type             string `json:"type,omitempty"`
	Scope            string `json:"scope,omitempty"`
	Subject          string `json:"subject,omitempty"`
	Body             string `json:"body,omitempty"`
	RequireCommitted bool   `json:"requireCommitted,omitempty"`
	Dir              string `json:"dir,omitempty"`
}

type gitCommitOutput struct {
	Committed bool   `json:"committed"`
	Blocked   bool   `json:"blocked"`
	Retried   bool   `json:"retried"`
	Reason    string `json:"reason,omitempty"`
	Message   string `json:"message,omitempty"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
}

// subjectLimit keeps the first line within the width git and every commit-lint
// convention expect.
const subjectLimit = 72

func (g *GitCommitExecutor) Execute(ctx context.Context, request activity.Request) (activity.Result, error) {
	var input gitCommitInput
	if err := strictJSON(request.Inputs, &input); err != nil {
		return activity.Result{}, contractError("git.commit input", err)
	}
	dir := input.Dir
	if dir == "" {
		dir = g.DefaultDir
	}

	if input.Enabled != nil && !*input.Enabled {
		return gitCommitResult(gitCommitOutput{Reason: "not enabled for this step"}, nil)
	}
	if strings.TrimSpace(input.Subject) == "" {
		return activity.Result{}, contractError("git.commit input", fmt.Errorf("subject is required: a commit with no subject would land an unreadable message"))
	}
	if _, _, code := git(ctx, dir, "rev-parse", "--git-dir"); code != 0 {
		return activity.Result{}, &activity.Error{Class: activity.ErrorPermanent, Op: "git.commit", Err: fmt.Errorf("not a git repository: %s", dir)}
	}

	message := commitMessage(input)
	output := gitCommitOutput{Message: message}

	staged, err := stageAll(ctx, dir)
	if err != nil {
		return activity.Result{}, err
	}
	if !staged {
		output.Reason = "nothing to commit"
		return gitCommitResult(output, nil)
	}

	stdout, stderr, code := commit(ctx, dir, message)
	if code == 0 {
		output.Committed = true
		return gitCommitResult(output, nil)
	}

	// A hook that rewrites the files it was given leaves the work unstaged and
	// the commit refused. Re-staging and trying once more is the whole fix, and
	// it costs nothing compared to waking an agent for a trailing newline.
	restaged, stageErr := stageAll(ctx, dir)
	if stageErr == nil && restaged {
		retryOut, retryErr, retryCode := commit(ctx, dir, message)
		output.Retried = true
		if retryCode == 0 {
			output.Committed = true
			return gitCommitResult(output, nil)
		}
		stdout, stderr, code = retryOut, retryErr, retryCode
	}

	output.Blocked = true
	output.ExitCode = code
	output.Stdout, output.Stderr = stdout, stderr
	output.Reason = "commit refused; see stdout and stderr"
	if input.RequireCommitted {
		return gitCommitResult(output, &activity.Error{
			Class: activity.ErrorGateFailed,
			Op:    "git.commit",
			Err:   fmt.Errorf("commit refused with exit %d: %s", code, firstLine(stderr, stdout)),
		})
	}
	return gitCommitResult(output, nil)
}

func gitCommitResult(output gitCommitOutput, err error) (activity.Result, error) {
	raw, _ := json.Marshal(output)
	return activity.Result{Output: raw}, err
}

// commitMessage assembles a Conventional Commits message. The subject is cut to
// its first line and to subjectLimit so a ticket title copied verbatim cannot
// produce a message a commit-msg hook rejects.
func commitMessage(input gitCommitInput) string {
	kind := strings.TrimSpace(input.Type)
	if kind == "" {
		kind = "chore"
	}
	subject := strings.TrimSpace(strings.SplitN(input.Subject, "\n", 2)[0])
	header := kind
	if scope := strings.TrimSpace(input.Scope); scope != "" {
		header += "(" + scope + ")"
	}
	header += ": "
	if room := subjectLimit - len(header); len(subject) > room && room > 0 {
		subject = strings.TrimSpace(subject[:room])
	}
	message := header + subject
	if body := strings.TrimSpace(input.Body); body != "" {
		message += "\n\n" + body
	}
	return message
}

// stageAll stages every change and reports whether anything is staged.
func stageAll(ctx context.Context, dir string) (bool, error) {
	if _, stderr, code := git(ctx, dir, "add", "-A"); code != 0 {
		return false, &activity.Error{Class: activity.ErrorPermanent, Op: "git.commit", Err: fmt.Errorf("git add failed: %s", firstLine(stderr))}
	}
	_, _, code := git(ctx, dir, "diff", "--cached", "--quiet")
	return code != 0, nil
}

func commit(ctx context.Context, dir, message string) (stdout, stderr string, code int) {
	return git(ctx, dir, "commit", "-m", message)
}

func git(ctx context.Context, dir string, args ...string) (stdout, stderr string, code int) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	var outBuf, errBuf bytes.Buffer
	command.Stdout, command.Stderr = &outBuf, &errBuf
	err := command.Run()
	code = 0
	if command.ProcessState != nil {
		code = command.ProcessState.ExitCode()
	} else if err != nil {
		code = -1
	}
	return outBuf.String(), errBuf.String(), code
}

func firstLine(candidates ...string) string {
	for _, candidate := range candidates {
		for _, line := range strings.Split(candidate, "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				return trimmed
			}
		}
	}
	return "no output"
}
