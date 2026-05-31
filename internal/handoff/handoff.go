package handoff

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

// Write renders a markdown handoff file for the failed task at
// <dir>/tasks/handoff-<task_id>.md. It returns the absolute path written.
// If dir/tasks does not exist, it is created.
func Write(dir string, t *tasks.Task) (string, error) {
	tasksDir := filepath.Join(dir, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		return "", fmt.Errorf("handoff: create tasks dir: %w", err)
	}

	outPath := filepath.Join(tasksDir, "handoff-"+t.ID+".md")
	content := render(t)
	if err := os.WriteFile(outPath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("handoff: write file: %w", err)
	}
	return outPath, nil
}

func render(t *tasks.Task) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Handoff — %s: %s\n\n", t.ID, t.Title)
	fmt.Fprintf(&b, "Generated: %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Cycle: %d\n", t.CreatedInReviewCycle)
	fmt.Fprintf(&b, "Attempts: %d\n", t.Attempts)
	fmt.Fprintf(&b, "\n## Original task\n\n%s\n", t.Description)

	fd := t.FailureDetails
	if fd == nil {
		fmt.Fprintf(&b, "\n## Why orq-lite gave up\n\n(no failure details captured)\n")
		fmt.Fprintf(&b, "\n## Suggested next steps\n\n- Inspect `.orquestalite/run.log` for the last agent run events.\n")
		fmt.Fprintf(&b, "- Rerun with `orq-lite run` after addressing the issue.\n")
		return b.String()
	}

	// Failure summary
	fmt.Fprintf(&b, "\n## Why orq-lite gave up\n\n")
	fmt.Fprintf(&b, "- Reason: `%s`\n", fd.Reason)
	fmt.Fprintf(&b, "- Config suspect: %s\n", yesNo(fd.ConfigSuspect))
	fmt.Fprintf(&b, "- Model suspect: %s\n", yesNo(fd.ModelSuspect))
	fmt.Fprintf(&b, "- Task suspect: %s\n", yesNo(fd.TaskSuspect))

	// Agent attempts table
	fmt.Fprintf(&b, "\n## Agent attempts\n\n")
	if len(fd.AgentChain) == 0 {
		fmt.Fprintf(&b, "(no agent run data captured)\n")
	} else {
		fmt.Fprintf(&b, "| # | Agent | Duration | Status |\n")
		fmt.Fprintf(&b, "|---|-------|----------|--------|\n")
		for i, ar := range fd.AgentChain {
			fmt.Fprintf(&b, "| %d | %s | %ds | %s |\n", i+1, ar.Agent, ar.Duration, ar.Status)
		}
	}

	// Last stderr tail
	fmt.Fprintf(&b, "\n## Last stderr tail\n\n```\n")
	if fd.LastStderrTail == "" {
		fmt.Fprintf(&b, "<none>\n")
	} else {
		fmt.Fprintf(&b, "%s\n", fd.LastStderrTail)
	}
	fmt.Fprintf(&b, "```\n")

	// Suggested next steps
	fmt.Fprintf(&b, "\n## Suggested next steps\n\n")
	fmt.Fprintf(&b, "- If `config_suspect`: check `team.json` and CLI flags for primary coder; rerun with `orq-lite run`.\n")
	fmt.Fprintf(&b, "- If `model_suspect`: edit `team.json` to use a more capable agent in the coder/escalation ladder.\n")
	fmt.Fprintf(&b, "- If `task_suspect`: rewrite the task description in `.orquestalite/tasks.json` with more specific acceptance criteria, or split it manually.\n")

	// Reproduce manually
	fmt.Fprintf(&b, "\n## Reproduce manually\n\n")
	fmt.Fprintf(&b, "(Best-effort) the failing coder invocation was:\n\n```\n")
	fmt.Fprintf(&b, "(captured in .orquestalite/run.log)\n")
	fmt.Fprintf(&b, "```\n")

	return b.String()
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
