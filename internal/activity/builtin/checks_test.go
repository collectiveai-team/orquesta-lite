package builtin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/checks"
)

func checkTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "source.txt"), []byte("original\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "test@example.test"}, {"config", "user.name", "test"}, {"add", "."}, {"commit", "-qm", "initial"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("git %v: %v %s", args, e, b)
		}
	}
	return dir
}
func TestChecksEvidenceDeletionBlocksWithoutReplay(t *testing.T) {
	repo := checkTestRepo(t)
	root := t.TempDir()
	ctx := context.Background()
	prepare := &ChecksPrepareExecutor{ProjectDir: repo, ArtifactRoot: root}
	req := activity.Request{RunID: "run", StepID: "freeze", Attempt: 1, Inputs: json.RawMessage(`{}`)}
	result, err := prepare.Execute(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err = prepare.ValidateEvidence(ctx, req, result); err != nil {
		t.Fatal(err)
	}
	var prepared checksPrepareOutput
	if err = json.Unmarshal(result.Output, &prepared); err != nil {
		t.Fatal(err)
	}
	profile := &checks.Resolved{SourceHash: checks.Hash([]byte("profile")), Profile: checks.Profile{Items: []checks.Check{{ID: "unit", Argv: []string{"sh", "-c", "cat source.txt"}, Required: true, Tier: 1, TimeoutSeconds: 2}}}}
	executor := &ChecksExecutor{ProjectDir: repo, ArtifactRoot: root, Profile: profile}
	inputs, _ := json.Marshal(checksInput{Event: "pr", MaxTier: 1, Snapshot: &prepared.Snapshot})
	request := activity.Request{RunID: "run", StepID: "checks", ScopePath: "unit", Attempt: 1, Inputs: inputs}
	run, err := executor.Execute(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err = executor.ValidateEvidence(ctx, request, run); err != nil {
		t.Fatal(err)
	}
	var output ChecksOutput
	if err = json.Unmarshal(run.Output, &output); err != nil {
		t.Fatal(err)
	}
	path := output.Evidence[0].Path
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = executor.ValidateEvidence(ctx, request, run); err == nil {
		t.Fatal("deleted evidence accepted")
	}
	source, _ := os.ReadFile(filepath.Join(repo, "source.txt"))
	if string(source) != "original\n" {
		t.Fatal("checks touched live tree")
	}
}
func TestChecksSelectionCannotBeRemovedFromOutput(t *testing.T) {
	repo := checkTestRepo(t)
	root := t.TempDir()
	ctx := context.Background()
	s, err := checks.Freeze(ctx, repo, filepath.Join(root, "snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	profile := &checks.Resolved{SourceHash: checks.Hash([]byte("profile")), Profile: checks.Profile{Items: []checks.Check{{ID: "required", Argv: []string{"true"}, Required: true, Tier: 1}}}}
	executor := &ChecksExecutor{ProjectDir: repo, ArtifactRoot: root, Profile: profile}
	raw, _ := json.Marshal(ChecksOutput{Passed: true, Snapshot: s, ProfileHash: profile.SourceHash, Selected: []string{}, Evidence: []CheckEvidence{}})
	err = executor.ValidateEvidence(ctx, activity.Request{Inputs: json.RawMessage(`{}`)}, activity.Result{Output: raw})
	if err == nil {
		t.Fatal("accepted missing required selected check")
	}
}

func TestChecksReportOnlyAuthenticatesFailedEvidence(t *testing.T) {
	repo := checkTestRepo(t)
	root := t.TempDir()
	ctx := context.Background()
	profile := &checks.Resolved{SourceHash: checks.Hash([]byte("profile")), Profile: checks.Profile{Items: []checks.Check{{ID: "failure", Argv: []string{"sh", "-c", "exit 7"}, Required: true, Tier: 1}}}}
	executor := &ChecksExecutor{ProjectDir: repo, ArtifactRoot: root, Profile: profile}
	request := activity.Request{RunID: "run", StepID: "checks", Attempt: 1, Inputs: json.RawMessage(`{"report_only":true}`)}
	result, err := executor.Execute(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err = executor.ValidateEvidence(ctx, request, result); err != nil {
		t.Fatal(err)
	}
	if VerifyOutput(result.Output) == nil {
		t.Fatal("failed report closed unit")
	}
	var out ChecksOutput
	if err = json.Unmarshal(result.Output, &out); err != nil {
		t.Fatal(err)
	}
	if out.Passed {
		t.Fatal("failed check marked passed")
	}
	if err = os.Remove(out.Evidence[0].Path); err != nil {
		t.Fatal(err)
	}
	if executor.ValidateEvidence(ctx, request, result) == nil {
		t.Fatal("report-only ignored deleted failed evidence")
	}
}

func TestChecksCoverageReportMustRemainVerifiable(t *testing.T) {
	repo := checkTestRepo(t)
	root := t.TempDir()
	ctx := context.Background()
	profile := &checks.Resolved{SourceHash: checks.Hash([]byte("profile")), Profile: checks.Profile{
		Items:    []checks.Check{{ID: "coverage", Argv: []string{"sh", "-c", "printf 'mode: set\\nsource.txt:1.1,1.2 1 1\\n' > coverage.out"}, Required: true, Tier: 1}},
		Coverage: checks.Coverage{Threshold: 80, CheckID: "coverage", Report: "coverage.out", Format: "go-cover"},
	}}
	executor := &ChecksExecutor{ProjectDir: repo, ArtifactRoot: root, Profile: profile}
	request := activity.Request{RunID: "run", StepID: "checks", Attempt: 1, Inputs: json.RawMessage(`{}`)}
	result, err := executor.Execute(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err = executor.ValidateEvidence(ctx, request, result); err != nil {
		t.Fatal(err)
	}
	var output ChecksOutput
	if err = json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.CoverageReport == "" || output.CoverageHash == "" {
		t.Fatal("coverage report was not captured")
	}
	if err = os.Remove(output.CoverageReport); err != nil {
		t.Fatal(err)
	}
	if executor.ValidateEvidence(ctx, request, result) == nil || VerifyOutput(result.Output) == nil {
		t.Fatal("deleted coverage report accepted")
	}
}

func TestChecksCoverageCannotReuseSourceReport(t *testing.T) {
	repo := checkTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "coverage.out"), []byte("mode: set\nsource.txt:1.1,1.2 1 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	profile := &checks.Resolved{SourceHash: checks.Hash([]byte("profile")), Profile: checks.Profile{
		Items:    []checks.Check{{ID: "coverage", Argv: []string{"true"}, Required: true, Tier: 1}},
		Coverage: checks.Coverage{Threshold: 80, CheckID: "coverage", Report: "coverage.out", Format: "go-cover"},
	}}
	executor := &ChecksExecutor{ProjectDir: repo, ArtifactRoot: t.TempDir(), Profile: profile}
	request := activity.Request{RunID: "run", StepID: "checks", Attempt: 1, Inputs: json.RawMessage(`{"report_only":true}`)}
	result, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var output ChecksOutput
	if err = json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.Passed || output.CoverageError == "" {
		t.Fatal("stale source report accepted as execution evidence")
	}
	if err = executor.ValidateEvidence(context.Background(), request, result); err != nil {
		t.Fatal(err)
	}
}
