package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/lionelchamorro/pyorquesta/internal/tasks"
)

func Status(projectDir string, w io.Writer) error {
	return printStatus(projectDir, w)
}

// StatusWatch refreshes the status table on every tick of `interval` until ctx
// is canceled. The terminal is cleared between renders. Designed to be driven
// from a SIGINT-aware context in main.
func StatusWatch(ctx context.Context, projectDir string, w io.Writer, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Second
	}

	render := func() error {
		fmt.Fprint(w, "\x1b[H\x1b[2J")
		if err := printStatus(projectDir, w); err != nil {
			return err
		}
		fmt.Fprintf(w, "\nrefreshing every %s — Ctrl+C to stop\n", interval)
		return nil
	}

	if err := render(); err != nil {
		return err
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := render(); err != nil {
				return err
			}
		}
	}
}

func printStatus(projectDir string, w io.Writer) error {
	p := filepath.Join(projectDir, ".pyorquesta", "tasks.json")
	tl, err := tasks.Load(p)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(w, "no tasks (run `pyorquesta plan plan.md` first)")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "%-6s  %-8s  %-3s  %-6s  %-20s  %s\n", "ID", "STATUS", "PRI", "ATT", "REASON", "TITLE")
	for _, t := range tl.Tasks {
		reason := ""
		if t.FailureReason != nil {
			reason = string(*t.FailureReason)
		}
		fmt.Fprintf(w, "%-6s  %-8s  %-3d  %-6d  %-20s  %s\n", t.ID, t.Status, t.Priority, t.Attempts, reason, t.Title)
	}
	return nil
}
