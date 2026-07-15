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

func CheckV2RunStart(projectDir string, forceNew bool) error {
	list, err := tasks.Load(filepath.Join(projectDir, "tasks.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, task := range list.Tasks {
		if task.Status != tasks.StatusDone && task.Status != tasks.StatusDecomposed {
			if forceNew {
				return nil
			}
			return fmt.Errorf("unfinished legacy task state exists; finish it with --engine=legacy or pass --force-new-run without deleting tasks.json")
		}
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
	queue, err := factory.Load(projectDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, feature := range queue.Features {
		if feature.Status != factory.StatusDone {
			if forceNew {
				return nil
			}
			return fmt.Errorf("unfinished legacy factory state exists; finish it with --engine=legacy or pass --force-new-run without deleting factory.json")
		}
	}
	return nil
}

func ValidateEngine(value string) error {
	if value != "legacy" && value != "v2" {
		return fmt.Errorf("engine must be legacy or v2")
	}
	return nil
}
