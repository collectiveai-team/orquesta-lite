package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/lionelchamorro/orquestalite/internal/cost"
)

// Cost prints the per-task spend rollup: agent runs from run.log joined
// against agtop's per-session pricing.
func Cost(ctx context.Context, projectDir string, out io.Writer) error {
	runs, err := cost.RunsFromLog(filepath.Join(projectDir, ".orquestalite", "run.log"))
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		fmt.Fprintln(out, "no agent runs recorded yet")
		return nil
	}

	sessions, collectErr := cost.Collect(ctx)
	if collectErr != nil {
		if !errors.Is(collectErr, cost.ErrAgtopUnavailable) {
			fmt.Fprintf(out, "warning: agtop pricing unavailable: %v\n\n", collectErr)
		}
		sessions = nil
	}

	rep := cost.Rollup(runs, sessions)
	fmt.Fprintf(out, "%-8s %5s %7s %12s %12s %10s\n", "TASK", "RUNS", "PRICED", "INPUT_TOK", "OUTPUT_TOK", "COST_USD")
	for _, t := range rep.Tasks {
		fmt.Fprintf(out, "%-8s %5d %7d %12d %12d %10.4f\n",
			t.TaskID, t.Runs, t.Priced, t.InputTok, t.OutputTok, t.TotalUSD)
	}
	fmt.Fprintf(out, "%-8s %5d %7d %25s %10.4f\n", "TOTAL", rep.Runs, rep.Priced, "", rep.TotalUSD)
	if errors.Is(collectErr, cost.ErrAgtopUnavailable) {
		fmt.Fprintln(out, "\nagtop not found; showing first-party tokens and embedded price estimates where available. Install agtop or update embedded prices for fuller USD coverage.")
	}
	if rep.Priced < rep.Runs {
		fmt.Fprintf(out, "\n%d run(s) could not be priced (session expired from the agent CLI's local logs, client unsupported by agtop, or no embedded price matched the model).\n", rep.Runs-rep.Priced)
	}
	return nil
}
