package commands

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/activity/builtin"
	"github.com/collectiveai-team/orquesta-lite/internal/checks"
	"github.com/collectiveai-team/orquesta-lite/internal/config"
	"github.com/collectiveai-team/orquesta-lite/internal/eventlog"
	"github.com/collectiveai-team/orquesta-lite/internal/flow"
	"github.com/collectiveai-team/orquesta-lite/internal/providers"
	"github.com/collectiveai-team/orquesta-lite/internal/review"
	"github.com/collectiveai-team/orquesta-lite/internal/risk"
	"github.com/collectiveai-team/orquesta-lite/internal/supervision"
	"github.com/collectiveai-team/orquesta-lite/internal/telemetry"
	"github.com/collectiveai-team/orquesta-lite/internal/workflow"
)

func reuseWorkflowTrigger(ctx context.Context, project, key string, out io.Writer) (bool, error) {
	db := filepath.Join(project, ".orquestalite", "workflows.db")
	if _, err := os.Stat(db); os.IsNotExist(err) {
		return false, nil
	}
	store, err := workflow.Open(db)
	if err != nil {
		return false, err
	}
	run, err := store.GetRunBySourceKey(ctx, key)
	store.Close()
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var ir flow.IR
	if err := json.Unmarshal(run.IR, &ir); err != nil {
		return true, err
	}
	root := project
	if ir.Pack != nil {
		root = findPinnedPack(project, ir.Pack)
		if root == "" {
			return true, fmt.Errorf("pinned pack for existing trigger is unavailable")
		}
	}
	compiled := &compiledWorkflow{IR: &ir, Root: root, Catalog: flow.NewDirectoryCatalog(root, builtinSpecs())}
	deps, err := newWorkflowDeps(project, filepath.Join(project, "team.json"), run.ID, compiled, eventlog.Format("auto"))
	if err != nil {
		return true, err
	}
	defer deps.close(context.Background())
	validated, err := deps.runtime.ValidateEvidence(ctx, run.ID)
	if validated != nil {
		fmt.Fprintf(out, "run_id=%s status=%s reused=true artifacts=%s\n", validated.ID, validated.Status, filepath.Join(project, ".orquestalite", "runs", validated.ID))
	}
	return true, err
}

func validateVerificationRoles(project, packRoot string, ir *flow.IR) error {
	v := ir.Verification
	if v == nil {
		return nil
	}
	cfg, err := config.LoadDynamic(filepath.Join(project, "team.json"))
	if err != nil {
		return err
	}
	roles := referencedAgentRoles(ir)
	specs, err := cfg.ResolveRoles(roles)
	if err != nil {
		return err
	}
	v.PromptHashes = map[string]string{}
	for name, spec := range specs {
		binding, ok := v.Roles[name]
		if !ok {
			return fmt.Errorf("verification: role %s requires explicit model bindings", name)
		}
		apply := func(agents []config.AgentSpec) error {
			for i := range agents {
				agent := &agents[i]
				model, ok := binding.Models[agent.Name]
				if !ok {
					return fmt.Errorf("verification: role %s agent %s requires an explicit model", name, agent.Name)
				}
				if agent.Provider == "" {
					return fmt.Errorf("verification: role %s requires a provider adapter to enforce its model", name)
				}
				agent.Model = model
				if binding.Purpose == "review" && !providers.CapabilitiesFor(agent.Provider).ReadOnlySandbox {
					return fmt.Errorf("verification: reviewer %s provider %s cannot guarantee read-only execution", name, agent.Provider)
				}
				if binding.RequireNativeModelControl {
					if err := providers.RequireNativeModelControl(agent.Provider); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err := apply(spec.Agents); err != nil {
			return err
		}
		if err := apply(spec.EscalationLadder); err != nil {
			return err
		}
		specs[name] = spec
		prompt := spec.PromptPath
		if !filepath.IsAbs(prompt) {
			prompt = filepath.Join(project, prompt)
		}
		if ir.Pack != nil && ir.Pack.Name == "development" && strings.HasPrefix(filepath.ToSlash(spec.PromptPath), ".orquestalite/packs/development/") {
			prompt = filepath.Join(packRoot, "prompts", filepath.Base(spec.PromptPath))
		}
		raw, err := os.ReadFile(prompt)
		if err != nil {
			return fmt.Errorf("verification role %s prompt: %w", name, err)
		}
		v.PromptHashes[name] = checks.Hash(raw)
	}
	v.ResolvedRoles = specs
	return ir.Rehash()
}

func registerVerification(project, runID string, compiled *compiledWorkflow, registry *activity.Registry, store *workflow.Store) error {
	v := compiled.IR.Verification
	root := filepath.Join(project, ".orquestalite", "runs", runID)
	var profile *checks.Resolved
	policy := risk.DefaultPolicy()
	limits := risk.DefaultCorrectionLimits()
	if v != nil {
		profile = &v.ResolvedChecks
		policy = v.Risk
		limits = v.Correction
	}
	units := risk.UnitStore{Root: filepath.Join(root, "units")}
	proof := func(ctx context.Context, s checks.Snapshot, ref string) (review.Proof, error) {
		if strings.HasPrefix(ref, "check:") {
			path := strings.TrimPrefix(ref, "check:")
			steps, err := store.ListSteps(ctx, runID)
			if err != nil {
				return review.Proof{}, err
			}
			for _, step := range steps {
				if activity.ExecutorRef(step.ActivityRef) != "checks.run@1" || step.Status != workflow.StepSucceeded {
					continue
				}
				var out builtin.ChecksOutput
				if json.Unmarshal(step.Output, &out) != nil {
					continue
				}
				for _, item := range out.Evidence {
					if item.Path != path {
						continue
					}
					if err := validatePersistedStep(ctx, store, registry, step); err != nil {
						return review.Proof{}, err
					}
					raw, err := os.ReadFile(path)
					if err != nil {
						return review.Proof{}, err
					}
					var receipt checks.Evidence
					if err := json.Unmarshal(raw, &receipt); err != nil {
						return review.Proof{}, err
					}
					return review.Proof{Revision: out.Snapshot.TreeHash, BaseRevision: out.Snapshot.BaseRevision, Kind: "check", Verified: true, Subject: item.CheckID, CheckPassed: receipt.ExitCode == 0 && receipt.Error == "" && !receipt.TimedOut}, nil
				}
			}
			return review.Proof{}, fmt.Errorf("check reference was not produced by this runtime")
		}
		if strings.HasPrefix(ref, "compare:") {
			steps, err := store.ListSteps(ctx, runID)
			if err != nil {
				return review.Proof{}, err
			}
			found := false
			for _, step := range steps {
				if activity.ExecutorRef(step.ActivityRef) != "review.compare@1" || step.Status != workflow.StepSucceeded {
					continue
				}
				var out struct {
					Reference string `json:"reference"`
				}
				if json.Unmarshal(step.Output, &out) == nil && out.Reference == ref {
					if err := validatePersistedStep(ctx, store, registry, step); err != nil {
						return review.Proof{}, err
					}
					found = true
					break
				}
			}
			if !found {
				return review.Proof{}, fmt.Errorf("comparison reference was not produced by this runtime")
			}
		}
		return resolveReviewProof(ctx, project, root, s, ref)
	}
	measure := func(ctx context.Context, u risk.Unit, s checks.Snapshot, c builtin.ChecksOutput, r review.ResultV3) (risk.Validation, error) {
		return measureCorrection(ctx, project, root, runID, store, registry, u, s, c, r)
	}
	validateReview := func(ctx context.Context, r review.ResultV3) error {
		return persistedReview(ctx, store, registry, runID, r)
	}
	validateChecks := func(ctx context.Context, c builtin.ChecksOutput) error {
		return persistedChecks(ctx, store, registry, runID, c)
	}
	validateClaims := func(ctx context.Context, snapshot checks.Snapshot, claims []review.ClaimsV3) error {
		return persistedClaims(ctx, store, runID, v, snapshot, claims)
	}
	previousReviews := func(ctx context.Context, unit string) ([]review.ResultV3, error) {
		steps, err := store.ListSteps(ctx, runID)
		if err != nil {
			return nil, err
		}
		history := []review.ResultV3{}
		for _, step := range steps {
			if activity.ExecutorRef(step.ActivityRef) != "review.evaluate@1" || step.Status != workflow.StepSucceeded {
				continue
			}
			var result struct {
				Unit   string          `json:"unit"`
				Review review.ResultV3 `json:"review"`
			}
			if json.Unmarshal(step.Output, &result) != nil || result.Unit != unit {
				continue
			}
			if err := validatePersistedStep(ctx, store, registry, step); err != nil {
				return nil, err
			}
			history = append(history, result.Review)
		}
		return history, nil
	}
	approved := func(ctx context.Context, r activity.Request) (bool, error) {
		step, err := store.GetStep(ctx, r.RunID, r.ScopePath, r.StepID, r.ForeachKey)
		if err != nil {
			return false, err
		}
		approval, err := store.GetApprovalForStep(ctx, step.ID)
		if err != nil {
			return false, nil
		}
		if approval.Status == "rejected" {
			return false, fmt.Errorf("unit closure rejected")
		}
		return approval.Status == "approved", nil
	}
	for _, executor := range []activity.Executor{
		&builtin.ChecksBaseExecutor{ProjectDir: project},
		&builtin.ChecksPrepareExecutor{ProjectDir: project, ArtifactRoot: root},
		&builtin.ChecksExecutor{ProjectDir: project, ArtifactRoot: root, Profile: profile},
		&builtin.RiskExecutor{Store: units, Policy: policy, Limits: limits, Checks: profile},
		&builtin.ReviewPolicyExecutor{Store: units, Policy: policy, ResolveProof: proof, VerifySnapshot: checks.VerifySnapshot, ValidateClaims: validateClaims, PreviousReviews: previousReviews},
		&builtin.ReviewCompareExecutor{ProjectDir: project, ArtifactRoot: root, Profile: profile},
		&builtin.ReviewFailedChecksExecutor{ArtifactRoot: root, Profile: profile},
		&builtin.CorrectionReserveExecutor{Store: units},
		&builtin.CorrectionValidateExecutor{Store: units, Policy: policy, Measure: measure},
		&builtin.UnitCloseExecutor{ProjectDir: project, Store: units, Profile: profile, ArtifactRoot: root, Approved: approved, ValidateReview: validateReview, ValidateChecks: validateChecks},
	} {
		if err := registry.Register(executor); err != nil {
			return err
		}
	}
	return nil
}

func persistedClaims(ctx context.Context, store *workflow.Store, runID string, v *flow.Verification, snapshot checks.Snapshot, claims []review.ClaimsV3) error {
	if v == nil {
		return fmt.Errorf("review claims require pinned verification policy")
	}
	steps, err := store.ListSteps(ctx, runID)
	if err != nil {
		return err
	}
	for _, claim := range claims {
		found := false
		for _, step := range steps {
			if activity.ExecutorRef(step.ActivityRef) != "agent.invoke@1" || step.Status != workflow.StepSucceeded || step.ForeachKey == "" && len(step.Output) > 0 && step.Output[0] == '[' {
				continue
			}
			var in struct {
				Role     string          `json:"role"`
				ReadOnly bool            `json:"read_only"`
				Snapshot checks.Snapshot `json:"snapshot"`
				Context  map[string]any  `json:"context"`
			}
			if json.Unmarshal(step.Inputs, &in) != nil || !in.ReadOnly || in.Snapshot.TreeHash != snapshot.TreeHash || v.Roles[in.Role].Purpose != "review" {
				continue
			}
			if lens, _ := in.Context["LENS"].(string); lens != claim.Lens {
				continue
			}
			actual, err := review.DecodeClaimsV3(step.Output)
			if err == nil && string(mustJSON(actual)) == string(mustJSON(claim)) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("lens %s has no persisted read-only reviewer result for candidate", claim.Lens)
		}
	}
	return nil
}

func verifyFinalCandidate(ctx context.Context, project string, store *workflow.Store, run *workflow.Run) error {
	var ir flow.IR
	if err := json.Unmarshal(run.IR, &ir); err != nil {
		return err
	}
	if ir.Verification == nil {
		return nil
	}
	steps, err := store.ListSteps(ctx, run.ID)
	if err != nil {
		return err
	}
	var latest *workflow.StepRun
	var snapshot checks.Snapshot
	for i := range steps {
		step := &steps[i]
		if activity.ExecutorRef(step.ActivityRef) != "unit.close@1" || step.Status != workflow.StepSucceeded {
			continue
		}
		if latest != nil && step.UpdatedAt.Before(latest.UpdatedAt) {
			continue
		}
		var out struct {
			Snapshot checks.Snapshot `json:"snapshot"`
		}
		if err := json.Unmarshal(step.Output, &out); err != nil {
			return err
		}
		latest = step
		snapshot = out.Snapshot
	}
	if latest == nil {
		return fmt.Errorf("verified run has no final closure")
	}
	tree, err := checks.CurrentTree(ctx, project)
	if err != nil {
		return err
	}
	if tree != snapshot.TreeHash {
		if _, err := store.CreateApproval(ctx, latest, "product tree changed after its final evidence-backed closure"); err != nil {
			return err
		}
		return workflow.ErrNeedsHuman
	}
	return nil
}

func persistedReview(ctx context.Context, store *workflow.Store, registry *activity.Registry, runID string, r review.ResultV3) error {
	if r.Digest == "" {
		return fmt.Errorf("missing runtime review digest")
	}
	copy := r
	copy.Digest = ""
	if checks.Hash(mustJSON(copy)) != r.Digest {
		return fmt.Errorf("runtime review digest mismatch")
	}
	steps, err := store.ListSteps(ctx, runID)
	if err != nil {
		return err
	}
	for _, step := range steps {
		if activity.ExecutorRef(step.ActivityRef) != "review.evaluate@1" || step.Status != workflow.StepSucceeded {
			continue
		}
		var out struct {
			Review review.ResultV3 `json:"review"`
		}
		if json.Unmarshal(step.Output, &out) == nil && string(mustJSON(out.Review)) == string(mustJSON(r)) {
			return validatePersistedStep(ctx, store, registry, step)
		}
	}
	return fmt.Errorf("review is not a persisted runtime verdict of this run")
}

func validatePersistedStep(ctx context.Context, store *workflow.Store, registry *activity.Registry, step workflow.StepRun) error {
	executor, ok := registry.Resolve(activity.ExecutorRef(step.ActivityRef))
	if !ok {
		return fmt.Errorf("proof executor missing")
	}
	validator, ok := executor.(activity.EvidenceValidator)
	if !ok {
		return fmt.Errorf("proof executor cannot revalidate evidence")
	}
	request, result, err := store.EvidenceRequest(ctx, step)
	if err != nil {
		return err
	}
	return validator.ValidateEvidence(ctx, request, result)
}

func persistedChecks(ctx context.Context, store *workflow.Store, registry *activity.Registry, runID string, c builtin.ChecksOutput) error {
	steps, err := store.ListSteps(ctx, runID)
	if err != nil {
		return err
	}
	raw := mustJSON(c)
	for _, step := range steps {
		if activity.ExecutorRef(step.ActivityRef) != "checks.run@1" || step.Status != workflow.StepSucceeded {
			continue
		}
		var out builtin.ChecksOutput
		if json.Unmarshal(step.Output, &out) == nil && string(mustJSON(out)) == string(raw) {
			return validatePersistedStep(ctx, store, registry, step)
		}
	}
	return fmt.Errorf("checks are not a persisted runtime result of this run")
}

// Code references prove readable content at a frozen revision, never causality.
// Only runtime comparison receipts may establish introduced/preexisting.
func resolveReviewProof(_ context.Context, project, root string, s checks.Snapshot, ref string) (review.Proof, error) {
	if strings.HasPrefix(ref, "compare:") {
		return builtin.ResolveComparisonReference(root, s, ref)
	}
	if ref == "snapshot:"+s.TreeHash {
		if err := checks.VerifySnapshot(s); err != nil {
			return review.Proof{}, err
		}
		return review.Proof{Revision: s.TreeHash, Kind: "snapshot", Verified: true}, nil
	}
	if strings.HasPrefix(ref, "code:") {
		value := strings.TrimPrefix(ref, "code:")
		parts := strings.SplitN(value, ":", 2)
		if len(parts) == 2 && (len(parts[0]) == 40 || len(parts[0]) == 64) {
			tree := parts[0]
			value = parts[1]
			if tree != s.TreeHash {
				files, err := filepath.Glob(filepath.Join(root, "snapshots", "*.json"))
				if err != nil {
					return review.Proof{}, err
				}
				found := false
				for _, file := range files {
					raw, err := os.ReadFile(file)
					if err != nil {
						return review.Proof{}, err
					}
					var old checks.Snapshot
					if json.Unmarshal(raw, &old) == nil && old.TreeHash == tree {
						if err := checks.VerifySnapshot(old); err != nil {
							return review.Proof{}, err
						}
						s = old
						found = true
						break
					}
				}
				if !found {
					return review.Proof{}, fmt.Errorf("historical code tree has no runtime snapshot")
				}
			}
		}
		split := strings.LastIndex(value, ":")
		if split < 1 {
			return review.Proof{}, fmt.Errorf("code reference must be code:path:line")
		}
		name := value[:split]
		line, err := strconv.Atoi(value[split+1:])
		if err != nil || line < 1 {
			return review.Proof{}, fmt.Errorf("invalid code line")
		}
		if filepath.IsAbs(name) || filepath.ToSlash(filepath.Clean(name)) != name || name == ".." || strings.HasPrefix(name, "../") {
			return review.Proof{}, fmt.Errorf("unsafe code reference")
		}
		path := filepath.Join(s.Dir, name)
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return review.Proof{}, err
		}
		relative, err := filepath.Rel(s.Dir, resolved)
		if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
			return review.Proof{}, fmt.Errorf("code reference escapes snapshot")
		}
		raw, err := os.ReadFile(resolved)
		if err != nil {
			return review.Proof{}, err
		}
		if line > len(strings.Split(string(raw), "\n")) {
			return review.Proof{}, fmt.Errorf("code line outside file")
		}
		return review.Proof{Revision: s.TreeHash, BaseRevision: s.BaseRevision, Kind: "code", Verified: true}, nil
	}
	return review.Proof{}, fmt.Errorf("unsupported runtime proof reference %q", ref)
}

func measureCorrection(ctx context.Context, project, root, runID string, store *workflow.Store, registry *activity.Registry, u risk.Unit, s checks.Snapshot, c builtin.ChecksOutput, r review.ResultV3) (risk.Validation, error) {
	v := risk.Validation{TreeHash: s.TreeHash}
	if err := checks.VerifySnapshot(s); err != nil {
		return v, nil
	}
	v.Readable = true
	if c.Snapshot.TreeHash != s.TreeHash || r.ReviewedRevision != s.TreeHash {
		return v, fmt.Errorf("correction proof refers to another tree")
	}
	if err := persistedChecks(ctx, store, registry, runID, c); err != nil {
		return v, err
	}
	if err := persistedReview(ctx, store, registry, runID, r); err != nil {
		return v, err
	}
	if err := builtin.VerifyOutput(mustJSON(c)); err != nil {
		v.Passed = false
	} else {
		v.Passed = r.Approved
	}
	cmd := exec.CommandContext(ctx, "git", "diff", "--numstat", "--no-renames", u.Risk.Candidate.TreeHash, s.TreeHash, "--")
	cmd.Dir = project
	raw, err := cmd.Output()
	if err != nil {
		return v, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		for _, n := range fields[:2] {
			count, err := strconv.Atoi(n)
			if err != nil {
				count = 1000
			}
			v.ChangedLines += count
		}
	}
	report, err := telemetry.TaskReport(root, u.CorrectionTask, u.StartedAt)
	if err != nil {
		return v, err
	}
	if report.Records == 0 || report.MeasuredRecords != report.Records {
		return v, &activity.ApprovalRequiredError{Reason: "correction token measurement is unavailable"}
	}
	v.Tokens = report.TotalVolume
	counters, err := supervision.Measure(root, u.CorrectionTask, "", u.StartedAt)
	if err != nil {
		return v, err
	}
	v.ToolCalls = counters.ToolCalls
	proof := struct {
		Checks     builtin.ChecksOutput `json:"checks"`
		Review     review.ResultV3      `json:"review"`
		Validation risk.Validation      `json:"validation"`
	}{c, r, v}
	data := mustJSON(proof)
	dir := filepath.Join(root, "corrections")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return v, err
	}
	v.EvidencePath = filepath.Join(dir, checks.Hash([]byte(u.ID+"\x00"+s.TreeHash))+".json")
	if err := os.WriteFile(v.EvidencePath, data, 0600); err != nil {
		return v, err
	}
	v.EvidenceDigest = checks.Hash(data)
	return v, nil
}

func mustJSON(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }
