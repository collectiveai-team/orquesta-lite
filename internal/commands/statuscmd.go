package commands

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lionelchamorro/pyorquesta/internal/tasks"
)

func Status(projectDir string, w io.Writer) error {
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
