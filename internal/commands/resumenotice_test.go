package commands

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// A flow that stops for approval leaves a non-terminal run behind — exactly the
// state an operator would want to resume rather than relaunch.
const approvalFlow = `{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"pauses","version":"1"},"steps":[{"id":"wait","uses":"activity:human.wait_for_approval@1","with":{"reason":"needs a human"}}]}`

func TestFlowRunNoticesAnExistingUnfinishedRun(t *testing.T) {
	dir := t.TempDir()
	installFixturePack(t, dir, "development", "1", map[string]string{"flows/pauses@1.json": approvalFlow})

	var first bytes.Buffer
	_ = FlowCLI(context.Background(), dir, []string{"run", "development/pauses@1"}, &first)
	if strings.Contains(first.String(), "notice:") {
		t.Fatalf("the first run has nothing to resume: %s", first.String())
	}
	runID := ""
	for _, field := range strings.Fields(first.String()) {
		if strings.HasPrefix(field, "run_id=") {
			runID = strings.TrimPrefix(field, "run_id=")
		}
	}
	if runID == "" {
		t.Fatalf("out=%s", first.String())
	}

	var second bytes.Buffer
	_ = FlowCLI(context.Background(), dir, []string{"run", "development/pauses@1"}, &second)
	if !strings.Contains(second.String(), "orq-lite flow resume "+runID) {
		t.Fatalf("the second run must surface the resume command for %s: %s", runID, second.String())
	}
	// Visibility only: the new run still starts, so semantics are unchanged.
	if !strings.Contains(second.String(), "run_id=") {
		t.Fatalf("the notice must not block the new run: %s", second.String())
	}
	newRunID := ""
	for _, field := range strings.Fields(second.String()) {
		if strings.HasPrefix(field, "run_id=") {
			newRunID = strings.TrimPrefix(field, "run_id=")
		}
	}
	if newRunID == runID {
		t.Fatalf("the second invocation must be its own run, got %s twice", runID)
	}
}

// A finished run of the same flow is not something to resume, so it must not
// produce a notice.
func TestFlowRunHasNoNoticeWhenPriorRunsFinished(t *testing.T) {
	dir := t.TempDir()
	installFixturePack(t, dir, "development", "1", nil)
	for pass := 0; pass < 2; pass++ {
		var out bytes.Buffer
		if err := FlowCLI(context.Background(), dir, []string{"run", "development/probe@1"}, &out); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "notice:") {
			t.Fatalf("pass %d: unexpected notice: %s", pass, out.String())
		}
	}
}
