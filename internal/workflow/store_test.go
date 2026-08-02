package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/activity"
)

func TestStoreCreateRunStepAttemptAndOutbox(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	run, _, err := store.CreateRunOnce(ctx, CreateRunParams{ID: "r1", FlowRef: "flow:demo@1", DefinitionHash: "abc", IR: json.RawMessage(`{}`), Inputs: json.RawMessage(`{}`), Policy: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != RunRunning {
		t.Fatalf("status=%s", run.Status)
	}
	step, err := store.EnsureStep(ctx, "r1", "root", "s1", "", "test.echo@1", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := store.StartAttempt(ctx, step, "test.echo@1", "r1/root/s1")
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Number != 1 {
		t.Fatalf("attempt=%d", attempt.Number)
	}
	if err := store.CompleteAttempt(ctx, step, attempt, activityResult(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	step, err = store.GetStep(ctx, "r1", "root", "s1", "")
	if err != nil {
		t.Fatal(err)
	}
	if step.Status != StepSucceeded {
		t.Fatalf("step=%s", step.Status)
	}
	sink := &memorySink{}
	if err := store.FlushOutbox(ctx, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) < 2 {
		t.Fatalf("events=%d", len(sink.events))
	}
}

func TestStoreMigratesEverySupportedSchema(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workflows.db")
			db, err := sql.Open("sqlite", "file:"+path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(schemaV1); err != nil {
				t.Fatal(err)
			}
			if version >= 2 {
				if _, err = db.Exec(schemaV2); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", version)); err != nil {
				t.Fatal(err)
			}
			db.Close()
			store, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			var got int
			if err = store.db.QueryRow("PRAGMA user_version").Scan(&got); err != nil || got != schemaVersion {
				t.Fatalf("version=%d err=%v", got, err)
			}
		})
	}
}

func TestCreateRunOnceDeduplicatesSourceKey(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	params := CreateRunParams{ID: "first", SourceKey: "github/issues/42/update-1", FlowRef: "flow:demo@1", DefinitionHash: "abc", IR: json.RawMessage(`{}`), Inputs: json.RawMessage(`{}`), Policy: json.RawMessage(`{}`)}
	first, created, err := store.CreateRunOnce(ctx, params)
	if err != nil || !created {
		t.Fatalf("first claim: run=%+v created=%v err=%v", first, created, err)
	}
	params.ID = "second"
	second, created, err := store.CreateRunOnce(ctx, params)
	if err != nil || created {
		t.Fatalf("second claim: run=%+v created=%v err=%v", second, created, err)
	}
	if second.ID != "first" || second.SourceKey != params.SourceKey {
		t.Fatalf("deduplicated run = %+v", second)
	}
	runs, err := store.ListRuns(ctx, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%d err=%v", len(runs), err)
	}
}

func activityResult(raw string) activity.Result { return activity.Result{Output: json.RawMessage(raw)} }

type memorySink struct{ events []json.RawMessage }

func (s *memorySink) Append(raw json.RawMessage) error {
	s.events = append(s.events, append(json.RawMessage(nil), raw...))
	return nil
}
