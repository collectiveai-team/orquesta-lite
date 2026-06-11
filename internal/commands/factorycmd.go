package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/eventlog"
	"github.com/lionelchamorro/orquestalite/internal/factory"
	"github.com/lionelchamorro/orquestalite/internal/gitx"
	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

// FactoryOptions holds the parameters for the factory command.
type FactoryOptions struct {
	ProjectDir   string
	FeaturesPath string // path to the features markdown; "" resumes the existing queue
	Force        bool   // replace an existing unfinished queue
	StatusOnly   bool   // print the queue and exit
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

	q, err := loadOrCreateQueue(opts)
	if err != nil {
		return err
	}

	deps := &liveFactoryDeps{dir: opts.ProjectDir, logFormat: opts.LogFormat, out: opts.Out}
	return factory.Run(ctx, q, deps)
}

func loadOrCreateQueue(opts FactoryOptions) (*factory.Queue, error) {
	existing, loadErr := factory.Load(opts.ProjectDir)
	if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
		return nil, loadErr
	}

	if opts.FeaturesPath == "" {
		if existing == nil {
			return nil, errors.New("no factory queue found: run `orq-lite factory <features.md>` first")
		}
		return existing, nil
	}

	if existing != nil && existing.NextRunnable() != nil && !opts.Force {
		return nil, errors.New("an unfinished factory queue exists: resume it with `orq-lite factory` (no args) or replace it with --force")
	}

	raw, err := os.ReadFile(opts.FeaturesPath)
	if err != nil {
		return nil, fmt.Errorf("read features: %w", err)
	}
	feats := factory.ParseFeatures(string(raw))
	if len(feats) == 0 {
		return nil, fmt.Errorf("no features found in %s", opts.FeaturesPath)
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

func factoryStatus(dir string, out io.Writer) error {
	q, err := factory.Load(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(out, "no factory queue")
			return nil
		}
		return err
	}
	fmt.Fprintf(out, "base branch: %s   %s\n", q.BaseBranch, factory.Counts(q))
	fmt.Fprintf(out, "%-5s %-12s %-32s %5s %6s  %s\n", "ID", "STATUS", "BRANCH", "DONE", "FAILED", "TITLE")
	for _, f := range q.Features {
		fmt.Fprintf(out, "%-5s %-12s %-32s %5d %6d  %s\n",
			f.ID, f.Status, f.Branch, f.TasksDone, f.TasksFailed, f.Title)
	}
	return nil
}

// liveFactoryDeps implements factory.Deps with real git and live agent runs.
type liveFactoryDeps struct {
	dir       string
	logFormat eventlog.Format
	out       io.Writer
}

func (d *liveFactoryDeps) CheckoutFeatureBranch(branch, base string) error {
	if gitx.BranchExists(d.dir, branch) {
		return gitx.Checkout(d.dir, branch)
	}
	return gitx.CheckoutNewBranch(d.dir, branch, base)
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
// (feature) branch. Each feature starts from a fresh tasks.json; the previous
// one is archived under .orquestalite/archive/.
func (d *liveFactoryDeps) RunFeature(ctx context.Context, f factory.Feature) (factory.Summary, error) {
	if err := d.archiveStaleTasks(); err != nil {
		return factory.Summary{}, err
	}

	planPath := filepath.Join(d.dir, ".orquestalite", "factory-plan-"+f.ID+".md")
	if err := os.WriteFile(planPath, []byte(f.Plan), 0o644); err != nil {
		return factory.Summary{}, err
	}

	if err := PlanWithLiveCaller(ctx, d.dir, planPath, false); err != nil {
		return factory.Summary{}, fmt.Errorf("plan: %w", err)
	}

	runErr := Run(ctx, RunOptions{
		ProjectDir: d.dir,
		TeamPath:   filepath.Join(d.dir, "team.json"),
		LogFormat:  d.logFormat,
	})

	sum := d.summarizeTasks()
	return sum, runErr
}

func (d *liveFactoryDeps) archiveStaleTasks() error {
	tasksPath := filepath.Join(d.dir, ".orquestalite", "tasks.json")
	if _, err := os.Stat(tasksPath); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	archiveDir := filepath.Join(d.dir, ".orquestalite", "archive")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(archiveDir, fmt.Sprintf("tasks-%s.json", time.Now().UTC().Format("20060102-150405")))
	return os.Rename(tasksPath, dst)
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
