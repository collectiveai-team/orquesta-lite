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
	failed := 0
	for _, c := range checks {
		fmt.Fprintf(out, "[%s] %-22s %s\n", label[c.Status], c.Name, c.Detail)
		if c.Status == doctor.StatusError {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("doctor: %d check(s) failed", failed)
	}
	fmt.Fprintln(out, "\nall checks passed")
	return nil
}
