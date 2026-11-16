package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/config"
	"github.com/collectiveai-team/orquesta-lite/internal/execlock"
	"github.com/collectiveai-team/orquesta-lite/internal/external"
	"github.com/collectiveai-team/orquesta-lite/internal/workflow"
)

func TestExternalAdapterLoadsCredentialWithoutSerializingIt(t *testing.T) {
	const credential = "test-credential-never-serialize"
	t.Setenv("ORQ_TEST_PAPERCLIP_TOKEN", credential)
	settings := map[string]config.ExternalAdapter{
		"paperclip": {Type: "paperclip", BaseURL: "http://localhost:3100", CompanyID: "company", TokenEnv: "ORQ_TEST_PAPERCLIP_TOKEN"},
	}
	adapters, err := externalAdapters(settings)
	if err != nil || adapters["paperclip"] == nil {
		t.Fatalf("load adapter failed: %v", err)
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), credential) {
		t.Fatal("configuration serialized a credential value")
	}
}

func TestExternalReceiveCommitsWithoutAdapterCredentialsOrExecutor(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, ".orquestalite")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := workflow.Open(filepath.Join(state, "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	_, _, err = store.CreateRunOnce(ctx, workflow.CreateRunParams{ID: "inbox", FlowRef: "flow:external@1", IR: json.RawMessage(`{}`), Inputs: json.RawMessage(`{}`), Policy: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	step, err := store.EnsureStep(ctx, "inbox", "root", "task", "", "external.dispatch@1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := store.PrepareExternal(ctx, step, "external.dispatch@1", external.DispatchRequest{EmployeeID: "stable", Instructions: "work", CompletionCondition: "done", Deadline: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	// This configuration deliberately cannot load. Callback acceptance must
	// still be possible while another process holds the executor lock.
	release, err := execlock.Acquire(filepath.Join(state, "executor.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := os.WriteFile(filepath.Join(dir, "team.json"), []byte(`{"runtime":{"external_adapters":{"broken":{"type":"unknown"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "event.json")
	if err := os.WriteFile(path, []byte(`{"id":"callback","type":"agent.run.finished"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := FlowCLI(ctx, dir, []string{"external", "receive", "inbox", "task", attempt.ID, path}, &out); err != nil {
		t.Fatal(err)
	}
	var receipt struct{ Received, Pending bool }
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.Received || !receipt.Pending {
		t.Fatalf("receipt=%s", out.String())
	}
	saved, err := store.GetExternalAttempt(ctx, attempt.ID)
	if err != nil || saved.PendingEvents != 1 {
		t.Fatalf("event not durable: %+v %v", saved, err)
	}
}

func TestFlowCancelStopsSchedulingWhenExternalCredentialMissing(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, ".orquestalite")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := workflow.Open(filepath.Join(state, "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	_, _, err = store.CreateRunOnce(ctx, workflow.CreateRunParams{ID: "cancel-external", FlowRef: "flow:demo@1", IR: json.RawMessage(`{}`), Inputs: json.RawMessage(`{}`), Policy: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORQ_TEST_MISSING_PAPERCLIP_TOKEN", "")
	team := `{"runtime":{"external_adapters":{"paperclip":{"type":"paperclip","base_url":"http://localhost:3100","company_id":"company","token_env":"ORQ_TEST_MISSING_PAPERCLIP_TOKEN"}}}}`
	if err := os.WriteFile(filepath.Join(dir, "team.json"), []byte(team), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = FlowCLI(ctx, dir, []string{"cancel", "cancel-external"}, &out)
	if err == nil || !strings.Contains(err.Error(), "ORQ_TEST_MISSING_PAPERCLIP_TOKEN") {
		t.Fatalf("expected missing-credential error, got %v", err)
	}
	run, err := store.GetRun(ctx, "cancel-external")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflow.RunCancelled {
		t.Fatalf("local scheduling was not cancelled, status=%s", run.Status)
	}
}

func TestExternalPaperclipCLIMixedFlowReceivesAndReconcilesDurably(t *testing.T) {
	dir := t.TempDir()
	var assignments atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/companies/company/agents":
			fmt.Fprint(w, `[{"id":"agent","companyId":"company","name":"employee","status":"idle"}]`)
		case "POST /api/companies/company/issues":
			var request struct {
				Key   string `json:"idempotencyKey"`
				Agent string `json:"assigneeAgentId"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request.Key == "" || request.Agent != "agent" {
				t.Errorf("dispatch lost correlation: %+v", request)
			}
			assignments.Add(1)
			fmt.Fprint(w, `{"id":"issue","companyId":"company","assigneeAgentId":"agent","status":"todo"}`)
		case "GET /api/issues/issue":
			fmt.Fprint(w, `{"id":"issue","companyId":"company","assigneeAgentId":"agent","status":"done","documentSummaries":[{"key":"orq-result","latestRevisionId":"revision"}]}`)
		case "GET /api/issues/issue/execution":
			fmt.Fprint(w, `{"runId":"execution"}`)
		case "GET /api/heartbeat-runs/execution":
			fmt.Fprint(w, `{"id":"execution","companyId":"company","agentId":"agent","status":"succeeded","usageJson":{"inputTokens":100,"outputTokens":20},"resultJson":{"costUsd":0.5}}`)
		case "GET /api/agents/agent":
			fmt.Fprint(w, `{"id":"agent","companyId":"company","status":"idle"}`)
		case "GET /api/issues/issue/documents/orq-result":
			_ = json.NewEncoder(w).Encode(map[string]string{"body": `{"deliverables":{"approved":true},"evidence":[]}`})
		default:
			t.Errorf("unexpected Paperclip request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	team, _ := json.Marshal(map[string]any{"runtime": map[string]any{"external_adapters": map[string]any{"paperclip": map[string]string{"type": "paperclip", "base_url": server.URL, "company_id": "company"}}}})
	if err := os.WriteFile(filepath.Join(dir, "team.json"), team, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mixed.json")
	definition := `{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"mixed","version":"1"},"steps":[{"id":"before","uses":"activity:gate.assert@1","with":{"value":1,"equals":1}},{"id":"external","uses":"activity:external.dispatch@1","with":{"adapter":"paperclip","employee":"employee","instructions":"work","completionCondition":"approved with evidence","timeoutSeconds":3600,"completionGate":"item.deliverables.approved == true"}},{"id":"after","uses":"activity:gate.assert@1","with":{"value":{"$ref":"steps.external.output.outcome"},"equals":"success"}}]}`
	if err := os.WriteFile(path, []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var out bytes.Buffer
	if err := FlowCLI(ctx, dir, []string{"run", path}, &out); err != nil {
		t.Fatal(err)
	}
	store, err := workflow.Open(filepath.Join(dir, ".orquestalite", "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runs, err := store.ListRuns(ctx, 1)
	if err != nil || len(runs) != 1 || runs[0].Status != workflow.RunWaitingExternal {
		t.Fatalf("runs=%+v %v", runs, err)
	}
	runID := runs[0].ID
	records, err := store.ListExternalAttempts(ctx, runID)
	if err != nil || len(records) != 1 {
		t.Fatalf("attempts=%+v %v", records, err)
	}
	event := filepath.Join(dir, "event.json")
	if err := os.WriteFile(event, []byte(`{"id":"finished","type":"agent.run.finished"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := FlowCLI(ctx, dir, []string{"external", "receive", runID, "external", records[0].ID, event}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"received": true`) {
		t.Fatalf("missing receipt: %s", out.String())
	}
	if err := FlowCLI(ctx, dir, []string{"external", "reconcile", runID}, &out); err != nil {
		t.Fatal(err)
	}
	run, err := store.GetRun(ctx, runID)
	if err != nil || run.Status != workflow.RunSucceeded || assignments.Load() != 1 {
		t.Fatalf("run=%+v %v assignments=%d", run, err, assignments.Load())
	}
	saved, err := store.GetExternalAttempt(ctx, records[0].ID)
	if err != nil || saved.Result == nil || saved.Result.CostUSD != 0.5 || saved.Result.InputTokens != 100 {
		t.Fatalf("usage=%+v %v", saved, err)
	}
}
