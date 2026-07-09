package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lionelchamorro/orquestalite/internal/eventlog"
	"github.com/lionelchamorro/orquestalite/internal/invoke"
	"github.com/lionelchamorro/orquestalite/internal/results"
)

// IntakeOptions holds the parameters for the intake command.
type IntakeOptions struct {
	ProjectDir string
	TeamPath   string
	// IssuePath is the file containing the raw GitHub issue body.
	IssuePath string
	// PlanPath is where the derived plan is written when the issue is
	// actionable. Defaults to "plan.md" in the project dir.
	PlanPath string
	// Run, when true (the default), triggers `orq-lite run` after planning.
	// Set false to stop after writing tasks.json (AFK-friendly dry check).
	Run bool
	// LogFormat controls the stdout pretty format for the run phase.
	LogFormat eventlog.Format
	Out       io.Writer
}

// IntakeCaller invokes the intake role over an issue body and returns its
// parsed verdict. The live implementation wires invoke.Role; tests inject a
// stub so the actionable→plan→run flow is exercised without a live agent.
type IntakeCaller interface {
	RunIntake(ctx context.Context, issue string) (*results.IntakeResult, error)
}

// Intake triages a GitHub issue, and when it is actionable writes the derived
// plan to plan.md, runs the parser to produce tasks.json, and triggers the run
// loop. An incomplete issue writes `missing_info` instead.
func Intake(ctx context.Context, opts IntakeOptions) (err error) {
	if opts.Run {
		// default true
	}
	deps, cleanup, err := newLiveDeps(liveDepsOptions{
		ProjectDir: opts.ProjectDir,
		TeamPath:   teamPathOrDefault(opts.TeamPath, opts.ProjectDir),
		LogFormat:  opts.LogFormat,
		Roles:      []string{"intake", "parser"},
		Command:    "intake",
	})
	if err != nil {
		return err
	}
	defer cleanup()
	defer markRunStatus(ctx, deps, &err)

	caller := &liveIntakeCaller{deps: deps}
	plan := func(ctx context.Context, projectDir, planPath string) error {
		return PlanWithLiveCaller(ctx, projectDir, planPath, false)
	}
	run := func(ctx context.Context, projectDir string) error {
		return Run(ctx, RunOptions{ProjectDir: projectDir, TeamPath: teamPathOrDefault(opts.TeamPath, projectDir), LogFormat: opts.LogFormat})
	}
	return IntakeWith(ctx, opts, caller, plan, run)
}

// IntakeWith is the testable core: callers inject the intake caller and the
// plan/run steps so the triage→plan→run flow is exercised without a live agent
// or a real run loop.
func IntakeWith(ctx context.Context, opts IntakeOptions, caller IntakeCaller, plan planFunc, run runFunc) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.IssuePath == "" {
		return errors.New("intake requires --issue <file>")
	}
	issue, err := os.ReadFile(opts.IssuePath)
	if err != nil {
		return fmt.Errorf("read issue: %w", err)
	}

	res, err := caller.RunIntake(ctx, string(issue))
	if err != nil {
		return fmt.Errorf("intake role: %w", err)
	}

	if !res.Actionable {
		return writeMissingInfo(opts.ProjectDir, res, opts.Out)
	}

	planPath := opts.PlanPath
	if planPath == "" {
		planPath = "plan.md"
	}
	if !filepath.IsAbs(planPath) {
		planPath = filepath.Join(opts.ProjectDir, planPath)
	}
	if err := os.WriteFile(planPath, []byte(res.Plan), 0o644); err != nil {
		return fmt.Errorf("write plan: %w", err)
	}
	fmt.Fprintf(opts.Out, "intake: actionable — plan written to %s\n", planPath)

	if err := plan(ctx, opts.ProjectDir, planPath); err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	if opts.Run {
		fmt.Fprintf(opts.Out, "intake: running task loop\n")
		if err := run(ctx, opts.ProjectDir); err != nil {
			return fmt.Errorf("run: %w", err)
		}
	}
	return nil
}

// writeMissingInfo persists the intake's `missing_info` to
// `.orquestalite/intake-missing-info.md` so it can be commented back on the
// issue, and surfaces it on stdout.
func writeMissingInfo(projectDir string, res *results.IntakeResult, out io.Writer) error {
	dir := filepath.Join(projectDir, ".orquestalite")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "intake-missing-info.md")
	missing := strings.Join(res.MissingInfo, "\n")
	body := "# Missing information\n\nThe issue is not actionable yet. Please add:\n\n" + missing + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write missing_info: %w", err)
	}
	fmt.Fprintf(out, "intake: NOT actionable — missing_info written to %s\n", path)
	fmt.Fprintln(out, "----- missing_info -----")
	fmt.Fprintln(out, missing)
	return nil
}

type planFunc func(ctx context.Context, projectDir, planPath string) error
type runFunc func(ctx context.Context, projectDir string) error

// liveIntakeCaller runs the configured intake role over an issue body.
type liveIntakeCaller struct {
	deps *liveDeps
}

func (c *liveIntakeCaller) RunIntake(ctx context.Context, issue string) (*results.IntakeResult, error) {
	r, err := invoke.Role(ctx, c.deps.inv, "intake", invoke.RoleCall{
		Vars: map[string]string{"ISSUE": issue},
	}, invoke.RunContext{TaskID: "_intake", Attempt: 1}, results.ParseIntake)
	if err != nil {
		return nil, err
	}
	return r, nil
}
