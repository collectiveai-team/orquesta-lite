package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

var developmentAliases = map[string]string{
	"plan":    "plan-tickets@1",
	"intake":  "issue-fix@1",
	"review":  "pr-review@1",
	"run":     "task-list@1",
	"factory": "factory-governed@2",
}

func RunDevelopmentAlias(ctx context.Context, projectDir, command string, inputs map[string]any, out io.Writer) error {
	flowName, ok := developmentAliases[command]
	if !ok {
		return fmt.Errorf("no development flow alias for %q", command)
	}
	args := []string{"run", "development/" + flowName}
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
