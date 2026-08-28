package opencodeattach

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeServer stands in for `opencode serve`, recording every session-creation
// body so a test can assert the shape of the tree that was built.
type fakeServer struct {
	*httptest.Server

	mu       sync.Mutex
	created  []CreateRequest
	dirs     []string
	notes    map[string]string
	nextID   int
	pingCode int
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{pingCode: http.StatusOK, notes: map[string]string{}}
	mux := http.NewServeMux()
	// POST /session/{id}/message — the note endpoint. Records the text and
	// asserts the caller asked for noReply, since a note that provokes a model
	// turn would bill the user for a container session.
	mux.HandleFunc("/session/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 3 || parts[2] != "message" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			NoReply bool `json:"noReply"`
			Parts   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"parts"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !body.NoReply {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		for _, p := range body.Parts {
			f.notes[parts[1]] += p.Text
		}
		f.mu.Unlock()
		_, _ = io.WriteString(w, "{}")
	})
	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(f.pingCode)
			_, _ = io.WriteString(w, "[]")
			return
		}
		var req CreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.nextID++
		id := "ses_test" + string(rune('0'+f.nextID))
		f.created = append(f.created, req)
		f.dirs = append(f.dirs, r.URL.Query().Get("directory"))
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeServer) requests() []CreateRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]CreateRequest(nil), f.created...)
}

func TestNormalizeURLRejectsMalformed(t *testing.T) {
	for _, tc := range []struct{ name, url string }{
		{"empty", ""},
		{"whitespace", "   "},
		{"no scheme", "127.0.0.1:4096"},
		{"wrong scheme", "ftp://127.0.0.1:4096"},
		{"no host", "http://"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NormalizeURL(tc.url); err == nil {
				t.Fatalf("NormalizeURL(%q) accepted a malformed URL", tc.url)
			}
		})
	}
}

func TestNormalizeURLStripsTrailingSlash(t *testing.T) {
	got, err := NormalizeURL("http://127.0.0.1:4096/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://127.0.0.1:4096" {
		t.Fatalf("trailing slash not stripped: got %q", got)
	}
}

func TestPingReportsUnreachableServer(t *testing.T) {
	// Port 1 on loopback refuses connections, standing in for "no server".
	client, err := NewClient("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	err = client.Ping(context.Background())
	if err == nil {
		t.Fatal("Ping succeeded against a server that is not running")
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("error should name the failure mode, got %q", err)
	}
}

func TestPingRejectsNon2xx(t *testing.T) {
	f := newFakeServer(t)
	f.pingCode = http.StatusInternalServerError
	client, err := NewClient(f.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Ping(context.Background()); err == nil {
		t.Fatal("Ping accepted an HTTP 500 as a healthy server")
	}
}

// TestManagerBuildsOneRootPerRun is the core structural claim: however many
// agent invocations a run makes, they all hang off a single root session.
func TestManagerBuildsOneRootPerRun(t *testing.T) {
	f := newFakeServer(t)
	client, err := NewClient(f.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(client, "/proj", "run-42", "development/factory-fast@1")

	first, err := m.ChildSession(context.Background(), "T001", "coder", "opencode-sonnet")
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.ChildSession(context.Background(), "T002", "qa", "opencode-sonnet")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two invocations were given the same session id")
	}

	reqs := f.requests()
	if len(reqs) != 3 {
		t.Fatalf("want 1 root + 2 children = 3 creates, got %d", len(reqs))
	}
	if reqs[0].ParentID != "" {
		t.Errorf("first create should be the root, got parentID=%q", reqs[0].ParentID)
	}
	rootID := m.RootID()
	if rootID == "" {
		t.Fatal("manager did not retain the root session id")
	}
	for i, child := range reqs[1:] {
		if child.ParentID != rootID {
			t.Errorf("child %d parented to %q, want root %q", i, child.ParentID, rootID)
		}
	}
}

func TestManagerTitlesAndMetadata(t *testing.T) {
	f := newFakeServer(t)
	client, err := NewClient(f.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(client, "/proj", "run-42", "development/factory-fast@1")
	if _, err := m.ChildSession(context.Background(), "T001", "coder", "opencode-sonnet"); err != nil {
		t.Fatal(err)
	}

	reqs := f.requests()
	root, child := reqs[0], reqs[1]

	if !strings.Contains(root.Title, "run-42") || !strings.Contains(root.Title, "factory-fast") {
		t.Errorf("root title %q should name the flow and the run", root.Title)
	}
	if root.Metadata["orq_run_id"] != "run-42" {
		t.Errorf("root metadata missing run id: %v", root.Metadata)
	}
	if child.Title != "coder · T001" {
		t.Errorf("child title = %q, want %q", child.Title, "coder · T001")
	}
	for key, want := range map[string]string{
		"orq_run_id":  "run-42",
		"orq_task_id": "T001",
		"orq_role":    "coder",
		"orq_agent":   "opencode-sonnet",
	} {
		if got := child.Metadata[key]; got != want {
			t.Errorf("child metadata[%s] = %v, want %q", key, got, want)
		}
	}
	// The server resolves paths on its own side, so the project directory has
	// to travel with the request rather than being implied by our cwd.
	if f.dirs[0] != "/proj" || f.dirs[1] != "/proj" {
		t.Errorf("directory not sent on create: %v", f.dirs)
	}
}

// TestManagerNoRootUntilUsed keeps a run whose roles never touch the opencode
// provider from littering the user's session list with an empty root.
func TestManagerNoRootUntilUsed(t *testing.T) {
	f := newFakeServer(t)
	client, err := NewClient(f.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(client, "/proj", "run-42", "flow")
	if got := m.RootID(); got != "" {
		t.Fatalf("root created before any child was requested: %q", got)
	}
	if n := len(f.requests()); n != 0 {
		t.Fatalf("manager issued %d creates before being used", n)
	}
}

func TestManagerConcurrentChildrenShareOneRoot(t *testing.T) {
	f := newFakeServer(t)
	client, err := NewClient(f.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(client, "/proj", "run-42", "flow")

	// foreach steps invoke roles in parallel; a racy lazy root would create
	// several and scatter the tree.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.ChildSession(context.Background(), "T", "coder", "a"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	roots := 0
	for _, req := range f.requests() {
		if req.ParentID == "" {
			roots++
		}
	}
	if roots != 1 {
		t.Fatalf("want exactly 1 root across concurrent children, got %d", roots)
	}
}

func TestCreateSessionSurfacesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"Unknown workspace adapter"}`)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CreateSession(context.Background(), "/proj", CreateRequest{Title: "x"})
	if err == nil {
		t.Fatal("CreateSession swallowed an HTTP 400")
	}
	if !strings.Contains(err.Error(), "Unknown workspace adapter") {
		t.Fatalf("error should carry the server's message, got %q", err)
	}
}

// TestManagerWritesRootNote covers the defect measured in the opencode TUI: a
// root with no messages is not just blank, it registers as a run that started
// and never finished. No agent ever runs in the root, so the only way it holds
// content is if the manager puts it there.
func TestManagerWritesRootNote(t *testing.T) {
	f := newFakeServer(t)
	client, err := NewClient(f.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(client, "/proj", "run-42", "flow:factory-fast@1")
	if _, err := m.ChildSession(context.Background(), "T001", "coder", "a"); err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	note := f.notes[m.RootID()]
	if note == "" {
		t.Fatal("run root was left with no messages — the TUI shows it as perpetually in-flight")
	}
	for _, want := range []string{"run-42", "flow:factory-fast@1", "/proj"} {
		if !strings.Contains(note, want) {
			t.Errorf("root note is missing %q: %q", want, note)
		}
	}
	// Only the root is a container; role sessions get their content from the
	// agent that runs in them and must not be pre-seeded.
	if len(f.notes) != 1 {
		t.Errorf("notes written to %d sessions, want only the root", len(f.notes))
	}
}

// A server that accepts the session create but rejects the note leaves an empty
// root behind — the exact state the note prevents — so it must not pass silently.
func TestManagerFailsWhenRootNoteRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/message") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "ses_x"})
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(client, "/proj", "run-42", "flow")
	if _, err := m.ChildSession(context.Background(), "T001", "coder", "a"); err == nil {
		t.Fatal("a rejected root note was swallowed, leaving an empty root")
	}
}

func TestChildTitleWithoutTask(t *testing.T) {
	if got := ChildTitle("critic", ""); got != "critic" {
		t.Fatalf("ChildTitle with no task = %q, want %q", got, "critic")
	}
}
