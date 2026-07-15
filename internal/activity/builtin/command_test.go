package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/activity"
)

type fakeCommand struct {
	exit int
	err  error
}

func (f fakeCommand) Run(context.Context, string, []string) ([]byte, []byte, int, error) {
	return []byte("out"), []byte("err"), f.exit, f.err
}

func commandRequest(t *testing.T) activity.Request {
	t.Helper()
	raw, _ := json.Marshal(commandInput{Argv: []string{"tool", "arg"}})
	return activity.Request{Inputs: raw}
}

func TestCommandAndGateSemantics(t *testing.T) {
	command := &CommandExecutor{Runner: fakeCommand{}}
	result, err := command.Execute(context.Background(), commandRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Output) == "" {
		t.Fatal("missing output")
	}
	gate := &GateExecutor{Command: &CommandExecutor{Runner: fakeCommand{exit: 1, err: errors.New("failed")}}, ArtifactRoot: t.TempDir()}
	result, err = gate.Execute(context.Background(), commandRequest(t))
	var output map[string]any
	_ = json.Unmarshal(result.Output, &output)
	if activity.Classify(err) != activity.ErrorGateFailed || output["passed"] != false || len(result.Artifacts) != 2 || output["stdout_artifact"] == "" {
		t.Fatalf("result=%s err=%v", result.Output, err)
	}
}

func TestShellRequiresCapability(t *testing.T) {
	raw, _ := json.Marshal(commandInput{Shell: "echo x"})
	_, err := (&CommandExecutor{}).Execute(context.Background(), activity.Request{Inputs: raw})
	if activity.Classify(err) != activity.ErrorPermissionDenied {
		t.Fatalf("err=%v", err)
	}
}
