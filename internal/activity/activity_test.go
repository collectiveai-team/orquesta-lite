package activity

import (
	"context"
	"errors"
	"testing"
)

type testExecutor struct{ spec Spec }

func (e testExecutor) Spec() Spec                                     { return e.spec }
func (testExecutor) Execute(context.Context, Request) (Result, error) { return Result{}, nil }

func TestRegistryRejectsDuplicatesAndInvalidEffects(t *testing.T) {
	r := NewRegistry()
	e := testExecutor{spec: Spec{Name: "test.echo", Version: "1", Effect: EffectPure}}
	if err := r.Register(e); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(e); err == nil {
		t.Fatal("expected duplicate error")
	}
	bad := testExecutor{spec: Spec{Name: "test.bad", Version: "1", Effect: "surprising"}}
	if err := r.Register(bad); err == nil {
		t.Fatal("expected invalid effect error")
	}
}

func TestClassify(t *testing.T) {
	err := &Error{Class: ErrorConflict, Op: "merge", Err: errors.New("boom")}
	if got := Classify(err); got != ErrorConflict {
		t.Fatalf("Classify = %q", got)
	}
	if got := Classify(errors.New("plain")); got != ErrorPermanent {
		t.Fatalf("plain Classify = %q", got)
	}
}
