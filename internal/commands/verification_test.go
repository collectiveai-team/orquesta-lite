package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/activity/builtin"
	"github.com/collectiveai-team/orquesta-lite/internal/checks"
	"github.com/collectiveai-team/orquesta-lite/internal/review"
	"github.com/collectiveai-team/orquesta-lite/internal/workflow"
)

func verifiedProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("source.txt", "good\n")
	write("verify.sh", "grep -q '^good$' source.txt\n")
	write("features.md", "Keep the source valid.\n")
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "core.hooksPath=/dev/null", "commit", "-qm", "base"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if raw, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, raw)
		}
	}
	profile, err := checks.Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	profile.Items = []checks.Check{{ID: "unit-source", Kind: "unit", Surface: "shared", Argv: []string{"sh", "verify.sh"}, Required: true, Tier: 1, Paths: []string{"source.txt"}}}
	raw, _ := json.Marshal(profile)
	write(checks.DefaultPath, string(raw))
	write("team.json", string(mustReadAsset("assets/team.json")))
	return dir
}

func verifiedTarget(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(governedPackRoot(t), "flows", name+"@1.json")
}

func TestVerificationCompilesPinnedProfileAndModels(t *testing.T) {
	dir := verifiedProject(t)
	compiled, err := compileWorkflowTarget(dir, verifiedTarget(t, "verified-development"))
	if err != nil {
		t.Fatal(err)
	}
	v := compiled.IR.Verification
	if v == nil || len(v.ResolvedChecks.SourceHash) != 64 || len(v.ResolvedRoles) != 3 {
		t.Fatalf("missing pinned resources: %+v", v)
	}
	if v.ResolvedRoles["verified_reviewer"].Agents[0].Model != "gpt-5.6-terra" {
		t.Fatal("model not pinned")
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"test":"changed"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := compileWorkflowTarget(dir, verifiedTarget(t, "verified-development")); err == nil || !strings.Contains(err.Error(), "manifests changed") {
		t.Fatalf("stale manifests accepted: %v", err)
	}
}

type verifiedFakeAgent struct {
	project         string
	implementations int
	reviews         int
}

func (*verifiedFakeAgent) Spec() activity.Spec {
	return activity.Spec{Name: "agent.invoke", Version: "1", Effect: activity.EffectAtMostOnce}
}
func (e *verifiedFakeAgent) Execute(_ context.Context, r activity.Request) (activity.Result, error) {
	var in struct {
		Role     string          `json:"role"`
		Snapshot checks.Snapshot `json:"snapshot"`
		Context  map[string]any  `json:"context"`
	}
	if err := json.Unmarshal(r.Inputs, &in); err != nil {
		return activity.Result{}, err
	}
	if in.Role == "verified_coder" {
		e.implementations++
		if err := os.WriteFile(filepath.Join(e.project, "source.txt"), []byte("good\nadded\n"), 0600); err != nil {
			return activity.Result{}, err
		}
		return activity.Result{Output: json.RawMessage(`{"continue":false,"summary":"implemented","remaining":[]}`)}, nil
	}
	e.reviews++
	lens, _ := in.Context["LENS"].(string)
	claims := review.ClaimsV3{Version: 3, Lens: lens, Status: "complete", Summary: "No confirmed defects", Findings: []review.ClaimV3{}, Limitations: []string{}, ReviewedRevision: in.Snapshot.TreeHash, Evidence: []string{"snapshot:" + in.Snapshot.TreeHash}}
	if evidence, ok := in.Context["CHECK_EVIDENCE"].(map[string]any); ok {
		if items, ok := evidence["evidence"].([]any); ok && len(items) > 0 {
			if item, ok := items[0].(map[string]any); ok {
				if path, ok := item["path"].(string); ok {
					claims.Evidence = append(claims.Evidence, "check:"+path)
				}
			}
		}
	}
	raw, _ := json.Marshal(claims)
	return activity.Result{Output: raw}, nil
}

func TestVerifiedRunClosesAndLostEvidenceNeverReplaysImplementation(t *testing.T) {
	ctx := context.Background()
	dir := verifiedProject(t)
	compiled, err := compileWorkflowTarget(dir, verifiedTarget(t, "verified-development"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := workflow.Open(filepath.Join(dir, ".orquestalite", "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	registry := activity.NewRegistry()
	id := "verified-test"
	if err := registerVerification(dir, id, compiled, registry, store); err != nil {
		t.Fatal(err)
	}
	fake := &verifiedFakeAgent{project: dir}
	for _, executor := range []activity.Executor{fake, builtin.GateAssertExecutor{}} {
		if err := registry.Register(executor); err != nil {
			t.Fatal(err)
		}
	}
	runtime := &workflow.Runtime{Store: store, Activities: registry, Catalog: compiled.Catalog}
	run, err := runtime.Start(ctx, compiled.IR, workflow.StartOptions{RunID: id, SourceKey: "verified-trigger", Inputs: map[string]any{"features_path": "features.md"}, Policy: workflow.Policy{MaxAttempts: 100}})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflow.RunSucceeded || fake.implementations != 1 || fake.reviews != 1 {
		t.Fatalf("status=%s implementations=%d reviews=%d", run.Status, fake.implementations, fake.reviews)
	}
	if err := installBuiltinDevelopmentPack(dir); err != nil {
		t.Fatal(err)
	}
	var reused bytes.Buffer
	if err := FlowCLI(ctx, dir, []string{"run", "ignored-on-reused-trigger", "--source-key=verified-trigger"}, &reused); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reused.String(), "run_id="+id) || !strings.Contains(reused.String(), "reused=true") {
		t.Fatalf("source key did not reuse pinned run: %s", reused.String())
	}
	steps, err := store.ListSteps(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	receipt := ""
	for _, step := range steps {
		if activity.ExecutorRef(step.ActivityRef) == "checks.run@1" {
			var out builtin.ChecksOutput
			if err := json.Unmarshal(step.Output, &out); err != nil {
				t.Fatal(err)
			}
			receipt = out.Evidence[0].Path
		}
	}
	if receipt == "" {
		t.Fatal("no runtime check evidence")
	}
	if err := os.Remove(receipt); err != nil {
		t.Fatal(err)
	}
	run, err = runtime.Resume(ctx, id)
	if !errors.Is(err, workflow.ErrNeedsHuman) || run.Status != workflow.RunNeedsHuman {
		t.Fatalf("missing evidence did not invalidate approval: run=%+v err=%v", run, err)
	}
	if fake.implementations != 1 || fake.reviews != 1 {
		t.Fatal("lost evidence repeated a completed product effect")
	}
}

func TestChecksDetectRequiresExplicitConfirmation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"test":"touch SHOULD_NOT_RUN"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := ChecksCLI(context.Background(), dir, []string{"detect"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, checks.DefaultPath)); !os.IsNotExist(err) {
		t.Fatal("detect silently approved a profile")
	}
	if _, err := os.Stat(filepath.Join(dir, "SHOULD_NOT_RUN")); !os.IsNotExist(err) {
		t.Fatal("detect executed a script")
	}
	proposal := filepath.Join(dir, "proposal.json")
	if err := os.WriteFile(proposal, out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := ChecksCLI(context.Background(), dir, []string{"confirm", proposal}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := checks.Resolve(dir, checks.DefaultPath); err != nil {
		t.Fatal(err)
	}
}
