package external

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func paperclipForTest(t *testing.T, handler http.HandlerFunc) *Paperclip {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	adapter, err := NewPaperclip(PaperclipConfig{BaseURL: server.URL, CompanyID: "company", Token: "test-private-token"})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func TestPaperclipResolveEmployeePinsCompanyAgent(t *testing.T) {
	adapter := paperclipForTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/companies/company/agents" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-private-token" {
			t.Error("missing authorization")
		}
		fmt.Fprint(w, `[{"id":"employee-1","name":"Builder","urlKey":"builder"},{"id":"employee-2","name":"Builder"}]`)
	})
	if _, err := adapter.ResolveEmployee(context.Background(), "Builder"); err == nil {
		t.Fatal("ambiguous name accepted")
	}
	for _, name := range []string{"builder", "employee-1"} {
		id, err := adapter.ResolveEmployee(context.Background(), name)
		if err != nil || id != "employee-1" {
			t.Fatalf("resolve %s: id=%s err=%v", name, id, err)
		}
	}
}

func TestPaperclipDispatchUsesServerIdempotencyBody(t *testing.T) {
	var mu sync.Mutex
	issues := map[string]string{}
	var seen []string
	adapter := paperclipForTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/companies/company/issues" {
			t.Errorf("unexpected dispatch %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		key, _ := body["idempotencyKey"].(string)
		if key == "" {
			t.Error("missing body idempotency key")
		}
		if body["assigneeAgentId"] != "employee-1" || body["status"] != "todo" {
			t.Error("incorrect assignment")
		}
		if body["projectId"] != "project-1" || !strings.Contains(body["description"].(string), `"required":["complete"]`) {
			t.Error("missing coding project or pinned output contract")
		}
		if !strings.Contains(body["description"].(string), "attempt_id: attempt-1") {
			t.Error("missing correlation")
		}
		mu.Lock()
		if issues[key] == "" {
			issues[key] = fmt.Sprintf("issue-%d", len(issues)+1)
		}
		id := issues[key]
		seen = append(seen, key)
		mu.Unlock()
		fmt.Fprintf(w, `{"id":%q,"assigneeAgentId":"employee-1"}`, id)
	})
	request := DispatchRequest{FlowRunID: "flow-1", StepID: "build", AttemptID: "attempt-1", IdempotencyKey: "attempt-key", EmployeeID: "employee-1", ProjectID: "project-1", Instructions: "Build it", CompletionCondition: "Tests pass", OutputSchema: json.RawMessage(`{"type":"object","required":["complete"]}`), Deadline: time.Now().Add(time.Hour)}
	first, err := adapter.Dispatch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := adapter.Dispatch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.IssueID != second.IssueID || len(issues) != 1 || len(seen) != 2 {
		t.Fatalf("retry did not retain key: %#v %#v", first, second)
	}
}

func TestPaperclipInspectReadsIssueRunAgentAndPersistedResult(t *testing.T) {
	adapter := paperclipForTest(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/issues/issue-1":
			fmt.Fprint(w, `{"id":"issue-1","companyId":"company","status":"done","assigneeAgentId":"employee-1","executionRunId":null,"workProducts":[{"id":"artifact-1","status":"approved"},{"id":"artifact-failed","status":"failed"}],"documentSummaries":[{"key":"orq-result","latestRevisionId":"revision-1"}]}`)
		case "/api/issues/issue-1/execution":
			fmt.Fprint(w, `{"runId":"latest-run"}`)
		case "/api/heartbeat-runs/latest-run":
			fmt.Fprint(w, `{"id":"latest-run","companyId":"company","agentId":"employee-1","status":"succeeded","usageJson":{"inputTokens":120,"outputTokens":35},"resultJson":{"summary":"raw","costUsd":0.25}}`)
		case "/api/agents/employee-1":
			fmt.Fprint(w, `{"id":"employee-1","companyId":"company","status":"idle"}`)
		case "/api/issues/issue-1/documents/orq-result":
			_ = json.NewEncoder(w).Encode(map[string]string{"body": `{"deliverables":{"approved":true},"evidence":["https://example.test/review"]}`})
		case "/api/issues/issue-1/work-products":
			fmt.Fprint(w, `[{"id":"artifact-1","status":"approved"}]`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	result, err := adapter.Inspect(context.Background(), References{IssueID: "issue-1", RunID: "obsolete-run"})
	if err != nil {
		t.Fatal(err)
	}
	if result.TaskStatus != "done" || result.RunStatus != "succeeded" || result.EmployeeID != "employee-1" || result.EmployeeStatus != "idle" || result.References.RunID != "latest-run" {
		t.Fatalf("bad identity/status: %#v", result)
	}
	if string(result.Deliverables) != `{"approved":true}` || len(result.Evidence) != 3 {
		t.Fatalf("missing persisted evidence/result: %#v", result)
	}
	if result.InputTokens != 120 || result.OutputTokens != 35 || result.CostUSD != 0.25 {
		t.Fatalf("bad usage %#v", result)
	}
	response, err := http.Get(result.Evidence[0])
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasSuffix(result.Evidence[0], "#artifact-1") {
		t.Fatalf("persisted product evidence has no resolvable collection provenance: %s", result.Evidence[0])
	}
}

func TestPaperclipInspectRunFinishedDoesNotCompleteOpenIssue(t *testing.T) {
	adapter := paperclipForTest(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/issues/issue":
			fmt.Fprint(w, `{"id":"issue","companyId":"company","status":"in_progress","assigneeAgentId":"employee"}`)
		case "/api/issues/issue/execution":
			fmt.Fprint(w, `{"runId":"run"}`)
		case "/api/heartbeat-runs/run":
			fmt.Fprint(w, `{"id":"run","companyId":"company","status":"succeeded","agentId":"employee"}`)
		case "/api/agents/employee":
			fmt.Fprint(w, `{"id":"employee","companyId":"company","status":"idle"}`)
		default:
			http.NotFound(w, r)
		}
	})
	result, err := adapter.Inspect(context.Background(), References{IssueID: "issue"})
	if err != nil {
		t.Fatal(err)
	}
	if result.TaskStatus != "in_progress" || result.RunStatus != "succeeded" {
		t.Fatalf("adapter invented completion %#v", result)
	}
}

func TestPaperclipUsagePreservesUnavailableAndExplicitZero(t *testing.T) {
	for _, metrics := range []string{``, `,"costUsd":0,"usageJson":{"inputTokens":0}`} {
		t.Run(fmt.Sprint(len(metrics)), func(t *testing.T) {
			adapter := paperclipForTest(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/issues/issue":
					fmt.Fprint(w, `{"id":"issue","companyId":"company","status":"in_progress","assigneeAgentId":"employee"}`)
				case "/api/issues/issue/execution":
					fmt.Fprint(w, `{"runId":"run"}`)
				case "/api/heartbeat-runs/run":
					fmt.Fprintf(w, `{"id":"run","companyId":"company","agentId":"employee","status":"running"%s}`, metrics)
				case "/api/agents/employee":
					fmt.Fprint(w, `{"id":"employee","companyId":"company","status":"running"}`)
				default:
					http.NotFound(w, r)
				}
			})
			result, err := adapter.Inspect(context.Background(), References{IssueID: "issue"})
			if err != nil {
				t.Fatal(err)
			}
			if result.Usage == nil {
				t.Fatal("missing usage presence information")
			}
			raw, _ := json.Marshal(result.Usage)
			expected := `{}`
			if metrics != "" {
				expected = `{"costUSD":0,"inputTokens":0}`
			}
			if string(raw) != expected {
				t.Fatalf("usage=%s expected=%s", raw, expected)
			}
		})
	}
}

func TestPaperclipInspectRejectsChangedResponsibleAgent(t *testing.T) {
	adapter := paperclipForTest(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/issues/issue":
			fmt.Fprint(w, `{"id":"issue","companyId":"company","status":"done","assigneeAgentId":"employee"}`)
		case "/api/issues/issue/execution":
			fmt.Fprint(w, `{"runId":"run"}`)
		case "/api/heartbeat-runs/run":
			fmt.Fprint(w, `{"id":"run","companyId":"company","status":"succeeded","agentId":"someone-else"}`)
		case "/api/agents/employee":
			fmt.Fprint(w, `{"id":"employee","companyId":"company","status":"idle"}`)
		default:
			http.NotFound(w, r)
		}
	})
	result, err := adapter.Inspect(context.Background(), References{IssueID: "issue"})
	if err != nil {
		t.Fatal(err)
	}
	if result.EmployeeID != "" {
		t.Fatalf("run ownership mismatch accepted %#v", result)
	}
}

func TestPaperclipUnavailableIsTypedAndDoesNotEchoSecrets(t *testing.T) {
	adapter := paperclipForTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "test-private-token private payload")
	})
	_, err := adapter.Inspect(context.Background(), References{IssueID: "issue"})
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("not typed unavailable: %v", err)
	}
	if strings.Contains(err.Error(), "test-private-token") || strings.Contains(err.Error(), "private payload") {
		t.Fatal("response leaked into error")
	}
}

func TestPaperclipCancelCancelsTaskAndActiveRun(t *testing.T) {
	var paths []string
	adapter := paperclipForTest(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "PATCH /api/issues/issue":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["status"] != "cancelled" {
				t.Error("task not cancelled")
			}
			fmt.Fprint(w, `{"id":"issue","status":"cancelled","executionRunId":"run"}`)
		case "GET /api/heartbeat-runs/run":
			fmt.Fprint(w, `{"status":"running"}`)
		case "POST /api/heartbeat-runs/run/cancel":
			fmt.Fprint(w, `{"status":"cancelled"}`)
		default:
			t.Errorf("unexpected cancel request %s", r.URL.Path)
		}
	})
	result, err := adapter.Cancel(context.Background(), References{IssueID: "issue"})
	if err != nil || !result.Accepted || len(paths) != 3 {
		t.Fatalf("cancel=%#v err=%v paths=%v", result, err, paths)
	}
}

func TestPaperclipDoesNotFollowRedirectWithAuthorization(t *testing.T) {
	var targetCalled bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalled = true }))
	defer target.Close()
	adapter := paperclipForTest(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	if _, err := adapter.ResolveEmployee(context.Background(), "employee"); err == nil {
		t.Fatal("redirect accepted")
	}
	if targetCalled {
		t.Fatal("redirected authenticated request")
	}
}
