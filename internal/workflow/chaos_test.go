package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/activity"
)

func TestChaosResumeReconcilesNotAppliedBeforeSingleExecute(t *testing.T) {
	ir, catalog := recoveryIR(t, activity.EffectReconcilable)
	store, err := Open(filepath.Join(t.TempDir(), "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	interruptedRun(t, store, ir)
	executor := &recoveryExecutor{spec: activity.Spec{Name: "test.effect", Version: "1", Effect: activity.EffectReconcilable}, reconcile: activity.ReconcileNotApplied}
	registry := activity.NewRegistry()
	_ = registry.Register(executor)
	run, err := (&Runtime{Store: store, Activities: registry, Catalog: catalog}).Resume(context.Background(), "r")
	if err != nil || run.Status != RunSucceeded || executor.reconciles != 1 || executor.executes != 1 {
		t.Fatalf("run=%+v err=%v reconcile=%d execute=%d", run, err, executor.reconciles, executor.executes)
	}
	step, _ := store.GetStep(context.Background(), "r", "root", "effect", "")
	latest, _ := store.LatestAttempt(context.Background(), step.ID)
	if latest.Number != 2 || latest.IdempotencyKey != "r/root/effect/" {
		t.Fatalf("latest attempt=%+v", latest)
	}
}

type failOnceSink struct {
	failed bool
	events []json.RawMessage
}

func (s *failOnceSink) Append(raw json.RawMessage) error {
	if !s.failed {
		s.failed = true
		return fmt.Errorf("crash before delivery acknowledgement")
	}
	s.events = append(s.events, append(json.RawMessage(nil), raw...))
	return nil
}

func TestChaosOutboxReplaysAfterAppendFailure(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, err = store.CreateRunOnce(context.Background(), CreateRunParams{ID: "outbox", FlowRef: "flow:test@1", DefinitionHash: "h", IR: json.RawMessage(`{}`), Inputs: json.RawMessage(`{}`), Policy: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	sink := &failOnceSink{}
	if err = store.FlushOutbox(context.Background(), sink); err == nil {
		t.Fatal("first delivery should fail")
	}
	if err = store.FlushOutbox(context.Background(), sink); err != nil || len(sink.events) != 1 {
		t.Fatalf("replay events=%d err=%v", len(sink.events), err)
	}
}
