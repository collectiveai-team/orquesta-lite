package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/lionelchamorro/orquestalite/internal/eventlog"
	"github.com/lionelchamorro/orquestalite/internal/gitx"
	"github.com/lionelchamorro/orquestalite/internal/invoke"
	"github.com/lionelchamorro/orquestalite/internal/results"
)

// ReviewOptions holds the parameters for the review command.
type ReviewOptions struct {
	ProjectDir string
	TeamPath   string
	// PR identifies the pull request to review. When non-empty it is resolved
	// to its base/head refs via `gh pr view` (unless Base and Head are given
	// explicitly). It is also the target of `gh pr review` publication.
	PR string
	// Base and Head are explicit git refs to diff. When both are set they
	// override PR resolution; PR is still used as the publication target when
	// Publish is true.
	Base string
	Head string
	// Publish posts the verdict as a PR review via `gh pr review`. No-op
	// without a PR target.
	Publish bool
	// LogFormat controls the stdout pretty format.
	LogFormat eventlog.Format
	Out       io.Writer
}

// ReviewCriticCaller invokes the review-oriented critic role over a diff and
// returns its parsed verdict. The live implementation wires invoke.Role; tests
// inject a stub so the orchestration (resolve → diff → review → publish) is
// exercised without a live agent.
type ReviewCriticCaller interface {
	RunReview(ctx context.Context, diff string) (*results.CriticResult, error)
}

// GHPRClient is the subset of `gh` operations the review command needs. The
// production implementation shells out to `gh`; tests inject a fake so
// publication is asserted with `gh` mocked.
type GHPRClient interface {
	// ResolvePR returns the base and head ref names for a PR number.
	ResolvePR(ctx context.Context, pr string) (base, head string, err error)
	// PostReview submits a PR review: event is "APPROVE" or "REQUEST_CHANGES".
	PostReview(ctx context.Context, pr, event, body string) error
}

// Review resolves the PR (or explicit refs), assembles the diff, runs the
// critic role under a review-oriented prompt, and (when --publish is set) posts
// the verdict as a PR review via `gh`. The result is archived under
// `.orquestalite/results/`.
func Review(ctx context.Context, opts ReviewOptions) error {
	return ReviewWith(ctx, opts, &ghPRRunner{dir: opts.ProjectDir}, reviewLiveCallerFactory)
}

// ReviewWith is the testable core: callers inject the gh client and a factory
// for the review role caller so the resolve→diff→review→publish flow is
// exercised without a live agent or real gh.
func ReviewWith(ctx context.Context, opts ReviewOptions, gh GHPRClient, callerFactory func(ReviewOptions) (ReviewCriticCaller, func() error, error)) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	base := opts.Base
	head := opts.Head
	if base == "" || head == "" {
		if opts.PR == "" {
			return errors.New("review requires a PR number (`--pr`) or explicit `--base`/`--head` refs")
		}
		b, h, err := gh.ResolvePR(ctx, opts.PR)
		if err != nil {
			return fmt.Errorf("resolve PR %s: %w", opts.PR, err)
		}
		if base == "" {
			base = b
		}
		if head == "" {
			head = h
		}
	}
	if base == "" || head == "" {
		return errors.New("review could not determine base and head refs")
	}

	diff, err := gitx.DiffRefs(opts.ProjectDir, base, head)
	if err != nil {
		return fmt.Errorf("assemble diff %s..%s: %w", base, head, err)
	}
	if strings.TrimSpace(diff) == "" {
		fmt.Fprintf(opts.Out, "review: no diff between %s and %s — nothing to review\n", base, head)
		return nil
	}

	caller, cleanup, err := callerFactory(opts)
	if err != nil {
		return err
	}
	defer cleanup()

	res, err := caller.RunReview(ctx, diff)
	if err != nil {
		return fmt.Errorf("review role: %w", err)
	}
	fmt.Fprintf(opts.Out, "review: %s\n", verdictLine(res))

	if opts.Publish && opts.PR != "" {
		event := "APPROVE"
		if res.Status != "approved" {
			event = "REQUEST_CHANGES"
		}
		body := reviewBody(res, base, head)
		if err := gh.PostReview(ctx, opts.PR, event, body); err != nil {
			return fmt.Errorf("post review: %w", err)
		}
		fmt.Fprintf(opts.Out, "review: posted %s on PR %s\n", event, opts.PR)
	}
	return nil
}

func verdictLine(res *results.CriticResult) string {
	if res.Status == "approved" {
		return "APPROVED"
	}
	return fmt.Sprintf("CHANGES REQUESTED (%d concern(s))", len(res.Concerns))
}

// reviewBody renders the critic's observations into the body of a `gh pr review`
// post: a one-line verdict, then each concern with severity/location/issue and
// its suggestion.
func reviewBody(res *results.CriticResult, base, head string) string {
	var b strings.Builder
	if res.Status == "approved" {
		b.WriteString("Approved by orq-lite review.\n\n")
	} else {
		b.WriteString("Changes requested by orq-lite review.\n\n")
	}
	fmt.Fprintf(&b, "Diff range: %s..%s\n\n", base, head)
	if len(res.Concerns) == 0 {
		b.WriteString("(no itemised concerns)\n")
		return b.String()
	}
	for i, c := range res.Concerns {
		fmt.Fprintf(&b, "%d. [%s] %s — %s", i+1, c.Severity, c.Where, c.Issue)
		if c.Suggestion != "" {
			fmt.Fprintf(&b, "\n   Suggestion: %s", c.Suggestion)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// reviewLiveCallerFactory builds a ReviewCriticCaller that runs the configured
// critic role under the review prompt. It reuses the same live-agent machinery
// as the rest of the orchestrator (fallback, rate-limit waiting, health).
func reviewLiveCallerFactory(opts ReviewOptions) (ReviewCriticCaller, func() error, error) {
	deps, cleanup, err := newLiveDeps(liveDepsOptions{
		ProjectDir: opts.ProjectDir,
		TeamPath:   teamPathOrDefault(opts.TeamPath, opts.ProjectDir),
		LogFormat:  opts.LogFormat,
		Roles:      []string{"critic"},
		Command:    "review",
	})
	if err != nil {
		return nil, nil, err
	}
	return &liveReviewCaller{deps: deps}, cleanup, nil
}

func teamPathOrDefault(teamPath, projectDir string) string {
	if teamPath != "" {
		return teamPath
	}
	return filepath.Join(projectDir, "team.json")
}

// liveReviewCaller runs the critic role with a review-oriented prompt and
// archives the result. The result contract is the critic's (approved/rejected
// + concerns) reused for PR review.
type liveReviewCaller struct {
	deps *liveDeps
}

func (c *liveReviewCaller) RunReview(ctx context.Context, diff string) (*results.CriticResult, error) {
	promptPath := "prompts/review.md"
	// Fall back to the configured critic prompt when no review prompt exists so
	// a team without the bespoke prompt still gets a review.
	if _, err := os.Stat(filepath.Join(c.deps.dir, promptPath)); err != nil {
		if spec, ok := c.deps.inv.Specs["critic"]; ok && spec.PromptPath != "" {
			promptPath = spec.PromptPath
		}
	}
	r, err := invoke.Role(ctx, c.deps.inv, "critic", invoke.RoleCall{
		ArchiveRole: "review",
		PromptPath:  promptPath,
		ResultPath:  ".orquestalite/results/review.json",
		Vars: map[string]string{
			"DIFF":    diff,
			"TASK_ID": "_review",
		},
	}, invoke.RunContext{TaskID: "_review", Cycle: 0, Attempt: 1}, results.ParseCritic)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// ghPRRunner is the production GHPRClient: it shells out to the `gh` CLI in the
// project directory, reusing the gh wrapper pattern from factorycmd.go.
type ghPRRunner struct {
	dir string
}

func (g *ghPRRunner) ResolvePR(ctx context.Context, pr string) (string, string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", "", errors.New("gh CLI not found on PATH")
	}
	out, err := ghRun(ctx, g.dir, "pr", "view", pr, "--json", "baseRefName,headRefName")
	if err != nil {
		return "", "", err
	}
	var prr struct {
		BaseRefName string `json:"baseRefName"`
		HeadRefName string `json:"headRefName"`
	}
	if err := json.Unmarshal(out, &prr); err != nil {
		return "", "", fmt.Errorf("parse gh pr view: %w", err)
	}
	if prr.BaseRefName == "" || prr.HeadRefName == "" {
		return "", "", errors.New("gh returned empty base/head refs")
	}
	return prr.BaseRefName, prr.HeadRefName, nil
}

func (g *ghPRRunner) PostReview(ctx context.Context, pr, event, body string) error {
	if _, err := exec.LookPath("gh"); err != nil {
		return errors.New("gh CLI not found on PATH")
	}
	_, err := ghRun(ctx, g.dir, "pr", "review", pr, "--"+event, "--body", body)
	return err
}

// ghRun runs `gh` in dir and returns combined output. A non-zero exit surfaces
// the captured output for diagnosis.
func ghRun(ctx context.Context, dir string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, "gh", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("gh %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return out, nil
}
