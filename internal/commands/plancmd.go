package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/config"
	"github.com/lionelchamorro/orquestalite/internal/eventlog"
	"github.com/lionelchamorro/orquestalite/internal/fallback"
	"github.com/lionelchamorro/orquestalite/internal/results"
	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

// PlanWithLiveCaller is a convenience wrapper that wires up the real subprocess
// agent machinery (same as Run does) and delegates to Plan. The team.json is
// looked up at <projectDir>/team.json.
func PlanWithLiveCaller(ctx context.Context, projectDir, planPath string, appendMode bool) error {
	teamPath := filepath.Join(projectDir, "team.json")
	cfg, err := config.Load(teamPath)
	if err != nil {
		return err
	}

	logPath := filepath.Join(projectDir, ".orquestalite", "run.log")
	logger, err := eventlog.Open(logPath, os.Stdout)
	if err != nil {
		return err
	}
	defer logger.Close()

	memPath := filepath.Join(projectDir, ".orquestalite", "memory.md")
	tasksPath := filepath.Join(projectDir, ".orquestalite", "tasks.json")

	fc := fallback.NewCaller(fallback.Config{
		InitialBackoff: time.Duration(cfg.RateLimitBackoff.InitialSeconds) * time.Second,
		Factor:         cfg.RateLimitBackoff.Factor,
		MaxBackoff:     time.Duration(cfg.RateLimitBackoff.MaxSeconds) * time.Second,
	})

	deps := &liveDeps{
		cfg:       cfg,
		dir:       projectDir,
		fc:        fc,
		log:       logger,
		tl:        &tasks.TaskList{},
		memPath:   memPath,
		tasksPath: tasksPath,
	}
	deps.inv, err = newLiveRoleInvoker(cfg, projectDir, memPath, fc, logger, nil, nil)
	if err != nil {
		return err
	}

	if err := Plan(ctx, projectDir, planPath, appendMode, deps); err != nil {
		return err
	}
	if tl, err := tasks.Load(tasksPath); err == nil {
		logger.Log(eventlog.Event{Type: "plan_written", Fields: map[string]any{
			"tasks_count": len(tl.Tasks),
			"path":        tasksPath,
			"append":      appendMode,
		}})
	}
	return nil
}

// ParserCaller is the interface for invoking the parser role.
type ParserCaller interface {
	RunParser(ctx context.Context, plan string) (*results.ParserResult, error)
}

// Plan reads the plan file at planPath, calls the parser via caller, converts
// the resulting tasks, and writes them to <projectDir>/.orquestalite/tasks.json.
// When appendMode is true, any existing tasks.json is loaded first so that new
// tasks are appended rather than overwriting the list.
func Plan(ctx context.Context, projectDir, planPath string, appendMode bool, caller ParserCaller) error {
	planRaw, err := os.ReadFile(planPath)
	if err != nil {
		return fmt.Errorf("read plan: %w", err)
	}

	parsed, err := caller.RunParser(ctx, string(planRaw))
	if err != nil {
		return fmt.Errorf("parser: %w", err)
	}

	tasksPath := filepath.Join(projectDir, ".orquestalite", "tasks.json")

	var tl *tasks.TaskList
	if appendMode {
		existing, loadErr := tasks.Load(tasksPath)
		if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
			return loadErr
		}
		if existing == nil {
			tl = &tasks.TaskList{}
		} else {
			tl = existing
		}
	} else {
		tl = &tasks.TaskList{}
	}

	converted := make([]tasks.Task, 0, len(parsed.Tasks))
	for _, p := range parsed.Tasks {
		converted = append(converted, tasks.Task{
			Title:       p.Title,
			Description: p.Description,
			Priority:    p.Priority,
		})
	}
	tl.Append(converted, 0)

	return tasks.Save(tasksPath, tl)
}
