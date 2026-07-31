package commands

import (
	"context"
	"fmt"
	"io"

	"github.com/lionelchamorro/orquestalite/internal/doctor"
)

// Doctor preflights the whole setup — git state, team.json, prompts, agent
// CLIs, credentials, test command, optional tooling — before any money is
// spent on agent calls. Misconfiguration is the top source of wasted runs
// (see tasks/orq-lite-fastapi-sse-test-findings.md). Returns an error iff
// any error-level check fires. The checks themselves live in
// internal/doctor, shared verbatim with GET /api/doctor.
func Doctor(projectDir string, out io.Writer) error {
	checks := doctor.Run(context.Background(), projectDir)
	label := map[doctor.Status]string{
		doctor.StatusOK:    "PASS",
		doctor.StatusWarn:  "WARN",
		doctor.StatusError: "FAIL",
	}
	failed, warned := 0, 0
	for _, c := range checks {
		fmt.Fprintf(out, "[%s] %-22s %s\n", label[c.Status], c.Name, c.Detail)
		switch c.Status {
		case doctor.StatusError:
			failed++
		case doctor.StatusWarn:
			warned++
		}
	}
	if failed > 0 {
		return fmt.Errorf("doctor: %d check(s) failed", failed)
	}
	// Warnings are not failures — plenty of them are genuinely optional — but
	// the summary must not read as a clean bill of health when some of them
	// name a setting the very next command needs. "all checks passed" over two
	// warnings about unset gates is how a project reaches `flow run` believing
	// it is configured.
	if warned > 0 {
		fmt.Fprintf(out, "\nno failures, %d warning(s) — read them before your first run\n", warned)
		return nil
	}
	fmt.Fprintln(out, "\nall checks passed")
	return nil
}
