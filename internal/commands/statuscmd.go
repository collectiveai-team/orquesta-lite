package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/workflow"
)

func Status(projectDir string, out io.Writer) error {
	return printStatus(context.Background(), projectDir, out)
}

// StatusWatch refreshes the durable workflow table until ctx is cancelled.
func StatusWatch(ctx context.Context, projectDir string, out io.Writer, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Second
	}
	render := func() error {
		fmt.Fprint(out, "\x1b[H\x1b[2J")
		if err := printStatus(ctx, projectDir, out); err != nil {
			return err
		}
		fmt.Fprintf(out, "\nrefreshing every %s — Ctrl+C to stop\n", interval)
		return nil
	}
	if err := render(); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := render(); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
}

func printStatus(ctx context.Context, projectDir string, out io.Writer) error {
	path := filepath.Join(projectDir, ".orquestalite", "workflows.db")
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(out, "no workflow runs")
			return nil
		}
		return err
	}
	store, err := workflow.Open(path)
	if err != nil {
		return err
	}
	defer store.Close()
	runs, err := store.ListRuns(ctx, 50)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		fmt.Fprintln(out, "no workflow runs")
		return nil
	}
	fmt.Fprintf(out, "%-28s  %-13s  %-38s  %-20s  %s\n", "RUN", "STATUS", "FLOW", "UPDATED", "ERROR")
	for _, run := range runs {
		fmt.Fprintf(out, "%-28s  %-13s  %-38s  %-20s  %s\n", run.ID, run.Status, run.FlowRef, run.UpdatedAt.Local().Format("2006-01-02 15:04:05"), run.Error)
	}
	return nil
}
