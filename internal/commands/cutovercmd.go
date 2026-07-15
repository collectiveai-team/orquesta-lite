package commands

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/lionelchamorro/orquestalite/internal/cutover"
)

func CutoverCLI(ctx context.Context, projectDir string, args []string, out io.Writer) error {
	command := "check"
	if len(args) > 0 {
		command = args[0]
		args = args[1:]
	}
	switch command {
	case "template":
		if len(args) != 0 {
			return fmt.Errorf("usage: orq-lite cutover template")
		}
		return writeCutoverJSON(out, cutover.Template())
	case "check":
		fs := flag.NewFlagSet("cutover check", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		evidencePath := fs.String("evidence", filepath.Join(projectDir, ".orquestalite", "cutover-evidence.json"), "cutover evidence path")
		expectedCommit := fs.String("commit", "", "deletion candidate git commit")
		jsonOutput := fs.Bool("json", false, "print machine-readable report")
		if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
			return fmt.Errorf("usage: orq-lite cutover check [--evidence path] --commit sha [--json]")
		}
		report, err := cutover.Check(ctx, cutover.Options{EvidencePath: *evidencePath, ExpectedCommit: *expectedCommit})
		if err != nil {
			return err
		}
		if *jsonOutput {
			if err = writeCutoverJSON(out, report); err != nil {
				return err
			}
		} else if err = writeCutoverReport(out, report); err != nil {
			return err
		}
		if !report.Open {
			return fmt.Errorf("%w: satisfy every reported gate before deleting compatibility packages", cutover.ErrGateClosed)
		}
		return nil
	default:
		return fmt.Errorf("usage: orq-lite cutover <check|template>")
	}
}

func writeCutoverReport(out io.Writer, report cutover.Report) error {
	for _, gate := range report.Gates {
		status := "FAIL"
		if gate.Passed {
			status = "PASS"
		}
		if _, err := fmt.Fprintf(out, "[%s] %s: %s\n", status, gate.Name, gate.Detail); err != nil {
			return err
		}
	}
	state := "CLOSED"
	if report.Open {
		state = "OPEN"
	}
	_, err := fmt.Fprintf(out, "cutover gate: %s\n", state)
	return err
}

func writeCutoverJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
