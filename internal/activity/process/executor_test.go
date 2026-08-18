package process

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
)

func TestHelperActivityProcess(t *testing.T) {
	if os.Getenv("ORQ_ACTIVITY_HELPER") == "1" {
		helperProcess()
		return
	}
	root := t.TempDir()
	executor := &Executor{Manifest: Manifest{Protocol: ProtocolV1, Command: []string{os.Args[0], "-test.run=TestHelperActivityProcess"}, Spec: activity.Spec{Name: "test.process", Version: "1", Effect: activity.EffectReconcilable}}, Env: []string{"ORQ_ACTIVITY_HELPER=1"}, StderrRoot: root}
	described, err := executor.Describe(context.Background())
	if err != nil || described.Ref() != "test.process@1" {
		t.Fatalf("describe=%+v err=%v", described, err)
	}
	request := activity.Request{RunID: "r1", StepID: "s1", IdempotencyKey: "r1/s1", Inputs: json.RawMessage(`{"value":"hello"}`)}
	result, err := executor.Execute(context.Background(), request)
	if err != nil || string(result.Output) != `{"value":"hello"}` {
		t.Fatalf("result=%s err=%v", result.Output, err)
	}
	reconciled, err := executor.Reconcile(context.Background(), activity.ReconcileRequest{Request: request, Receipt: result.Receipt})
	if err != nil || reconciled.Status != activity.ReconcileApplied {
		t.Fatalf("reconcile=%+v err=%v", reconciled, err)
	}
	compensated, err := executor.Compensate(context.Background(), activity.CompensateRequest{Request: request, Receipt: result.Receipt})
	if err != nil || string(compensated.Output) != `{"compensated":true}` {
		t.Fatalf("compensate=%s err=%v", compensated.Output, err)
	}
}

func helperProcess() {
	var request requestEnvelope
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		os.Exit(2)
	}
	response := responseEnvelope{Protocol: ProtocolV1, OK: true}
	switch request.Operation {
	case OperationDescribe:
		spec := activity.Spec{Name: "test.process", Version: "1", Effect: activity.EffectReconcilable}
		response.Spec = &spec
	case OperationExecute:
		response.Result = &activity.Result{Output: request.Request.Inputs, Receipt: request.Request.IdempotencyKey}
	case OperationReconcile:
		response.Reconcile = &activity.ReconcileResult{Status: activity.ReconcileApplied, Result: &activity.Result{Output: request.Reconcile.Inputs, Receipt: request.Reconcile.Receipt}}
	case OperationCompensate:
		response.Result = &activity.Result{Output: json.RawMessage(`{"compensated":true}`), Receipt: request.Compensate.Receipt}
	default:
		response.OK = false
		response.Error = &responseError{Class: activity.ErrorPermanent, Message: "unsupported"}
	}
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}
