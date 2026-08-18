package commands

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/watch"
)

func TestWatch_V2FailsFastOnMissingFlow(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := Watch(ctx, WatchOptions{
		ProjectDir: dir,
		Issues:     true,
		Out:        io.Discard,
	})
	if err == nil {
		t.Fatal("expected startup error for missing v2 flow")
	}
	if !strings.Contains(err.Error(), "development/issue-fix@1") {
		t.Fatalf("error should name the flow ref, got: %v", err)
	}
}

func issueTrigger() watch.Trigger {
	return watch.Trigger{FlowRef: "development/issue-fix@1", Inputs: map[string]any{
		"type": watch.ItemIssue, "number": "27", "title": "Crash on empty payload",
		"body": "steps to reproduce", "author": "octocat", "updated_at": time.Now(),
	}}
}

func prTrigger() watch.Trigger {
	return watch.Trigger{FlowRef: "development/pr-review@1", Inputs: map[string]any{
		"type": watch.ItemPR, "number": "31", "title": "Add retry", "body": "",
		"author": "octocat", "updated_at": time.Now(),
	}}
}

// The watch loop reports generic GitHub fields; issue-fix@1 only declares
// issue_path and run. Forwarding the raw payload used to fail every trigger with
// `unknown input "author"`.
func TestWatchFlowInputs_IssueNarrowsToPackContract(t *testing.T) {
	dir := t.TempDir()
	declared := map[string]bool{"issue_path": true, "run": true}

	inputs, err := watchFlowInputs(WatchOptions{ProjectDir: dir}, issueTrigger(), declared)
	if err != nil {
		t.Fatalf("watchFlowInputs: %v", err)
	}

	want := filepath.Join(".orquestalite", "watch-issue-27.md")
	if inputs["issue_path"] != want {
		t.Fatalf("issue_path = %v, want %v", inputs["issue_path"], want)
	}
	for _, key := range []string{"author", "title", "body", "number", "type", "updated_at"} {
		if _, present := inputs[key]; present {
			t.Errorf("undeclared input %q was forwarded", key)
		}
	}

	raw, err := os.ReadFile(filepath.Join(dir, want))
	if err != nil {
		t.Fatalf("issue file not written: %v", err)
	}
	if got := string(raw); !strings.Contains(got, "Crash on empty payload") ||
		!strings.Contains(got, "steps to reproduce") {
		t.Fatalf("issue file should carry title and body, got: %q", got)
	}
}

func TestWatchFlowInputs_PRMapsNumberAndPublish(t *testing.T) {
	declared := map[string]bool{"pr": true, "base": true, "head": true, "publish": true}

	inputs, err := watchFlowInputs(WatchOptions{ProjectDir: t.TempDir(), PublishPRs: true}, prTrigger(), declared)
	if err != nil {
		t.Fatalf("watchFlowInputs: %v", err)
	}

	if inputs["pr"] != "31" {
		t.Errorf("pr = %v, want \"31\"", inputs["pr"])
	}
	if inputs["publish"] != true {
		t.Errorf("publish = %v, want true (--publish was set)", inputs["publish"])
	}
	if _, present := inputs["author"]; present {
		t.Error("undeclared input \"author\" was forwarded")
	}
}

// The narrowing is driven by the flow's own IR, not a hardcoded allow-list, so a
// custom watch flow that does declare the generic fields still receives them.
func TestWatchFlowInputs_KeepsGenericFieldsWhenDeclared(t *testing.T) {
	declared := map[string]bool{"title": true, "author": true, "number": true}

	inputs, err := watchFlowInputs(WatchOptions{ProjectDir: t.TempDir()}, prTrigger(), declared)
	if err != nil {
		t.Fatalf("watchFlowInputs: %v", err)
	}

	if inputs["title"] != "Add retry" || inputs["author"] != "octocat" || inputs["number"] != "31" {
		t.Fatalf("declared generic inputs should survive, got: %v", inputs)
	}
	if _, present := inputs["publish"]; present {
		t.Error("publish is not declared by this flow and must be dropped")
	}
}

func TestWatchFlowInputs_SkipsIssueFileWhenPathNotDeclared(t *testing.T) {
	dir := t.TempDir()

	inputs, err := watchFlowInputs(WatchOptions{ProjectDir: dir}, issueTrigger(), map[string]bool{"body": true})
	if err != nil {
		t.Fatalf("watchFlowInputs: %v", err)
	}

	if inputs["body"] != "steps to reproduce" {
		t.Fatalf("body = %v, want the issue body", inputs["body"])
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, ".orquestalite")); len(entries) != 0 {
		t.Errorf("no issue file should be written when issue_path is not declared, got %d", len(entries))
	}
}
