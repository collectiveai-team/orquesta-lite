//go:build live

// Live end-to-end checks against a real `opencode serve` and the real opencode
// CLI. Excluded from the normal suite by the `live` build tag: they need a
// running server and working provider credentials, so they cannot be part of a
// hermetic run. They exist because the unit tests all stub either the server or
// the CLI, and the claims that matter most here — that `-s` on a pre-created
// session is honoured, and that an abort is distinguishable from success — are
// claims about software this repo does not own.
//
//	opencode serve --hostname 127.0.0.1 --port 4096 &
//	ORQ_ATTACH_URL=http://127.0.0.1:4096 go test -tags live ./internal/opencodeattach/ -v -count=1
package opencodeattach_test

import (
	"context"
	"encoding/json"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/opencodeattach"
	"github.com/collectiveai-team/orquesta-lite/internal/runner"
)

func serverURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("ORQ_ATTACH_URL")
	if url == "" {
		t.Skip("ORQ_ATTACH_URL not set")
	}
	return url
}

func getJSON(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}

type session struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	ParentID string         `json:"parentID"`
	Metadata map[string]any `json:"metadata"`
}

// TestLiveAttachEndToEnd runs the real CLI through a pre-minted child session
// and then asserts the resulting tree from the server's own view.
func TestLiveAttachEndToEnd(t *testing.T) {
	url := serverURL(t)
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}

	client, err := opencodeattach.NewClient(url)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}

	runID := "live-" + time.Now().Format("150405")
	mgr := opencodeattach.NewManager(client, dir, runID, "live/probe@1")

	childID, err := mgr.ChildSession(context.Background(), "T001", "coder", "opencode-live")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("root=%s child=%s", mgr.RootID(), childID)

	// Drive the real opencode CLI through the pre-created session.
	resultPath := filepath.Join(t.TempDir(), "result.json")
	res, err := runner.RunAgent(context.Background(), runner.Spec{
		Provider:        "opencode",
		Prompt:          "Reply with exactly the single word PONG. Do not use any tools.",
		ResultPath:      resultPath,
		Timeout:         120 * time.Second,
		AttachURL:       url,
		AttachDir:       dir,
		ResumeSessionID: childID,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("exit=%d aborted=%v session=%q final=%q stderr=%q",
		res.ExitCode, res.Aborted, res.SessionID, res.FinalText, res.StderrTail(400))

	if res.ExitCode != 0 {
		t.Fatalf("real opencode run failed: exit=%d stderr=%s", res.ExitCode, res.StderrTail(2000))
	}
	if res.Aborted {
		t.Fatal("run reported as aborted")
	}
	// The id the CLI reports must be the one we minted: that equality is the
	// whole inversion. If they differ, the CLI ignored -s and made its own
	// session, and the tree is a fiction.
	if res.SessionID != childID {
		t.Fatalf("CLI ran in session %q, not the minted child %q", res.SessionID, childID)
	}
	if res.FinalText == "" {
		t.Fatal("no assistant text came back; the prompt did not round-trip")
	}

	// Server-side view: the child must be under our root, and must NOT appear
	// in the roots listing that the TUI's session list uses.
	var children []session
	getJSON(t, url+"/session/"+mgr.RootID()+"/children", &children)
	found := false
	for _, c := range children {
		if c.ID == childID {
			found = true
			if c.Title != "coder · T001" {
				t.Errorf("child title = %q, want %q", c.Title, "coder · T001")
			}
			if c.Metadata["orq_run_id"] != runID {
				t.Errorf("child metadata lost the run id: %v", c.Metadata)
			}
		}
	}
	if !found {
		t.Fatalf("child %s is not listed under root %s", childID, mgr.RootID())
	}

	// roots=true is scoped by directory: a session created for /proj shows up in
	// that project's listing, not in whatever directory the server was started
	// in. The query must therefore name the same directory that was used to
	// create the session, which is also what the TUI does when opened there.
	var roots []session
	getJSON(t, url+"/session?roots=true&directory="+neturl.QueryEscape(dir), &roots)
	var rootSeen bool
	for _, r := range roots {
		if r.ID == childID {
			t.Error("child session appears in the roots listing — it would still clutter the TUI list")
		}
		if r.ID == mgr.RootID() {
			rootSeen = true
		}
	}
	if !rootSeen {
		t.Error("run root is missing from the roots listing — it would be invisible in the TUI")
	}
}

// TestLiveAbortIsDetected proves the exit-code finding on the real CLI: abort
// the session mid-flight and confirm the run is reported as aborted even though
// the process exits 0.
func TestLiveAbortIsDetected(t *testing.T) {
	url := serverURL(t)
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	client, err := opencodeattach.NewClient(url)
	if err != nil {
		t.Fatal(err)
	}
	mgr := opencodeattach.NewManager(client, dir, "live-abort", "live/probe@1")
	childID, err := mgr.ChildSession(context.Background(), "T002", "coder", "opencode-live")
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(12 * time.Second)
		req, _ := http.NewRequest(http.MethodPost, url+"/session/"+childID+"/abort", nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()

	res, err := runner.RunAgent(context.Background(), runner.Spec{
		Provider:        "opencode",
		Prompt:          "Write a detailed 3000 word essay on the history of the Roman Republic. Do not use any tools.",
		ResultPath:      filepath.Join(t.TempDir(), "result.json"),
		Timeout:         120 * time.Second,
		AttachURL:       url,
		AttachDir:       dir,
		ResumeSessionID: childID,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("exit=%d aborted=%v timedout=%v", res.ExitCode, res.Aborted, res.TimedOut)

	if res.TimedOut {
		t.Fatal("run hit the orq-lite timeout instead of noticing the abort")
	}
	if res.ExitCode != 0 {
		t.Logf("note: aborted run exited %d (measured 0 on opencode 1.18.18)", res.ExitCode)
	}
	if !res.Aborted {
		t.Fatal("a server-side abort was not detected — cancellation reads as an ordinary empty run")
	}
}
