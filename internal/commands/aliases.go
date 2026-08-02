package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

var developmentAliases = map[string]string{
	"plan":    "plan-tickets",
	"intake":  "issue-fix",
	"review":  "pr-review",
	"run":     "task-list",
	"factory": "factory-governed",
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
