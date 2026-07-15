package builtin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/activity"
)

type CommandRunner interface {
	Run(context.Context, string, []string) (stdout, stderr []byte, exitCode int, err error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, dir string, argv []string) ([]byte, []byte, int, error) {
	if len(argv) == 0 {
		return nil, nil, -1, fmt.Errorf("empty argv")
	}
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Dir = dir
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	exitCode := 0
	if command.ProcessState != nil {
		exitCode = command.ProcessState.ExitCode()
	}
	return stdout.Bytes(), stderr.Bytes(), exitCode, err
}

type CommandExecutor struct {
	Runner     CommandRunner
	AllowShell bool
	DefaultDir string
}

func (c *CommandExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "command.run", Version: "1", Effect: activity.EffectAtMostOnce}
}

type commandInput struct {
	Argv  []string `json:"argv,omitempty"`
	Shell string   `json:"shell,omitempty"`
	Dir   string   `json:"dir,omitempty"`
}
type commandOutput struct {
	ExitCode   int    `json:"exitCode"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	DurationMS int64  `json:"durationMs"`
}

func (c *CommandExecutor) execute(ctx context.Context, request activity.Request) (commandOutput, error) {
	var input commandInput
	if err := strictJSON(request.Inputs, &input); err != nil {
		return commandOutput{}, contractError("command.run input", err)
	}
	if (len(input.Argv) == 0) == (input.Shell == "") {
		return commandOutput{}, contractError("command.run input", fmt.Errorf("set exactly one of argv or shell"))
	}
	argv := input.Argv
	if input.Shell != "" {
		if !c.AllowShell {
			return commandOutput{}, &activity.Error{Class: activity.ErrorPermissionDenied, Op: "command.run", Err: fmt.Errorf("shell capability is disabled")}
		}
		argv = []string{"sh", "-c", input.Shell}
	}
	runner := c.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	started := time.Now()
	dir := input.Dir
	if dir == "" {
		dir = c.DefaultDir
	}
	stdout, stderr, exitCode, err := runner.Run(ctx, dir, argv)
	output := commandOutput{ExitCode: exitCode, Stdout: string(stdout), Stderr: string(stderr), DurationMS: time.Since(started).Milliseconds()}
	if err != nil {
		return output, &activity.Error{Class: activity.ErrorPermanent, Op: "command.run", Err: fmt.Errorf("exit %d: %w", exitCode, err)}
	}
	return output, nil
}

func (c *CommandExecutor) Execute(ctx context.Context, request activity.Request) (activity.Result, error) {
	output, err := c.execute(ctx, request)
	raw, _ := json.Marshal(output)
	if err != nil {
		return activity.Result{Output: raw}, err
	}
	return activity.Result{Output: raw}, nil
}

type GateExecutor struct {
	Command      *CommandExecutor
	ArtifactRoot string
}

func (g *GateExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "gate.run", Version: "1", Effect: activity.EffectPure}
}
func (g *GateExecutor) Execute(ctx context.Context, request activity.Request) (activity.Result, error) {
	command := g.Command
	if command == nil {
		command = &CommandExecutor{}
	}
	output, err := command.execute(ctx, request)
	stdoutArtifact, stdoutMeta, captureErr := captureCommandArtifact(g.ArtifactRoot, request, "stdout.log", []byte(output.Stdout))
	if captureErr != nil {
		return activity.Result{}, captureErr
	}
	stderrArtifact, stderrMeta, captureErr := captureCommandArtifact(g.ArtifactRoot, request, "stderr.log", []byte(output.Stderr))
	if captureErr != nil {
		return activity.Result{}, captureErr
	}
	artifacts := make([]activity.Artifact, 0, 2)
	if stdoutMeta != nil {
		artifacts = append(artifacts, *stdoutMeta)
	}
	if stderrMeta != nil {
		artifacts = append(artifacts, *stderrMeta)
	}
	raw, _ := json.Marshal(map[string]any{"passed": err == nil, "exit_code": output.ExitCode, "stdout_artifact": stdoutArtifact, "stderr_artifact": stderrArtifact, "duration_ms": output.DurationMS})
	result := activity.Result{Output: raw, Artifacts: artifacts}
	if err != nil {
		return result, &activity.Error{Class: activity.ErrorGateFailed, Op: "gate.run", Err: err}
	}
	return result, nil
}

func captureCommandArtifact(root string, request activity.Request, name string, raw []byte) (string, *activity.Artifact, error) {
	if root == "" || len(raw) == 0 {
		return "", nil, nil
	}
	dir := filepath.Join(root, "gates", request.ScopePath, request.StepID)
	if request.ForeachKey != "" {
		dir = filepath.Join(dir, safeArtifact.ReplaceAllString(request.ForeachKey, "-"))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return "", nil, err
	}
	digestBytes := sha256.Sum256(raw)
	artifact := &activity.Artifact{Kind: "gate_" + strings.TrimSuffix(name, filepath.Ext(name)), URI: filepath.ToSlash(path), Digest: hex.EncodeToString(digestBytes[:])}
	return artifact.URI, artifact, nil
}
