package process

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
)

const defaultMaxMessage = 8 << 20

type Executor struct {
	Manifest        Manifest
	Dir             string
	Env             []string
	MaxMessageBytes int64
	StderrRoot      string
}

func (e *Executor) Spec() activity.Spec { return e.Manifest.Spec }

func (e *Executor) Describe(ctx context.Context) (activity.Spec, error) {
	response, err := e.call(ctx, requestEnvelope{Protocol: ProtocolV1, Operation: OperationDescribe})
	if err != nil {
		return activity.Spec{}, err
	}
	if response.Spec == nil {
		return activity.Spec{}, protocolError("describe response has no spec")
	}
	return *response.Spec, nil
}

func (e *Executor) Execute(ctx context.Context, request activity.Request) (activity.Result, error) {
	response, err := e.call(ctx, requestEnvelope{Protocol: ProtocolV1, Operation: OperationExecute, Request: &request})
	if err != nil {
		return activity.Result{}, err
	}
	if response.Result == nil {
		return activity.Result{}, protocolError("execute response has no result")
	}
	return *response.Result, nil
}

func (e *Executor) Reconcile(ctx context.Context, request activity.ReconcileRequest) (activity.ReconcileResult, error) {
	response, err := e.call(ctx, requestEnvelope{Protocol: ProtocolV1, Operation: OperationReconcile, Reconcile: &request})
	if err != nil {
		return activity.ReconcileResult{}, err
	}
	if response.Reconcile == nil {
		return activity.ReconcileResult{}, protocolError("reconcile response has no result")
	}
	return *response.Reconcile, nil
}

func (e *Executor) Compensate(ctx context.Context, request activity.CompensateRequest) (activity.Result, error) {
	response, err := e.call(ctx, requestEnvelope{Protocol: ProtocolV1, Operation: OperationCompensate, Compensate: &request})
	if err != nil {
		return activity.Result{}, err
	}
	if response.Result == nil {
		return activity.Result{}, protocolError("compensate response has no result")
	}
	return *response.Result, nil
}

func (e *Executor) call(ctx context.Context, envelope requestEnvelope) (*responseEnvelope, error) {
	if len(e.Manifest.Command) == 0 {
		return nil, protocolError("empty command")
	}
	raw, err := encodeRequest(envelope)
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, e.Manifest.Command[0], e.Manifest.Command[1:]...)
	command.Dir = e.Dir
	command.Env = append(os.Environ(), e.Env...)
	command.Stdin = bytes.NewReader(append(raw, '\n'))
	var stdout, stderr bytes.Buffer
	command.Stdout = &limitedWriter{Writer: &stdout, N: e.messageLimit()}
	command.Stderr = &limitedWriter{Writer: &stderr, N: e.messageLimit()}
	if err := command.Run(); err != nil {
		_, _ = e.captureStderr(envelope, stderr.Bytes())
		return nil, &activity.Error{Class: activity.ErrorPermanent, Op: e.Manifest.Spec.Ref(), Err: fmt.Errorf("process: %w; stderr: %s", err, stderr.String())}
	}
	decoder := json.NewDecoder(io.LimitReader(bytes.NewReader(stdout.Bytes()), e.messageLimit()))
	decoder.DisallowUnknownFields()
	var response responseEnvelope
	if err := decoder.Decode(&response); err != nil {
		return nil, protocolError("decode response: " + err.Error())
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, protocolError("multiple response values")
	}
	if response.Protocol != ProtocolV1 {
		return nil, protocolError("unexpected protocol " + response.Protocol)
	}
	if !response.OK {
		if response.Error == nil {
			return nil, protocolError("failed response has no error")
		}
		return nil, &activity.Error{Class: response.Error.Class, Op: e.Manifest.Spec.Ref(), Err: fmt.Errorf("%s", response.Error.Message)}
	}
	if artifact, captureErr := e.captureStderr(envelope, stderr.Bytes()); captureErr != nil {
		return nil, captureErr
	} else if artifact != nil && response.Result != nil {
		response.Result.Artifacts = append(response.Result.Artifacts, *artifact)
	}
	return &response, nil
}

func (e *Executor) captureStderr(envelope requestEnvelope, raw []byte) (*activity.Artifact, error) {
	if len(raw) == 0 || e.StderrRoot == "" {
		return nil, nil
	}
	request := envelope.Request
	if request == nil && envelope.Reconcile != nil {
		request = &envelope.Reconcile.Request
	}
	if request == nil && envelope.Compensate != nil {
		request = &envelope.Compensate.Request
	}
	if request == nil {
		return nil, nil
	}
	dir := filepath.Join(e.StderrRoot, "activities", request.ScopePath, request.StepID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("attempt-%d.stderr.log", request.Attempt)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return &activity.Artifact{Kind: "activity_stderr", URI: filepath.ToSlash(path), Digest: hex.EncodeToString(digest[:])}, nil
}

func (e *Executor) messageLimit() int64 {
	if e.MaxMessageBytes > 0 {
		return e.MaxMessageBytes
	}
	return defaultMaxMessage
}
func protocolError(message string) error {
	return &activity.Error{Class: activity.ErrorInvalidContract, Op: "activity protocol", Err: fmt.Errorf("%s", message)}
}

type limitedWriter struct {
	Writer  io.Writer
	N       int64
	written int64
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.written+int64(len(p)) > w.N {
		return 0, fmt.Errorf("activity message exceeds %d bytes", w.N)
	}
	n, err := w.Writer.Write(p)
	w.written += int64(n)
	return n, err
}
