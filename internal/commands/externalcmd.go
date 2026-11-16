package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/config"
	"github.com/collectiveai-team/orquesta-lite/internal/eventlog"
	"github.com/collectiveai-team/orquesta-lite/internal/external"
	"github.com/collectiveai-team/orquesta-lite/internal/flow"
	"github.com/collectiveai-team/orquesta-lite/internal/workflow"
)

// A missing external credential must not prevent a synchronous flow from
// running merely because the project also defines unused external adapters.
func referencedExternalSettings(ir *flow.IR, settings map[string]config.ExternalAdapter) map[string]config.ExternalAdapter {
	selected := map[string]config.ExternalAdapter{}
	var visit func(*flow.IR)
	inspect := func(spec *activity.Spec, with map[string]flow.Value) {
		if spec == nil || spec.Name != "external.dispatch" {
			return
		}
		value := with["adapter"]
		name, literal := value.Literal.(string)
		if !literal {
			for key, setting := range settings {
				selected[key] = setting
			}
		} else if setting, ok := settings[name]; ok {
			selected[name] = setting
		}
	}
	visit = func(current *flow.IR) {
		if current == nil {
			return
		}
		for _, step := range current.Steps {
			inspect(step.Activity, step.With)
			visit(step.Subflow)
			for _, handler := range []*flow.IRHandler{step.OnError, step.OnCancel, step.Compensate} {
				if handler != nil {
					inspect(handler.Activity, handler.With)
					visit(handler.Subflow)
				}
			}
		}
	}
	visit(ir)
	return selected
}

func externalAdapters(settings map[string]config.ExternalAdapter) (map[string]external.Adapter, error) {
	adapters := make(map[string]external.Adapter, len(settings))
	for name, setting := range settings {
		if setting.Type != "paperclip" {
			return nil, fmt.Errorf("external adapter %s has unsupported type %q", name, setting.Type)
		}
		token := ""
		if setting.TokenEnv != "" {
			var present bool
			token, present = os.LookupEnv(setting.TokenEnv)
			if !present || token == "" {
				return nil, fmt.Errorf("external adapter %s requires environment variable %s", name, setting.TokenEnv)
			}
		}
		adapter, err := external.NewPaperclip(external.PaperclipConfig{
			BaseURL: setting.BaseURL, CompanyID: setting.CompanyID,
			Token: token, Employees: setting.Employees,
		})
		if err != nil {
			// Connection validation does not include credential values.
			return nil, fmt.Errorf("external adapter %s: %w", name, err)
		}
		adapters[name] = adapter
	}
	return adapters, nil
}

func loadExternalAdapters(teamPath string) (map[string]external.Adapter, error) {
	raw, err := os.ReadFile(teamPath)
	if os.IsNotExist(err) {
		return map[string]external.Adapter{}, nil
	}
	if err != nil {
		return nil, err
	}
	var partial struct {
		Runtime config.Runtime `json:"runtime"`
	}
	if err := json.Unmarshal(raw, &partial); err != nil {
		return nil, fmt.Errorf("read external adapter configuration: %w", err)
	}
	return externalAdapters(partial.Runtime.ExternalAdapters)
}

func externalWorkflowDeps(ctx context.Context, projectDir, runID string) (*workflowDeps, error) {
	store, err := workflow.Open(filepath.Join(projectDir, ".orquestalite", "workflows.db"))
	if err != nil {
		return nil, err
	}
	run, err := store.GetRun(ctx, runID)
	store.Close()
	if err != nil {
		return nil, err
	}
	var ir flow.IR
	if err := json.Unmarshal(run.IR, &ir); err != nil {
		return nil, err
	}
	root := projectDir
	if ir.Pack != nil {
		root = findPinnedPack(projectDir, ir.Pack)
		if root == "" {
			return nil, fmt.Errorf("pinned pack %s@%s is missing or its digest changed", ir.Pack.Name, ir.Pack.Version)
		}
	}
	teamPath := filepath.Join(projectDir, "team.json")
	if err := validateConfigReferences(&ir, loadGateConfig(teamPath)); err != nil {
		return nil, err
	}
	compiled := &compiledWorkflow{IR: &ir, Catalog: flow.NewDirectoryCatalog(root, builtinSpecs()), Root: root}
	return newWorkflowDeps(projectDir, teamPath, runID, compiled, eventlog.Format("auto"))
}

func flowExternalCLI(ctx context.Context, projectDir string, args []string, out io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: orq-lite flow external <attempts|reconcile|watch|receive|retry> <run-id> [arguments]")
	}
	command, runID := args[0], args[1]
	if command == "attempts" {
		if len(args) != 2 {
			return fmt.Errorf("usage: orq-lite flow external attempts <run-id>")
		}
		store, err := workflow.Open(filepath.Join(projectDir, ".orquestalite", "workflows.db"))
		if err != nil {
			return err
		}
		defer store.Close()
		attempts, err := store.ListExternalAttempts(ctx, runID)
		if err != nil {
			return err
		}
		return writePrettyJSON(out, attempts)
	}
	switch command {
	case "reconcile", "watch":
		if len(args) != 2 {
			return fmt.Errorf("usage: orq-lite flow external %s <run-id>", command)
		}
	case "retry":
		if len(args) != 3 {
			return fmt.Errorf("usage: orq-lite flow external retry <run-id> <attempt-id>")
		}
	case "receive":
		if len(args) != 5 {
			return fmt.Errorf("usage: orq-lite flow external receive <run-id> <step-id> <attempt-id> <event-json-path>")
		}
		// The inbox is independent of adapter credentials, pinned packs and
		// executor availability. Reconciliation runs outside callback receipt.
		file, err := os.Open(args[4])
		if err != nil {
			return err
		}
		defer file.Close()
		raw, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
		if err != nil {
			return err
		}
		if len(raw) > 8<<20 {
			return fmt.Errorf("external event exceeds 8 MiB")
		}
		var event external.Event
		if err := json.Unmarshal(raw, &event); err != nil {
			return fmt.Errorf("decode external event: %w", err)
		}
		store, err := workflow.Open(filepath.Join(projectDir, ".orquestalite", "workflows.db"))
		if err != nil {
			return err
		}
		defer store.Close()
		pending, err := store.ReceiveExternalEvent(ctx, runID, args[2], args[3], event)
		if err != nil {
			return err
		}
		return writePrettyJSON(out, map[string]any{"received": true, "pending": pending, "runId": runID, "stepId": args[2], "attemptId": args[3]})
	default:
		return fmt.Errorf("unknown external flow command %q", command)
	}
	deps, err := externalWorkflowDeps(ctx, projectDir, runID)
	if err != nil {
		return err
	}
	defer deps.close(context.Background())
	if command == "retry" {
		return deps.runtime.RetryExternal(ctx, runID, args[2])
	}
	for {
		run, err := deps.runtime.ReconcileExternal(ctx, runID)
		if run != nil {
			fmt.Fprintf(out, "run_id=%s status=%s\n", run.ID, run.Status)
		}
		if err != nil || command != "watch" || run == nil || run.Status != workflow.RunWaitingExternal {
			return err
		}
		timer := time.NewTimer(30 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
