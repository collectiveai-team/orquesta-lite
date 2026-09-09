package workflow

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"testing"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
)

type blockingEffect struct{ marker string }

func (e blockingEffect) Spec() activity.Spec {
	return activity.Spec{Name: "test.effect", Version: "1", Effect: activity.EffectAtMostOnce}
}
func (e blockingEffect) Execute(ctx context.Context, _ activity.Request) (activity.Result, error) {
	f, err := os.OpenFile(e.marker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return activity.Result{}, err
	}
	defer f.Close()
	_, _ = f.WriteString("started\n")
	<-ctx.Done()
	_, _ = f.WriteString("stopped\n")
	return activity.Result{}, ctx.Err()
}

func TestExecutorProcess(t *testing.T) {
	if db := os.Getenv("ORQ_TEST_DB"); db != "" {
		store, err := Open(db)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		_, catalog := recoveryIR(t, activity.EffectAtMostOnce)
		registry := activity.NewRegistry()
		_ = registry.Register(blockingEffect{db + ".marker"})
		runtime := &Runtime{Store: store, Activities: registry, Catalog: catalog}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		_, err = runtime.Resume(ctx, "r")
		if err != nil {
			os.Exit(24)
		}
		return
	}
	for _, mode := range []string{"cancel", "deadline", "interrupt", "crash"} {
		t.Run(mode, func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "w.db")
			store, err := Open(db)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ir, _ := recoveryIR(t, activity.EffectAtMostOnce)
			raw, _ := json.Marshal(ir)
			policy := DefaultPolicy()
			if mode == "deadline" {
				policy.MaxDurationSeconds = 1
			}
			pol, _ := json.Marshal(policy)
			_, _, err = store.CreateRunOnce(context.Background(), CreateRunParams{ID: "r", IR: raw, Inputs: json.RawMessage(`{}`), Policy: pol})
			if err != nil {
				t.Fatal(err)
			}
			child := func() *exec.Cmd {
				cmd := exec.Command(os.Args[0], "-test.run=^TestExecutorProcess$")
				cmd.Env = append(os.Environ(), "ORQ_TEST_DB="+db)
				return cmd
			}
			first := child()
			if err = first.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = first.Process.Kill() }()
			until := time.Now().Add(4 * time.Second)
			for {
				b, _ := os.ReadFile(db + ".marker")
				if len(b) > 0 {
					break
				}
				if time.Now().After(until) {
					t.Fatal("executor did not start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			// A second coordinator cannot recover the live attempt or run any effect.
			if err = child().Run(); err == nil {
				t.Fatal("second executor accepted")
			}
			switch mode {
			case "cancel":
				if err = store.SetRunStatus(context.Background(), "r", RunCancelled, "test"); err != nil {
					t.Fatal(err)
				}
			case "interrupt":
				if err = first.Process.Signal(os.Interrupt); err != nil {
					t.Skipf("interrupt unavailable: %v", err)
				}
			case "crash":
				_ = first.Process.Kill()
			}
			done := make(chan error, 1)
			go func() { done <- first.Wait() }()
			select {
			case <-done:
			case <-time.After(4 * time.Second):
				t.Fatal("executor did not stop")
			}
			run, err := store.GetRun(context.Background(), "r")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "cancel" || mode == "deadline" {
				if run.Status != RunCancelled {
					t.Fatalf("status %s", run.Status)
				}
			}
			if mode == "interrupt" && run.Status != RunNeedsHuman {
				t.Fatalf("status %s", run.Status)
			}
			// The crash-released lock permits recovery, but uncertain effects require
			// approval; no automatic replay and no second marker.
			_ = child().Run()
			b, _ := os.ReadFile(db + ".marker")
			expected := "started\nstopped\n"
			if mode == "crash" {
				expected = "started\n"
			}
			if string(b) != expected {
				t.Fatalf("effects: %q", b)
			}
		})
	}
}
