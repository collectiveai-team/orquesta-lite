package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lionelchamorro/orquestalite/internal/eventlog"
	"github.com/lionelchamorro/orquestalite/internal/results"
	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

// PlanWithLiveCaller is a convenience wrapper that wires up the real subprocess
// agent machinery (same as Run does) and delegates to Plan. The team.json is
// looked up at <projectDir>/team.json.
func PlanWithLiveCaller(ctx context.Context, projectDir, planPath string, appendMode bool) error {
	teamPath := filepath.Join(projectDir, "team.json")
	deps, cleanup, err := newLiveDeps(liveDepsOptions{
		ProjectDir: projectDir,
		TeamPath:   teamPath,
		Roles:      []string{"parser"},
	})
	if err != nil {
		return err
	}
	defer cleanup()

	if err := Plan(ctx, projectDir, planPath, appendMode, deps); err != nil {
		return err
	}
	tasksPath := filepath.Join(projectDir, ".orquestalite", "tasks.json")
	if tl, err := tasks.Load(tasksPath); err == nil {
		deps.log.Log(eventlog.Event{Type: "plan_written", Fields: map[string]any{
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
			Squad:       p.Squad,
		})
	}
	tl.Append(converted, 0)

	return tasks.Save(tasksPath, tl)
}
