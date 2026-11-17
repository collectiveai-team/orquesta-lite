package external

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PaperclipConfig describes a company-scoped Paperclip REST connection. Token
// is supplied by the host and is never included in errors or durable state.
type PaperclipConfig struct {
	BaseURL   string
	CompanyID string
	Token     string
	Employees map[string]string
	Client    *http.Client
}

type Paperclip struct{ config PaperclipConfig }

func NewPaperclip(config PaperclipConfig) (*Paperclip, error) {
	u, err := url.Parse(config.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("paperclip: base URL must be an HTTP(S) URL without credentials, query, or fragment")
	}
	if config.CompanyID == "" {
		return nil, errors.New("paperclip: company ID is required")
	}
	config.BaseURL = strings.TrimRight(config.BaseURL, "/")
	if !strings.HasSuffix(config.BaseURL, "/api") {
		config.BaseURL += "/api"
	}
	if config.Client == nil {
		config.Client = &http.Client{Timeout: 30 * time.Second}
	}
	// Copy alias resolutions so a caller cannot mutate an in-flight adapter.
	aliases := make(map[string]string, len(config.Employees))
	for name, id := range config.Employees {
		aliases[name] = id
	}
	config.Employees = aliases
	return &Paperclip{config: config}, nil
}

type paperclipAgent struct {
	ID        string `json:"id"`
	CompanyID string `json:"companyId"`
	Name      string `json:"name"`
	URLKey    string `json:"urlKey"`
	Status    string `json:"status"`
}

func (p *Paperclip) ResolveEmployee(ctx context.Context, employee string) (string, error) {
	if alias, ok := p.config.Employees[employee]; ok {
		employee = alias
	}
	var agents []paperclipAgent
	if err := p.request(ctx, http.MethodGet, "/companies/"+url.PathEscape(p.config.CompanyID)+"/agents", nil, &agents); err != nil {
		return "", err
	}
	var matches []paperclipAgent
	for _, agent := range agents {
		if agent.ID == employee {
			return agent.ID, nil
		}
		if agent.Name == employee || agent.URLKey == employee {
			matches = append(matches, agent)
		}
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("paperclip: employee must resolve to exactly one company agent (found %d)", len(matches))
	}
	return matches[0].ID, nil
}

type paperclipIssue struct {
	ID              string `json:"id"`
	CompanyID       string `json:"companyId"`
	Status          string `json:"status"`
	AssigneeAgentID string `json:"assigneeAgentId"`
	ExecutionRunID  string `json:"executionRunId"`
	WorkProducts    []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"workProducts"`
	DocumentSummaries []struct {
		Key              string `json:"key"`
		LatestRevisionID string `json:"latestRevisionId"`
	} `json:"documentSummaries"`
}

// Dispatch relies on Paperclip's createIssue body idempotencyKey contract, not
// an unimplemented Idempotency-Key header. Paperclip must support that contract.
// Upstream retains create keys for seven days; hosts must send an uncertain
// dispatch older than that to intervention rather than replaying its POST.
func (p *Paperclip) Dispatch(ctx context.Context, request DispatchRequest) (References, error) {
	if request.AttemptID == "" || request.IdempotencyKey == "" || request.EmployeeID == "" || request.Instructions == "" || request.Deadline.IsZero() {
		return References{}, errors.New("paperclip: attempt, idempotency key, employee, instructions, and deadline are required")
	}
	if len(request.IdempotencyKey) > 255 {
		return References{}, errors.New("paperclip: idempotency key exceeds 255 bytes")
	}
	description := request.Instructions + "\n\nCompletion condition:\n" + request.CompletionCondition +
		"\n\nOrq-lite correlation:\nflow_run_id: " + request.FlowRunID + "\nstep_id: " + request.StepID + "\nattempt_id: " + request.AttemptID +
		"\ndeadline: " + request.Deadline.UTC().Format(time.RFC3339Nano) +
		"\n\nComplete this issue only after meeting the completion condition. Publish structured output in an issue document keyed orq-result, containing JSON with deliverables and evidence fields. Flow transitions remain owned by orq-lite."
	if len(request.OutputSchema) > 0 {
		if !json.Valid(request.OutputSchema) {
			return References{}, errors.New("paperclip: invalid deliverables schema")
		}
		description += "\n\nRequired JSON schema for the deliverables field:\n```json\n" + string(request.OutputSchema) + "\n```"
	}
	correlation, _ := json.Marshal(map[string]string{"flowRunId": request.FlowRunID, "stepId": request.StepID, "attemptId": request.AttemptID, "sourceKey": request.SourceKey})
	description += "\n\n<!-- orq-lite: " + string(correlation) + " -->"
	body := map[string]any{
		"title":       "orq-lite " + request.StepID + " [" + request.AttemptID + "]",
		"description": description, "status": "todo", "assigneeAgentId": request.EmployeeID,
		"idempotencyKey": request.IdempotencyKey,
	}
	if request.ProjectID != "" {
		body["projectId"] = request.ProjectID
	}
	var issue paperclipIssue
	if err := p.request(ctx, http.MethodPost, "/companies/"+url.PathEscape(p.config.CompanyID)+"/issues", body, &issue); err != nil {
		return References{}, err
	}
	if issue.ID == "" {
		return References{}, &UnavailableError{Operation: "dispatch response", Err: errors.New("missing issue ID")}
	}
	if issue.AssigneeAgentID != request.EmployeeID {
		return References{}, errors.New("paperclip: dispatched issue assignee differs from pinned employee")
	}
	return References{IssueID: issue.ID, RunID: issue.ExecutionRunID}, nil
}

func (p *Paperclip) Inspect(ctx context.Context, refs References) (Result, error) {
	if refs.IssueID == "" {
		return Result{}, errors.New("paperclip: issue ID is required")
	}
	issuePath := "/issues/" + url.PathEscape(refs.IssueID)
	var issue paperclipIssue
	if err := p.request(ctx, http.MethodGet, issuePath, nil, &issue); err != nil {
		return Result{}, err
	}
	if issue.ID != refs.IssueID || issue.CompanyID != p.config.CompanyID {
		return Result{}, errors.New("paperclip: issue identity or company mismatch")
	}
	result := Result{TaskStatus: issue.Status, EmployeeID: issue.AssigneeAgentID, References: References{IssueID: issue.ID}}
	// Task-level execution lookup includes completed runs after execution locks
	// have been cleared. Do not trust a callback's run ID as the current run.
	var execution *struct {
		RunID string `json:"runId"`
	}
	if err := p.request(ctx, http.MethodGet, issuePath+"/execution", nil, &execution); err != nil {
		return Result{}, err
	}
	if execution != nil {
		result.References.RunID = execution.RunID
	}
	if result.References.RunID == "" {
		result.References.RunID = issue.ExecutionRunID
	}
	if result.References.RunID != "" {
		var run struct {
			ID        string          `json:"id"`
			CompanyID string          `json:"companyId"`
			AgentID   string          `json:"agentId"`
			Status    string          `json:"status"`
			Result    json.RawMessage `json:"resultJson"`
			CostUSD   *float64        `json:"costUsd"`
			Usage     struct {
				InputTokens  *int64 `json:"inputTokens"`
				OutputTokens *int64 `json:"outputTokens"`
			} `json:"usageJson"`
		}
		if err := p.request(ctx, http.MethodGet, "/heartbeat-runs/"+url.PathEscape(result.References.RunID), nil, &run); err != nil {
			return Result{}, err
		}
		if run.ID != result.References.RunID || run.CompanyID != p.config.CompanyID {
			return Result{}, errors.New("paperclip: run identity or company mismatch")
		}
		result.RunStatus, result.Deliverables = run.Status, run.Result
		// A reassigned issue or an execution performed by a different agent may
		// never satisfy the engine's pinned employee check.
		if run.AgentID != issue.AssigneeAgentID {
			result.EmployeeID = ""
		}
		result.Usage = &Usage{CostUSD: run.CostUSD, InputTokens: run.Usage.InputTokens, OutputTokens: run.Usage.OutputTokens}
		// The full run endpoint keeps provider cost inside resultJson; list
		// projections may also expose a top-level costUsd. Match Paperclip's
		// documented projection precedence without inventing a token price.
		if result.Usage.CostUSD == nil {
			var fields map[string]json.RawMessage
			if json.Unmarshal(run.Result, &fields) == nil {
				for _, key := range []string{"costUsd", "cost_usd", "total_cost_usd"} {
					if raw, ok := fields[key]; ok && string(raw) != "null" {
						var amount float64
						if json.Unmarshal(raw, &amount) == nil {
							result.Usage.CostUSD = &amount
							break
						}
						var decimal string
						if json.Unmarshal(raw, &decimal) == nil {
							if amount, err := strconv.ParseFloat(decimal, 64); err == nil {
								result.Usage.CostUSD = &amount
								break
							}
						}
					}
				}
			}
		}
		if result.Usage.CostUSD != nil {
			result.CostUSD = *result.Usage.CostUSD
		}
		if result.Usage.InputTokens != nil {
			result.InputTokens = *result.Usage.InputTokens
		}
		if result.Usage.OutputTokens != nil {
			result.OutputTokens = *result.Usage.OutputTokens
		}
	}
	if issue.AssigneeAgentID != "" {
		var agent paperclipAgent
		if err := p.request(ctx, http.MethodGet, "/agents/"+url.PathEscape(issue.AssigneeAgentID), nil, &agent); err != nil {
			return Result{}, err
		}
		if agent.ID != issue.AssigneeAgentID || agent.CompanyID != p.config.CompanyID {
			return Result{}, errors.New("paperclip: agent identity or company mismatch")
		}
		result.EmployeeStatus = agent.Status
	}
	for _, product := range issue.WorkProducts {
		if product.ID != "" && product.Status != "failed" && product.Status != "archived" && product.Status != "draft" {
			result.Evidence = append(result.Evidence, p.config.BaseURL+issuePath+"/work-products#"+url.PathEscape(product.ID))
		}
	}
	for _, document := range issue.DocumentSummaries {
		if document.Key != "orq-result" {
			continue
		}
		var doc struct {
			Body string `json:"body"`
		}
		if err := p.request(ctx, http.MethodGet, issuePath+"/documents/orq-result", nil, &doc); err != nil {
			return Result{}, err
		}
		var output struct {
			Deliverables json.RawMessage `json:"deliverables"`
			Evidence     []string        `json:"evidence"`
		}
		if err := json.Unmarshal([]byte(doc.Body), &output); err != nil {
			return Result{}, errors.New("paperclip: orq-result document must contain a JSON result object")
		}
		result.Deliverables = output.Deliverables
		result.Evidence = append(result.Evidence, output.Evidence...)
		if document.LatestRevisionID != "" {
			result.Evidence = append(result.Evidence, p.config.BaseURL+issuePath+"/documents/orq-result/revisions/"+url.PathEscape(document.LatestRevisionID))
		}
	}
	return result, nil
}

func (p *Paperclip) Cancel(ctx context.Context, refs References) (Cancellation, error) {
	if refs.IssueID == "" {
		return Cancellation{}, errors.New("paperclip: issue ID is required")
	}
	// Persist task cancellation first so a heartbeat completion cannot cause a
	// fresh assignment. Cancelling only a run leaves its task open in Paperclip.
	var issue paperclipIssue
	if err := p.request(ctx, http.MethodPatch, "/issues/"+url.PathEscape(refs.IssueID), map[string]string{"status": "cancelled"}, &issue); err != nil {
		return Cancellation{Detail: "task cancellation was not confirmed"}, err
	}
	if issue.Status != "cancelled" {
		return Cancellation{Detail: "Paperclip did not confirm task cancellation"}, nil
	}
	runID := issue.ExecutionRunID
	if runID == "" {
		runID = refs.RunID
	}
	if runID != "" {
		var run struct {
			Status string `json:"status"`
		}
		if err := p.request(ctx, http.MethodGet, "/heartbeat-runs/"+url.PathEscape(runID), nil, &run); err != nil {
			return Cancellation{Detail: "task cancelled; run cancellation could not be confirmed"}, err
		}
		if run.Status == "running" || run.Status == "queued" {
			var cancelled *struct {
				Status string `json:"status"`
			}
			if err := p.request(ctx, http.MethodPost, "/heartbeat-runs/"+url.PathEscape(runID)+"/cancel", map[string]any{}, &cancelled); err != nil {
				return Cancellation{Detail: "task cancelled; run cancellation was not accepted"}, err
			}
			if cancelled == nil || cancelled.Status != "cancelled" {
				return Cancellation{Detail: "task cancelled; Paperclip did not confirm run cancellation"}, nil
			}
		}
	}
	return Cancellation{Accepted: true, Detail: "Paperclip confirmed task cancellation"}, nil
}

const maxResponseBytes = 8 << 20

func (p *Paperclip) request(ctx context.Context, method, path string, body, output any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return errors.New("paperclip: cannot encode request")
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.config.BaseURL+path, reader)
	if err != nil {
		return errors.New("paperclip: invalid request")
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.config.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.config.Token)
	}
	// Disable redirects, including when an injected client permits them. Never
	// forward credentials or replay mutations at a redirected endpoint.
	client := *p.config.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return &UnavailableError{Operation: method, Err: err}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusRequestTimeout {
			return &UnavailableError{Operation: method, StatusCode: response.StatusCode}
		}
		// Response bodies may echo credentials, instructions, or private output.
		return fmt.Errorf("paperclip %s: HTTP %d", method, response.StatusCode)
	}
	if output == nil {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes {
		return &UnavailableError{Operation: method + " response", Err: err}
	}
	if err := json.Unmarshal(raw, output); err != nil {
		return &UnavailableError{Operation: method + " response", Err: err}
	}
	return nil
}
