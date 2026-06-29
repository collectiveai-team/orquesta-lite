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
	"time"

	"github.com/lionelchamorro/orquestalite/internal/eventlog"
	"github.com/lionelchamorro/orquestalite/internal/watch"
)

// WatchOptions holds the parameters for the watch command.
type WatchOptions struct {
	ProjectDir   string
	Interval     time.Duration
	Issues       bool
	PRs          bool
	ReviewOwnPRs bool
	PublishPRs   bool // post PR reviews via gh when reviewing
	LogFormat    eventlog.Format
	Out          io.Writer
}

// Watch runs the long-lived per-project daemon: polls GitHub via `gh` for
// issues/PRs and triggers intake (issues) or review (PRs) on new items. State
// persists in .orquestalite/watch.json so an item is never processed twice.
func Watch(ctx context.Context, opts WatchOptions) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	enabled := map[watch.ItemType]bool{}
	// Default: watch both when neither flag is given. Any explicit flag
	// restricts to exactly the types the operator asked for — passing only
	// --issues polls issues and never PRs.
	if !opts.Issues && !opts.PRs {
		enabled[watch.ItemIssue] = true
		enabled[watch.ItemPR] = true
	} else {
		if opts.Issues {
			enabled[watch.ItemIssue] = true
		}
		if opts.PRs {
			enabled[watch.ItemPR] = true
		}
	}

	lister := &ghLister{dir: opts.ProjectDir, out: opts.Out}
	cfg := watch.Config{
		ProjectDir:   opts.ProjectDir,
		Interval:     opts.Interval,
		Enabled:      enabled,
		ReviewOwnPRs: opts.ReviewOwnPRs,
		Lister:       lister,
		Intake:       newIntakeTrigger(opts, lister),
		Review:       newReviewTrigger(opts),
	}
	fmt.Fprintf(opts.Out, "watch: %s — enabled: %s — interval %s\n",
		opts.ProjectDir, summaryOfEnabled(enabled), intervalLabel(cfg.Interval))
	return watch.Run(ctx, cfg)
}

func summaryOfEnabled(enabled map[watch.ItemType]bool) string {
	var on []string
	if enabled[watch.ItemIssue] {
		on = append(on, "issues")
	}
	if enabled[watch.ItemPR] {
		on = append(on, "prs")
	}
	if len(on) == 0 {
		return "none"
	}
	return strings.Join(on, ", ")
}

func intervalLabel(d time.Duration) string {
	if d <= 0 {
		return "60s (default)"
	}
	return d.String()
}

// newIntakeTrigger returns a watch.Intake func that writes the issue body to a
// temp file and runs the intake role→plan→run pipeline on it.
func newIntakeTrigger(opts WatchOptions, _ *ghLister) func(context.Context, string) error {
	return func(ctx context.Context, issueBody string) error {
		issuePath := filepath.Join(opts.ProjectDir, ".orquestalite", "watch-issue.md")
		if err := os.WriteFile(issuePath, []byte(issueBody), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(opts.Out, "watch: new issue → intake\n")
		return Intake(ctx, IntakeOptions{
			ProjectDir: opts.ProjectDir,
			IssuePath:  issuePath,
			Run:        true,
			LogFormat:  opts.LogFormat,
			Out:        opts.Out,
		})
	}
}

// newReviewTrigger returns a watch.Review func that runs the critic review over
// the PR's diff and (when PublishPRs) posts the verdict via gh.
func newReviewTrigger(opts WatchOptions) func(context.Context, string) error {
	return func(ctx context.Context, prNumber string) error {
		fmt.Fprintf(opts.Out, "watch: new PR #%s → review\n", prNumber)
		return Review(ctx, ReviewOptions{
			ProjectDir: opts.ProjectDir,
			PR:         prNumber,
			Publish:    opts.PublishPRs,
			LogFormat:  opts.LogFormat,
			Out:        opts.Out,
		})
	}
}

// ghLister implements watch.Lister by shelling out to the already-authenticated
// `gh` CLI in the project directory.
type ghLister struct {
	dir string
	out io.Writer
}

func (g *ghLister) WhoAmI(ctx context.Context) (string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", errors.New("gh CLI not found on PATH")
	}
	out, err := ghRun(ctx, g.dir, "api", "user", "--jq", ".login")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (g *ghLister) ListIssues(ctx context.Context, since time.Time) ([]watch.Item, error) {
	args := []string{"issue", "list", "--json", "number,title,body,author,updatedAt", "--limit", "100", "--state", "all"}
	out, err := ghRun(ctx, g.dir, args...)
	if err != nil {
		return nil, err
	}
	return parseGHItems(string(out), watch.ItemIssue, since)
}

func (g *ghLister) ListPRs(ctx context.Context, since time.Time) ([]watch.Item, error) {
	args := []string{"pr", "list", "--json", "number,title,author,updatedAt", "--limit", "100", "--state", "all"}
	out, err := ghRun(ctx, g.dir, args...)
	if err != nil {
		return nil, err
	}
	items, err := parseGHItems(string(out), watch.ItemPR, since)
	// PR reviews operate on the PR number/diff, not a body; leave Body empty.
	return items, err
}

// parseGHItems parses `gh <kind> list --json` output into watch.Items,
// dropping anything at or before the since cursor (the caller asked for "since
// last seen"). The author field accepts both the nested `{login:...}` object
// gh sometimes returns and a bare string.
func parseGHItems(raw string, typ watch.ItemType, since time.Time) ([]watch.Item, error) {
	// gh emits author as {"login":"x","..."} for some resources; decode into a
	// tolerant intermediate that accepts either shape.
	var raws []struct {
		Number    int             `json:"number"`
		Title     string          `json:"title"`
		Body      string          `json:"body"`
		Author    json.RawMessage `json:"author"`
		UpdatedAt time.Time       `json:"updatedAt"`
	}
	if err := json.Unmarshal([]byte(raw), &raws); err != nil {
		return nil, fmt.Errorf("parse gh list: %w", err)
	}
	items := make([]watch.Item, 0, len(raws))
	for _, r := range raws {
		if !r.UpdatedAt.After(since) {
			continue
		}
		items = append(items, watch.Item{
			Type:      typ,
			Number:    fmt.Sprintf("%d", r.Number),
			Title:     r.Title,
			Body:      r.Body,
			Author:    parseAuthor(r.Author),
			UpdatedAt: r.UpdatedAt,
		})
	}
	return items, nil
}

// parseAuthor extracts the login from either {"login":"x",...} or "x".
func parseAuthor(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj struct {
		Login string `json:"login"`
	}
	_ = json.Unmarshal(raw, &obj)
	return obj.Login
}
