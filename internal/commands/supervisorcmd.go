package commands

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/supervision"
	"github.com/collectiveai-team/orquesta-lite/internal/workflow"
)

// SupervisorCLI acknowledges a real durable checkpoint before allowing the
// existing approval to resume. A justification and fresh context are required.
func SupervisorCLI(ctx context.Context, project string, args []string, out io.Writer) error {
	if len(args) < 2 || args[0] != "decide" {
		return fmt.Errorf("usage: orq-lite supervisor decide <run> --approval ID --task ID --role ROLE --action replan|delegate|continue --reason TEXT")
	}
	id := args[1]
	if !safeRunComponent(id) {
		return fmt.Errorf("invalid run ID")
	}
	fs := flag.NewFlagSet("supervisor decide", flag.ContinueOnError)
	fs.SetOutput(out)
	approval := fs.String("approval", "", "checkpoint approval ID")
	task := fs.String("task", "", "task ID from agent event")
	role := fs.String("role", "", "role")
	action := fs.String("action", "", "replan|delegate|continue")
	reason := fs.String("reason", "", "recorded justification")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if *approval == "" || fs.NArg() != 0 {
		return fmt.Errorf("checkpoint approval ID required")
	}
	store, err := workflow.Open(filepath.Join(project, ".orquestalite", "workflows.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	approvals, err := store.ListApprovals(ctx, id)
	if err != nil {
		return err
	}
	var checkpoint *workflow.Approval
	for _, a := range approvals {
		if a.ID == *approval && a.Status == "pending" {
			copy := a
			checkpoint = &copy
		}
	}
	if checkpoint == nil {
		return fmt.Errorf("pending checkpoint approval not found")
	}
	expectedTask, expectedRole, err := supervisorCheckpointIdentity(ctx, store, id, *checkpoint)
	if err != nil {
		return err
	}
	if *task != expectedTask || *role != expectedRole {
		return fmt.Errorf("checkpoint task/role mismatch; expected --task %s --role %s", expectedTask, expectedRole)
	}
	root := filepath.Join(project, ".orquestalite", "runs", id)
	if err := supervision.Acknowledge(root, *task, *role, *action, *reason); err != nil {
		return err
	}
	if err := store.DecideApproval(ctx, id, *approval, "approve"); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "recorded %s; next invocation uses fresh context; resume run %s\n", *action, id)
	return err
}

// supervisorCheckpointIdentity binds a decision to the exact agent step that
// requested it. Evidence, review and other approvals cannot reset counters.
func supervisorCheckpointIdentity(ctx context.Context, store *workflow.Store, runID string, a workflow.Approval) (string, string, error) {
	if a.RunID != runID || !strings.HasPrefix(a.Reason, supervision.ErrCheckpointRequired.Error()+": ") {
		return "", "", fmt.Errorf("approval is not a supervisor checkpoint")
	}
	steps, err := store.ListSteps(ctx, runID)
	if err != nil {
		return "", "", err
	}
	for _, step := range steps {
		if step.ID != a.StepRunID {
			continue
		}
		if activity.ExecutorRef(step.ActivityRef) != "agent.invoke@1" || step.Status != workflow.StepNeedsHuman {
			return "", "", fmt.Errorf("supervisor checkpoint must belong to a paused agent invocation")
		}
		var input struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(step.Inputs, &input); err != nil || strings.TrimSpace(input.Role) == "" {
			return "", "", fmt.Errorf("checkpoint agent role unavailable")
		}
		identity, _ := json.Marshal([]string{runID, step.ScopePath, step.ForeachKey, step.StepID})
		return fmt.Sprintf("%x", sha256.Sum256(identity)), input.Role, nil
	}
	return "", "", fmt.Errorf("checkpoint step not found")
}
