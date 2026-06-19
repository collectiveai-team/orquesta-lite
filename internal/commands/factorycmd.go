package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/config"
	"github.com/lionelchamorro/orquestalite/internal/cost"
	"github.com/lionelchamorro/orquestalite/internal/eventlog"
	"github.com/lionelchamorro/orquestalite/internal/factory"
	"github.com/lionelchamorro/orquestalite/internal/gitx"
	"github.com/lionelchamorro/orquestalite/internal/invoke"
	"github.com/lionelchamorro/orquestalite/internal/results"
	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

// FactoryOptions holds the parameters for the factory command.
type FactoryOptions struct {
	ProjectDir   string
	FeaturesPath string // path to the features markdown; "" resumes the existing queue
	Force        bool   // replace an existing unfinished queue
	StatusOnly   bool   // print the queue and exit
	CreatePR     bool   // push each finished feature branch and open a PR via gh
	Resume       bool   // retry failed features (reuses their persisted task lists)
	Replan       bool   // force fresh task decomposition for every feature
	LogFormat    eventlog.Format
	Out          io.Writer
}

// Factory runs orq-lite as a feature factory: each feature from the queue is
// developed on its own git branch via a full plan+run cycle, sequentially,
// with state persisted so an interrupted queue resumes.
func Factory(ctx context.Context, opts FactoryOptions) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}

	if opts.StatusOnly {
		return factoryStatus(opts.ProjectDir, opts.Out)
	}

	if !gitx.IsRepo(opts.ProjectDir) {
		return errors.New("factory mode requires a git repository (run `git init` or `orq-lite init` first)")
	}
	clean, err := gitx.IsCleanTree(opts.ProjectDir)
	if err != nil {
		return err
	}
	if !clean {
		return errors.New("factory mode requires a clean work tree: commit or stash your changes first")
	}

	q, err := loadOrCreateQueue(ctx, opts, extractFeaturesWithLLM)
	if err != nil {
		return err
	}

	// Budget is optional and read from team.json; a missing or invalid
	// team.json surfaces later (and more precisely) when the run starts.
	fcfg := factory.Config{Resume: opts.Resume, Replan: opts.Replan}
	if cfg, cfgErr := config.Load(filepath.Join(opts.ProjectDir, "team.json")); cfgErr == nil {
		fcfg.BudgetUSD = cfg.Limits.FactoryBudgetUSD
	}

	deps := &liveFactoryDeps{dir: opts.ProjectDir, logFormat: opts.LogFormat, out: opts.Out, createPR: opts.CreatePR}
	return factory.Run(ctx, q, fcfg, deps)
}

// featureExtractor turns raw plan markdown into queued features. The production
// implementation is extractFeaturesWithLLM (the planner role); tests inject a
// pure function so queue construction is exercised without a live agent.
type featureExtractor func(ctx context.Context, opts FactoryOptions, planMarkdown string) ([]factory.Feature, error)

func loadOrCreateQueue(ctx context.Context, opts FactoryOptions, extract featureExtractor) (*factory.Queue, error) {
	existing, loadErr := factory.Load(opts.ProjectDir)
	if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
		return nil, loadErr
	}

	if opts.FeaturesPath == "" {
		// Resume an in-progress queue if one exists; otherwise fall back to
		// auto-discovering a feature file in the project root so `orq-lite
		// factory` (no args) works when a feature.md is simply dropped in.
		if existing == nil {
			if discovered := discoverFeaturesFile(opts.ProjectDir); discovered != "" {
				opts.FeaturesPath = discovered
				if opts.Out != nil {
					fmt.Fprintf(opts.Out, "factory: using auto-discovered feature file %s\n", discovered)
				}
			} else {
				return nil, errors.New("no factory queue and no feature file found: drop a feature.md in the project root or run `orq-lite factory <features.md>`")
			}
		} else {
			return existing, nil
		}
	}

	if existing != nil && existing.NextRunnable(false, nil) != nil && !opts.Force {
		return nil, errors.New("an unfinished factory queue exists: resume it with `orq-lite factory` (no args) or replace it with --force")
	}

	raw, err := os.ReadFile(opts.FeaturesPath)
	if err != nil {
		return nil, fmt.Errorf("read features: %w", err)
	}
	feats, err := extract(ctx, opts, string(raw))
	if err != nil {
		return nil, fmt.Errorf("plan features from %s: %w", opts.FeaturesPath, err)
	}
	if len(feats) == 0 {
		return nil, fmt.Errorf("planner found no implementable features in %s — the plan may be all documentation, or refine it and retry", opts.FeaturesPath)
	}
	base, err := gitx.CurrentBranch(opts.ProjectDir)
	if err != nil {
		return nil, err
	}
	q := &factory.Queue{BaseBranch: base, Features: feats}
	if err := factory.Save(opts.ProjectDir, q); err != nil {
		return nil, err
	}
	return q, nil
}

// extractFeaturesWithLLM runs the factory planner once over the raw plan to
// produce vertical-slice features. It uses the same live-agent machinery as the
// rest of the factory (so the planner role's agent chain, fallback, rate-limit
// waiting, and health tracking all apply). If the whole chain fails it returns
// an error — the caller hard-fails rather than silently falling back to the
// heading-split, which is exactly the behavior that produced document-section
// "features" in the first place.
func extractFeaturesWithLLM(ctx context.Context, opts FactoryOptions, planMarkdown string) ([]factory.Feature, error) {
	// A clear, actionable error for projects scaffolded before the planner role
	// existed — better than the generic "role not configured" from invoke.Role.
	if cfg, cfgErr := config.Load(filepath.Join(opts.ProjectDir, "team.json")); cfgErr == nil {
		if _, ok := cfg.Roles["planner"]; !ok {
			return nil, errors.New("no 'planner' role in team.json — the factory needs it to extract vertical-slice features; add a planner role (re-run `orq-lite init`, or copy the default planner role from the docs)")
		}
	}

	deps, cleanup, err := newLiveDeps(liveDepsOptions{
		ProjectDir: opts.ProjectDir,
		TeamPath:   filepath.Join(opts.ProjectDir, "team.json"),
		LogFormat:  opts.LogFormat,
		Roles:      []string{"planner"},
	})
	if err != nil {
		return nil, err
	}
	defer cleanup()

	res, err := invoke.Role(ctx, deps.inv, "planner",
		invoke.RoleCall{Vars: map[string]string{"PLAN": planMarkdown}},
		invoke.RunContext{TaskID: "_features", Attempt: 1},
		results.ParsePlanner)
	if err != nil {
		return nil, err
	}

	drafts := make([]factory.FeatureDraft, 0, len(res.Features))
	for _, f := range res.Features {
		drafts = append(drafts, factory.FeatureDraft{
			Title: f.Title,
			Plan:  planTextWithCriteria(f),
		})
	}
	return factory.NewFeatures(drafts), nil
}

// planTextWithCriteria folds the planner's acceptance criteria and file hints
// into the feature's plan markdown, so the per-feature decompose step receives
// them as part of {{PLAN}} without needing extra struct fields or prompt vars.
func planTextWithCriteria(f results.PlannerFeature) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(f.Plan))
	if len(f.AcceptanceCriteria) > 0 {
		b.WriteString("\n\n## Acceptance Criteria\n")
		for _, c := range f.AcceptanceCriteria {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	}
	if len(f.FilesLikelyTouched) > 0 {
		b.WriteString("\n## Files Likely Touched\n")
		for _, p := range f.FilesLikelyTouched {
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}
	return b.String()
}

// discoverFeaturesFile looks for a feature/goal markdown in the project root
// when `orq-lite factory` is run with no path argument and no existing queue.
// Returns the first match's full path, or "" if none of the candidates exist.
func discoverFeaturesFile(dir string) string {
	candidates := []string{
		"feature.md", "FEATURE.md",
		"features.md", "FEATURES.md",
		"goal.md", "GOAL.md",
	}
	for _, name := range candidates {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

func factoryStatus(dir string, out io.Writer) error {
	q, err := factory.Load(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(out, "no factory queue")
			return nil
		}
		return err
	}
	fmt.Fprintf(out, "base branch: %s   %s   spent: $%.2f\n", q.BaseBranch, factory.Counts(q), factory.SpentUSD(q))
	fmt.Fprintf(out, "%-5s %-12s %-32s %5s %6s %8s  %s\n", "ID", "STATUS", "BRANCH", "DONE", "FAILED", "COST", "TITLE")
	for _, f := range q.Features {
		title := f.Title
		if f.PRURL != "" {
			title += "  (" + f.PRURL + ")"
		}
		fmt.Fprintf(out, "%-5s %-12s %-32s %5d %6d %8.2f  %s\n",
			f.ID, f.Status, f.Branch, f.TasksDone, f.TasksFailed, f.CostUSD, title)
	}
	return nil
}

// liveFactoryDeps implements factory.Deps with real git and live agent runs.
type liveFactoryDeps struct {
	dir       string
	logFormat eventlog.Format
	out       io.Writer
	createPR  bool
}

func (d *liveFactoryDeps) CheckoutFeatureBranch(branch, base string) error {
	if gitx.BranchExists(d.dir, branch) {
		return gitx.Checkout(d.dir, branch)
	}
	return gitx.CheckoutNewBranch(d.dir, branch, base)
}

// CheckpointResidue preserves a half-done task's uncommitted edits (completed
// tasks are already committed) as a labelled WIP commit on the current feature
// branch. This keeps the work recoverable on the branch and leaves a clean tree
// so the subsequent `git checkout <base>` cannot abort with "local changes
// would be overwritten". No-op when the tree is already clean, and never blocks
// the queue when git state can't be read.
func (d *liveFactoryDeps) CheckpointResidue(f factory.Feature) (bool, error) {
	clean, err := gitx.IsCleanTree(d.dir)
	if err != nil || clean {
		return false, nil
	}
	msg := fmt.Sprintf("wip(orq-lite): checkpoint residue from interrupted feature %s (%s) — not gate-passed", f.ID, f.Title)
	if _, err := gitx.CommitAll(d.dir, msg); err != nil {
		return false, err
	}
	return true, nil
}

func (d *liveFactoryDeps) CheckoutBase(base string) error {
	return gitx.Checkout(d.dir, base)
}

func (d *liveFactoryDeps) SaveState(q *factory.Queue) error {
	return factory.Save(d.dir, q)
}

func (d *liveFactoryDeps) Logf(format string, args ...any) {
	fmt.Fprintf(d.out, format+"\n", args...)
}

// RunFeature plans the feature and drives the review loop on the current
// (feature) branch. Each feature persists its decomposition in
// .orquestalite/tasks-<ID>.json; the canonical .orquestalite/tasks.json is a
// scratch copy of the feature currently running. When reusePlan is true the
// persisted per-feature list is reused (skipping the plan) so already-completed
// tasks are not redone; otherwise the feature is decomposed fresh and the result
// is saved as its per-feature file. After the run the canonical tasks.json is
// synced back to the per-feature file so its final task states persist.
func (d *liveFactoryDeps) RunFeature(ctx context.Context, f factory.Feature, reusePlan bool) (factory.Summary, error) {
	start := time.Now().UTC()
	if f.StartedAt != nil {
		start = *f.StartedAt
	}

	canonical := filepath.Join(d.dir, ".orquestalite", "tasks.json")
	featureTasks := factory.TasksFilePath(d.dir, f.ID)

	if reusePlan && fileExists(featureTasks) {
		if err := copyFileAtomic(featureTasks, canonical); err != nil {
			return factory.Summary{}, err
		}
		d.Logf("factory: %s reusing task list from %s (skipping plan)", f.ID, filepath.Base(featureTasks))
	} else {
		planPath := filepath.Join(d.dir, ".orquestalite", "factory-plan-"+f.ID+".md")
		if err := os.WriteFile(planPath, []byte(f.Plan), 0o644); err != nil {
			return factory.Summary{}, err
		}
		if err := PlanWithLiveCaller(ctx, d.dir, planPath, false); err != nil {
			return factory.Summary{}, fmt.Errorf("plan: %w", err)
		}
		// Persist the fresh decomposition as this feature's own task list.
		if err := copyFileAtomic(canonical, featureTasks); err != nil {
			return factory.Summary{}, err
		}
	}

	runErr := Run(ctx, RunOptions{
		ProjectDir: d.dir,
		TeamPath:   filepath.Join(d.dir, "team.json"),
		LogFormat:  d.logFormat,
	})

	// Sync the final task states back to the per-feature file so a later resume
	// reflects what actually completed. Best-effort: a sync failure must not mask
	// the run outcome.
	if err := copyFileAtomic(canonical, featureTasks); err != nil {
		d.Logf("factory: %s warning: could not persist task states to %s: %v", f.ID, filepath.Base(featureTasks), err)
	}

	sum := d.summarizeTasks()
	sum.CostUSD = d.featureSpend(ctx, start)
	return sum, runErr
}

// featureSpend prices the agent sessions this feature consumed (runs logged
// at or after the feature's start) via agtop. Cost tracking is best-effort:
// a missing agtop or a pricing failure yields 0, never an error.
func (d *liveFactoryDeps) featureSpend(ctx context.Context, since time.Time) float64 {
	runs, err := cost.RunsFromLog(filepath.Join(d.dir, ".orquestalite", "run.log"))
	if err != nil || len(runs) == 0 {
		return 0
	}
	sessions, err := cost.Collect(ctx)
	if err != nil {
		d.Logf("factory: cost tracking skipped: %v", err)
		return 0
	}
	return cost.SpendSince(runs, sessions, since)
}

// PublishFeature pushes the feature branch and opens a PR via the gh CLI.
// Disabled unless the factory was started with --pr.
func (d *liveFactoryDeps) PublishFeature(ctx context.Context, f factory.Feature, base string) (string, error) {
	if !d.createPR {
		return "", nil
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return "", errors.New("gh CLI not found on PATH")
	}

	push := exec.CommandContext(ctx, "git", "push", "-u", "origin", f.Branch)
	push.Dir = d.dir
	if out, err := push.CombinedOutput(); err != nil {
		return "", fmt.Errorf("push %s: %w\n%s", f.Branch, err, out)
	}

	body := fmt.Sprintf("## %s\n\n%s\n\n---\n%d task(s) completed, %d failed.\n\nCreated by orquestalite factory.\n",
		f.Title, f.Plan, f.TasksDone, f.TasksFailed)
	pr := exec.CommandContext(ctx, "gh", "pr", "create",
		"--base", base,
		"--head", f.Branch,
		"--title", fmt.Sprintf("feat(%s): %s", f.ID, f.Title),
		"--body", body,
	)
	pr.Dir = d.dir
	out, err := pr.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh pr create: %w\n%s", err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// fileExists reports whether path is a readable existing file.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// copyFileAtomic copies src to dst by writing a sibling temp file and renaming
// it into place, so a crash mid-copy never leaves a half-written dst. Both paths
// are expected to live in the same directory (.orquestalite), keeping the rename
// atomic on the same filesystem.
func copyFileAtomic(src, dst string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func (d *liveFactoryDeps) summarizeTasks() factory.Summary {
	tl, err := tasks.Load(filepath.Join(d.dir, ".orquestalite", "tasks.json"))
	if err != nil {
		return factory.Summary{}
	}
	var sum factory.Summary
	for _, t := range tl.Tasks {
		switch t.Status {
		case tasks.StatusDone:
			sum.TasksDone++
		case tasks.StatusFailed, tasks.StatusNeedsHuman:
			sum.TasksFailed++
		default:
			sum.TasksOther++
		}
	}
	return sum
}
