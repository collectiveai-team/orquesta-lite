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

func comparisonRepo(t *testing.T, initial string) (string, string) {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.name", "comparison-test"}, {"config", "user.email", "comparison@example.test"}} {
		command := exec.Command("git", args...)
		command.Dir = repo
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "source.txt"), []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "source.txt"}, {"commit", "-qm", "base"}} {
		command := exec.Command("git", args...)
		command.Dir = repo
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, output)
		}
	}
	root := filepath.Join(repo, ".orquestalite", "runs", "compare")
	return repo, root
}

func TestReviewComparisonAttributionAndEvidenceLoss(t *testing.T) {
	for _, test := range []struct{ name, base, candidate, want string }{{"introduced", "ok\n", "bad\n", "introduced"}, {"preexisting", "bad\n", "bad\n", "preexisting"}, {"resolved", "bad\n", "ok\n", "unknown"}} {
		t.Run(test.name, func(t *testing.T) {
			repo, root := comparisonRepo(t, test.base)
			if err := os.WriteFile(filepath.Join(repo, "source.txt"), []byte(test.candidate), 0600); err != nil {
				t.Fatal(err)
			}
			snapshot, err := checks.FreezeBase(context.Background(), repo, filepath.Join(root, "snapshots", "candidate"), "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			profile := &checks.Resolved{SourceHash: "approved", Profile: checks.Profile{Items: []checks.Check{{ID: "unit", Argv: []string{"sh", "-c", "grep -q '^ok$' source.txt"}, TimeoutSeconds: 5}}}}
			executor := &ReviewCompareExecutor{ProjectDir: repo, ArtifactRoot: root, Profile: profile}
			raw, _ := json.Marshal(comparisonInput{Snapshot: snapshot, CheckID: "unit"})
			request := activity.Request{RunID: "compare", ScopePath: "unit", StepID: "comparison", Attempt: 1, Inputs: raw}
			result, err := executor.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if err := executor.ValidateEvidence(context.Background(), request, result); err != nil {
				t.Fatal(err)
			}
			var comparison comparisonEvidence
			if err := json.Unmarshal(result.Output, &comparison); err != nil {
				t.Fatal(err)
			}
			if comparison.Causal != test.want {
				t.Fatalf("causal=%s want %s", comparison.Causal, test.want)
			}
			proof, err := ResolveComparisonProof(root, snapshot, comparison.Reference)
			if err != nil {
				t.Fatal(err)
			}
			if proof.Subject != "unit" || proof.Causal != test.want {
				t.Fatalf("proof=%+v", proof)
			}
			if err := os.Remove(comparison.CandidateReceipt.Path); err != nil {
				t.Fatal(err)
			}
			if _, err := ResolveComparisonProof(root, snapshot, comparison.Reference); err == nil {
				t.Fatal("missing evidence accepted")
			}
		})
	}
}

func TestReviewComparisonRejectsCommandsAndForeignTrees(t *testing.T) {
	repo, root := comparisonRepo(t, "ok\n")
	snapshot, err := checks.Freeze(context.Background(), repo, filepath.Join(root, "snapshots", "candidate"))
	if err != nil {
		t.Fatal(err)
	}
	profile := &checks.Resolved{SourceHash: "approved", Profile: checks.Profile{Items: []checks.Check{}}}
	executor := &ReviewCompareExecutor{ProjectDir: repo, ArtifactRoot: root, Profile: profile}
	for _, raw := range [][]byte{[]byte(`{"check_id":"unit","argv":["sh"]}`), func() []byte {
		raw, _ := json.Marshal(comparisonInput{Snapshot: snapshot, CheckID: "unknown"})
		return raw
	}()} {
		if _, err := executor.Execute(context.Background(), activity.Request{Inputs: raw, Attempt: 1}); err == nil {
			t.Fatal("unapproved comparison accepted")
		}
	}
	if _, err := ResolveComparisonProof(root, snapshot, "compare:/tmp/foreign.json"); err == nil {
		t.Fatal("foreign receipt accepted")
	}
}
