package commands

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/activity/builtin"
	"github.com/collectiveai-team/orquesta-lite/internal/checks"
	"github.com/collectiveai-team/orquesta-lite/internal/runid"
	"github.com/collectiveai-team/orquesta-lite/internal/telemetry"
)

// ChecksCLI never executes a detected script until the reviewed profile has
// been explicitly confirmed. Detection output is a proposal, not approval.
func ChecksCLI(ctx context.Context, project string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: orq-lite checks detect|confirm <proposal.json>|status|run [--event pr|merge|release --max-tier N --base REF]")
	}
	switch args[0] {
	case "detect":
		if len(args) != 1 {
			return fmt.Errorf("usage: orq-lite checks detect (redirect proposal JSON to a file)")
		}
		profile, err := checks.Detect(project)
		if err != nil {
			return err
		}
		return writePrettyJSON(out, profile)
	case "confirm":
		if len(args) != 2 {
			return fmt.Errorf("usage: orq-lite checks confirm <reviewed-proposal.json>")
		}
		raw, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		profile, err := checks.Decode(raw)
		if err != nil {
			return err
		}
		hash, err := checks.ManifestHash(project)
		if err != nil {
			return err
		}
		if profile.ManifestsHash != hash {
			return fmt.Errorf("proposal is stale: manifests changed; detect and review again")
		}
		destination := filepath.Join(project, checks.DefaultPath)
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(destination), "checks-confirm-*.json")
		if err != nil {
			return err
		}
		name := tmp.Name()
		defer os.Remove(name)
		if _, err = tmp.Write(raw); err != nil {
			tmp.Close()
			return err
		}
		if err = tmp.Close(); err != nil {
			return err
		}
		if err = os.Rename(name, destination); err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "confirmed %s source_hash=%s manifests_hash=%s\n", checks.DefaultPath, checks.Hash(raw), hash)
		return err
	case "status":
		resolved, err := checks.Resolve(project, checks.DefaultPath)
		if err != nil {
			return err
		}
		return writePrettyJSON(out, resolved)
	case "run":
		fs := flag.NewFlagSet("checks run", flag.ContinueOnError)
		fs.SetOutput(out)
		event := fs.String("event", "pr", "pr|merge|release")
		tier := fs.Int("max-tier", 1, "maximum default PR tier")
		base := fs.String("base", "", "base commit/ref")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("checks run: unexpected positional arguments")
		}
		profile, err := checks.Resolve(project, checks.DefaultPath)
		if err != nil {
			return err
		}
		id := runid.New()
		root := filepath.Join(project, ".orquestalite", "runs", id)
		input, _ := json.Marshal(map[string]any{"event": *event, "max_tier": *tier, "base": *base})
		executor := &builtin.ChecksExecutor{ProjectDir: project, ArtifactRoot: root, Profile: profile}
		result, execErr := executor.Execute(ctx, activity.Request{RunID: id, StepID: "checks", Attempt: 1, Inputs: input})
		if len(result.Output) > 0 {
			if err := writePrettyJSON(out, json.RawMessage(result.Output)); err != nil {
				return err
			}
		}
		return execErr
	default:
		return fmt.Errorf("unknown checks command %q", args[0])
	}
}

func CostRun(project, id string, out io.Writer) error {
	if !safeRunComponent(id) {
		return fmt.Errorf("invalid run ID")
	}
	calls, err := telemetry.Read(filepath.Join(project, ".orquestalite", "runs", id, "usage.jsonl"))
	if err != nil {
		return err
	}
	return writePrettyJSON(out, telemetry.Summarize(calls))
}

func safeRunComponent(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value && filepath.IsLocal(value)
}
