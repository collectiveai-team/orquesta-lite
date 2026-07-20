package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/lionelchamorro/orquestalite/internal/factory"
	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

var developmentAliases = map[string]string{
	"plan":    "plan-tickets",
	"intake":  "issue-fix",
	"review":  "pr-review",
	"run":     "task-list",
	"factory": "factory-governed",
}

// SelectDevelopmentEngine applies the strangler routing rule during the
// canary window. An implicit v2 default drains unfinished legacy state through
// the legacy runtime; an explicit v2 request fails unless forceNew is set.
func SelectDevelopmentEngine(projectDir, command, requested string, explicitlySet, forceNew bool, out io.Writer) (string, error) {
	if err := ValidateEngine(requested); err != nil {
		return "", err
	}
	if requested == "legacy" {
		return requested, nil
	}
	active, err := hasRelevantLegacyState(projectDir, command)
	if err != nil {
		return "", err
	}
	if !active || forceNew {
		return requested, nil
	}
	if explicitlySet {
		return "", fmt.Errorf("unfinished legacy %s state exists; finish it with --engine=legacy or explicitly force a separate v2 run where supported", command)
	}
	if out != nil {
		fmt.Fprintf(out, "warning: unfinished legacy %s state found; routing to --engine=legacy for this run\n", command)
	}
	return "legacy", nil
}

func hasRelevantLegacyState(projectDir, command string) (bool, error) {
	switch command {
	case "factory":
		return hasUnfinishedLegacyFactory(projectDir)
	case "plan", "run", "intake":
		return hasUnfinishedLegacyTasks(projectDir)
	default:
		return false, nil
	}
}

func hasUnfinishedLegacyTasks(projectDir string) (bool, error) {
	paths := []string{
		filepath.Join(projectDir, ".orquestalite", "tasks.json"),
		filepath.Join(projectDir, "tasks.json"), // pre-state-dir compatibility
	}
	for _, path := range paths {
		list, err := tasks.Load(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		for _, task := range list.Tasks {
			if task.Status != tasks.StatusDone && task.Status != tasks.StatusDecomposed {
				return true, nil
			}
		}
	}
	return false, nil
}

func hasUnfinishedLegacyFactory(projectDir string) (bool, error) {
	queue, err := factory.Load(projectDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, feature := range queue.Features {
		if feature.Status != factory.StatusDone {
			return true, nil
		}
	}
	return false, nil
}

func CheckV2RunStart(projectDir string, forceNew bool) error {
	active, err := hasUnfinishedLegacyTasks(projectDir)
	if err != nil {
		return err
	}
	if active && !forceNew {
		return fmt.Errorf("unfinished legacy task state exists; finish it with --engine=legacy or pass --force-new-run without deleting tasks.json")
	}
	return nil
}

func RunDevelopmentAlias(ctx context.Context, projectDir, command string, inputs map[string]any, out io.Writer) error {
	flowName, ok := developmentAliases[command]
	if !ok {
		return fmt.Errorf("no development flow alias for %q", command)
	}
	args := []string{"run", "development/" + flowName + "@1"}
	keys := make([]string, 0, len(inputs))
	for key := range inputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		raw, err := json.Marshal(inputs[key])
		if err != nil {
			return err
		}
		args = append(args, key+"="+string(raw))
	}
	return FlowCLI(ctx, projectDir, args, out)
}

func CheckV2FactoryStart(projectDir string, forceNew bool) error {
	active, err := hasUnfinishedLegacyFactory(projectDir)
	if err != nil {
		return err
	}
	if active && !forceNew {
		return fmt.Errorf("unfinished legacy factory state exists; finish it with --engine=legacy or pass --force-new-run without deleting factory.json")
	}
	return nil
}

func ValidateEngine(value string) error {
	if value != "legacy" && value != "v2" {
		return fmt.Errorf("engine must be legacy or v2")
	}
	return nil
}
