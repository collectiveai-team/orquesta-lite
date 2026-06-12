package commands

import (
	"context"
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

	sessions, err := cost.Collect(ctx)
	if err != nil {
		return err
	}

	rep := cost.Rollup(runs, sessions)
	fmt.Fprintf(out, "%-8s %5s %7s %12s %12s %10s\n", "TASK", "RUNS", "PRICED", "INPUT_TOK", "OUTPUT_TOK", "COST_USD")
	for _, t := range rep.Tasks {
		fmt.Fprintf(out, "%-8s %5d %7d %12d %12d %10.4f\n",
			t.TaskID, t.Runs, t.Priced, t.InputTok, t.OutputTok, t.TotalUSD)
	}
	fmt.Fprintf(out, "%-8s %5d %7d %25s %10.4f\n", "TOTAL", rep.Runs, rep.Priced, "", rep.TotalUSD)
	if rep.Priced < rep.Runs {
		fmt.Fprintf(out, "\n%d run(s) could not be priced (session expired from the agent CLI's local logs or client unsupported by agtop).\n", rep.Runs-rep.Priced)
	}
	return nil
}
