// Package opencodeattach talks to a user-run opencode server so orq-lite can
// mint sessions before an agent runs, instead of scraping the session id out of
// stdout afterwards.
//
// The point is the shape of the result in the opencode TUI. Every `opencode run`
// already creates a session, so a twenty-ticket factory run leaves sixty-odd
// flat, similarly-named sessions in the session list. Creating them up front
// lets orq-lite give each one a parent and a readable title: one root session
// per run, one child per (task, role, agent). `GET /session?roots=true` — which
// is what the TUI's list uses — excludes children, so the run collapses to a
// single navigable entry.
//
// Server ownership is deliberately the user's. orq-lite never spawns or
// supervises `opencode serve`; the session tree is only worth building if it
// appears in a TUI the user already has open. Consequently an unreachable
// server is a hard error, never a silent fallback to a detached `opencode run`:
// config declaring attach while the runtime quietly did something else is the
// exact failure mode this package exists to avoid.
package opencodeattach

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultTimeout bounds every control-plane call. These are small local HTTP
// requests; a slow one means the server is wedged, and blocking a run on it
// helps nobody.
const DefaultTimeout = 10 * time.Second

// Client is a minimal opencode server control-plane client. It covers only the
// two things attach mode needs: proving the server is there, and creating a
// session with a parent, title, and metadata.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient returns a Client for a server base URL (e.g. http://127.0.0.1:4096).
// The URL is validated here so a typo in team.json surfaces at config load
// rather than as a confusing connection error mid-run.
func NewClient(rawURL string) (*Client, error) {
	normalized, err := NormalizeURL(rawURL)
	if err != nil {
		return nil, err
	}
	return &Client{
		baseURL: normalized,
		http:    &http.Client{Timeout: DefaultTimeout},
	}, nil
}

// NormalizeURL validates an attach URL and strips any trailing slash so paths
// can be concatenated without producing a double slash.
func NormalizeURL(rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", fmt.Errorf("attach url is empty")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("attach url %q is not a valid URL: %w", rawURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("attach url %q must use http or https, got %q", rawURL, parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("attach url %q has no host", rawURL)
	}
	return strings.TrimSuffix(trimmed, "/"), nil
}

// URL returns the normalized server base URL.
func (c *Client) URL() string { return c.baseURL }

// Ping proves the server is reachable and speaking the session API. It asks for
// roots only, which is the cheapest listing the server offers.
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/session?roots=true", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("opencode server at %s is unreachable: %w", c.baseURL, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("opencode server at %s returned HTTP %d for GET /session", c.baseURL, resp.StatusCode)
	}
	return nil
}

// CreateRequest describes a session to create. ParentID empty creates a root.
type CreateRequest struct {
	ParentID string         `json:"parentID,omitempty"`
	Title    string         `json:"title,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// CreateSession creates a session and returns its id. Directory scopes the
// session to a project path on the server, which matters because the server
// resolves paths remote-side.
func (c *Client) CreateSession(ctx context.Context, directory string, req CreateRequest) (string, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	endpoint := c.baseURL + "/session"
	if directory != "" {
		endpoint += "?directory=" + url.QueryEscape(directory)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("create opencode session on %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("create opencode session on %s: %w", c.baseURL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("create opencode session on %s: HTTP %d: %s",
			c.baseURL, resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var decoded struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", fmt.Errorf("create opencode session on %s: malformed response: %w", c.baseURL, err)
	}
	if decoded.ID == "" {
		return "", fmt.Errorf("create opencode session on %s: response carried no session id", c.baseURL)
	}
	return decoded.ID, nil
}

// Manager mints the session tree for a single run: one root, then a child per
// agent invocation. It is safe for concurrent use because foreach steps run
// role invocations in parallel.
type Manager struct {
	client  *Client
	dir     string
	runID   string
	flowRef string

	mu     sync.Mutex
	rootID string
}

// NewManager builds a Manager for one run. dir is the absolute project path the
// server should resolve agent work against; it is also what the CLI is handed
// as --dir.
func NewManager(client *Client, dir, runID, flowRef string) *Manager {
	return &Manager{client: client, dir: dir, runID: runID, flowRef: flowRef}
}

// URL returns the attach server URL.
func (m *Manager) URL() string { return m.client.URL() }

// Dir returns the project directory the server resolves paths against.
func (m *Manager) Dir() string { return m.dir }

// RootID returns the run's root session id, or "" if none has been created yet.
func (m *Manager) RootID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rootID
}

// root returns the run's root session, creating it on first use. Creation is
// lazy so a run whose roles never touch the opencode provider leaves no stray
// empty session behind.
func (m *Manager) root(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rootID != "" {
		return m.rootID, nil
	}
	id, err := m.client.CreateSession(ctx, m.dir, CreateRequest{
		Title:    RootTitle(m.flowRef, m.runID),
		Metadata: map[string]any{"orq_run_id": m.runID, "orq_flow": m.flowRef},
	})
	if err != nil {
		return "", err
	}
	m.rootID = id
	return id, nil
}

// ChildSession creates a session for one agent invocation, parented to the
// run's root, and returns its id. The caller passes that id to
// `opencode run -s`, inverting the usual direction: the session id is known
// before the agent starts rather than scraped from its output afterwards.
func (m *Manager) ChildSession(ctx context.Context, taskID, role, agent string) (string, error) {
	rootID, err := m.root(ctx)
	if err != nil {
		return "", err
	}
	metadata := map[string]any{
		"orq_run_id": m.runID,
		"orq_role":   role,
		"orq_agent":  agent,
	}
	if taskID != "" {
		metadata["orq_task_id"] = taskID
	}
	return m.client.CreateSession(ctx, m.dir, CreateRequest{
		ParentID: rootID,
		Title:    ChildTitle(role, taskID),
		Metadata: metadata,
	})
}

// RootTitle names a run's root session. The flow ref comes first because that
// is what an operator scanning the TUI session list recognizes.
func RootTitle(flowRef, runID string) string {
	switch {
	case flowRef == "" && runID == "":
		return "orq-lite run"
	case flowRef == "":
		return "orq-lite run " + runID
	case runID == "":
		return "orq-lite " + flowRef
	default:
		return "orq-lite " + flowRef + " " + runID
	}
}

// ChildTitle names one agent invocation, e.g. "coder · T001".
func ChildTitle(role, taskID string) string {
	if taskID == "" {
		return role
	}
	return role + " · " + taskID
}
