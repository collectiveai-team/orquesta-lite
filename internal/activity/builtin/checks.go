package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/checks"
)

type ChecksPrepareExecutor struct {
	ProjectDir   string
	ArtifactRoot string
}

func (*ChecksPrepareExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "checks.prepare", Version: "1", Effect: activity.EffectAtMostOnce}
}

type checksPrepareInput struct {
	Base string `json:"base,omitempty"`
}
type checksPrepareOutput struct {
	Snapshot checks.Snapshot `json:"snapshot"`
}

func (e *ChecksPrepareExecutor) Execute(ctx context.Context, r activity.Request) (activity.Result, error) {
	var in checksPrepareInput
	if err := strictJSON(r.Inputs, &in); err != nil {
		return activity.Result{}, contractError("checks.prepare", err)
	}
	key := checks.Hash([]byte(r.RunID + "\x00" + r.ScopePath + "\x00" + r.StepID + "\x00" + r.ForeachKey))
	dest := filepath.Join(e.ArtifactRoot, "snapshots", fmt.Sprintf("%s-%d", key, r.Attempt))
	s, err := checks.FreezeBase(ctx, e.ProjectDir, dest, in.Base)
	if err != nil {
		return activity.Result{}, err
	}
	raw, _ := json.Marshal(checksPrepareOutput{Snapshot: s})
	manifest, _ := os.ReadFile(s.Dir + ".json")
	return activity.Result{Output: raw, Artifacts: []activity.Artifact{{Kind: "check_snapshot", URI: s.Dir + ".json", Digest: checks.Hash(manifest)}}}, nil
}
func (e *ChecksPrepareExecutor) ValidateEvidence(_ context.Context, _ activity.Request, r activity.Result) error {
	var out checksPrepareOutput
	if err := strictJSON(r.Output, &out); err != nil {
		return err
	}
	if !within(e.ArtifactRoot, out.Snapshot.Dir) {
		return fmt.Errorf("checks: snapshot outside run artifacts")
	}
	if err := checks.VerifySnapshot(out.Snapshot); err != nil {
		return err
	}
	return verifyArtifacts(r.Artifacts)
}

type ChecksExecutor struct {
	ProjectDir   string
	ArtifactRoot string
	Profile      *checks.Resolved
}

func (*ChecksExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "checks.run", Version: "1", Effect: activity.EffectAtMostOnce}
}

type checksInput struct {
	ReportOnly bool             `json:"report_only,omitempty"`
	Event      string           `json:"event,omitempty"`
	MaxTier    int              `json:"max_tier,omitempty"`
	Base       string           `json:"base,omitempty"`
	Snapshot   *checks.Snapshot `json:"snapshot,omitempty"`
}
type CheckEvidence struct {
	CheckID  string `json:"check_id"`
	Required bool   `json:"required"`
	Path     string `json:"path"`
	Digest   string `json:"digest"`
}
type ChecksOutput struct {
	Passed          bool            `json:"passed"`
	Snapshot        checks.Snapshot `json:"snapshot"`
	ProfileHash     string          `json:"profile_hash"`
	Selected        []string        `json:"selected"`
	Evidence        []CheckEvidence `json:"evidence"`
	CoverageError   string          `json:"coverage_error,omitempty"`
	CoveragePercent *float64        `json:"coverage_percent,omitempty"`
	CoverageReport  string          `json:"coverage_report,omitempty"`
	CoverageHash    string          `json:"coverage_hash,omitempty"`
}

func (e *ChecksExecutor) Execute(ctx context.Context, r activity.Request) (activity.Result, error) {
	var in checksInput
	if err := strictJSON(r.Inputs, &in); err != nil {
		return activity.Result{}, contractError("checks.run", err)
	}
	if e.Profile == nil {
		return activity.Result{}, contractError("checks.run", fmt.Errorf("compiled check profile missing"))
	}
	if in.Event == "" {
		in.Event = "pr"
	}
	if in.MaxTier == 0 {
		in.MaxTier = 1
	}
	var s checks.Snapshot
	if in.Snapshot != nil {
		s = *in.Snapshot
		if !within(e.ArtifactRoot, s.Dir) {
			return activity.Result{}, fmt.Errorf("checks: snapshot outside run artifacts")
		}
		if err := checks.VerifySnapshot(s); err != nil {
			return activity.Result{}, err
		}
	} else {
		key := checks.Hash([]byte(r.RunID + "\x00" + r.ScopePath + "\x00" + r.StepID + "\x00" + r.ForeachKey))
		var err error
		s, err = checks.FreezeBase(ctx, e.ProjectDir, filepath.Join(e.ArtifactRoot, "snapshots", fmt.Sprintf("%s-%d", key, r.Attempt)), in.Base)
		if err != nil {
			return activity.Result{}, err
		}
	}
	selected, err := checks.Select(e.Profile.Profile, checks.Selection{Event: in.Event, MaxTier: in.MaxTier, Paths: s.Paths, DiffKnown: s.DiffKnown})
	if err != nil {
		return activity.Result{}, contractError("checks.run", err)
	}
	out := ChecksOutput{Passed: true, Snapshot: s, ProfileHash: e.Profile.SourceHash, Selected: []string{}, Evidence: []CheckEvidence{}}
	artifacts := []activity.Artifact{}
	for _, c := range selected {
		out.Selected = append(out.Selected, c.ID)
		evidence, err := checks.Execute(ctx, c, s, checks.Execution{Root: e.ArtifactRoot, RunID: r.RunID, Scope: r.ScopePath + "/" + r.StepID + "/" + r.ForeachKey, Attempt: r.Attempt, ProfileHash: e.Profile.SourceHash})
		if err != nil {
			return activity.Result{}, err
		}
		passed := evidence.ExitCode == 0 && evidence.Error == "" && !evidence.TimedOut
		if c.Required && !passed {
			out.Passed = false
		}
		out.Evidence = append(out.Evidence, CheckEvidence{CheckID: c.ID, Required: c.Required, Path: evidence.Receipt, Digest: evidence.ReceiptHash})
		artifacts = append(artifacts, activity.Artifact{Kind: "check_evidence", URI: evidence.Receipt, Digest: evidence.ReceiptHash})
		if c.ID == e.Profile.Profile.Coverage.CheckID && e.Profile.Profile.Coverage.Threshold > 0 {
			report := filepath.Join(filepath.Dir(evidence.Receipt), "tree", e.Profile.Profile.Coverage.Report)
			pct, hash, err := verifiedCoverageReport(filepath.Join(filepath.Dir(evidence.Receipt), "tree"), report)
			if _, sourceErr := os.Lstat(filepath.Join(s.Dir, e.Profile.Profile.Coverage.Report)); sourceErr == nil {
				err = fmt.Errorf("coverage report must be produced by the check, not copied from source")
			} else if !os.IsNotExist(sourceErr) {
				err = sourceErr
			}
			if err != nil {
				out.CoverageError = err.Error()
				out.Passed = false
			} else {
				out.CoveragePercent = &pct
				out.CoverageReport = report
				out.CoverageHash = hash
				artifacts = append(artifacts, activity.Artifact{Kind: "coverage_report", URI: report, Digest: hash})
			}
			if pct < e.Profile.Profile.Coverage.Threshold {
				out.Passed = false
			}
		}
	}
	raw, _ := json.Marshal(out)
	result := activity.Result{Output: raw, Artifacts: artifacts}
	if !out.Passed && !in.ReportOnly {
		return result, &activity.Error{Class: activity.ErrorGateFailed, Op: "checks.run", Err: fmt.Errorf("required runtime checks or coverage failed")}
	}
	return result, nil
}
func (e *ChecksExecutor) ValidateEvidence(_ context.Context, request activity.Request, r activity.Result) error {
	var input checksInput
	if err := strictJSON(request.Inputs, &input); err != nil {
		return err
	}
	if err := verifyOutput(r.Output, input.ReportOnly); err != nil {
		return err
	}
	var out ChecksOutput
	if err := json.Unmarshal(r.Output, &out); err != nil {
		return err
	}
	if e.Profile == nil || out.ProfileHash != e.Profile.SourceHash {
		return fmt.Errorf("checks: compiled profile mismatch")
	}
	if !within(e.ArtifactRoot, out.Snapshot.Dir) {
		return fmt.Errorf("checks: snapshot outside run artifacts")
	}
	var in checksInput
	if err := strictJSON(request.Inputs, &in); err != nil {
		return err
	}
	if in.Event == "" {
		in.Event = "pr"
	}
	if in.MaxTier == 0 {
		in.MaxTier = 1
	}
	selected, err := checks.Select(e.Profile.Profile, checks.Selection{Event: in.Event, MaxTier: in.MaxTier, Paths: out.Snapshot.Paths, DiffKnown: out.Snapshot.DiffKnown})
	if err != nil {
		return err
	}
	if len(selected) != len(out.Selected) || len(selected) != len(out.Evidence) {
		return fmt.Errorf("checks: incomplete selected evidence")
	}
	for i, c := range selected {
		item := out.Evidence[i]
		if item.CheckID != c.ID || out.Selected[i] != c.ID || item.Required != c.Required || !within(e.ArtifactRoot, item.Path) {
			return fmt.Errorf("checks: evidence does not match compiled selection")
		}
		raw, err := os.ReadFile(item.Path)
		if err != nil {
			return err
		}
		var receipt checks.Evidence
		if err = json.Unmarshal(raw, &receipt); err != nil {
			return err
		}
		expectedScope := request.ScopePath + "/" + request.StepID + "/" + request.ForeachKey
		if receipt.CheckID != c.ID || !reflect.DeepEqual(receipt.Argv, c.Argv) || receipt.RunID != request.RunID || receipt.Scope != expectedScope || receipt.Attempt != request.Attempt {
			return fmt.Errorf("checks: execution identity does not match request")
		}
	}
	coverage := e.Profile.Profile.Coverage
	if coverage.Threshold > 0 && out.CoverageError == "" {
		var expected, treeRoot string
		for _, item := range out.Evidence {
			if item.CheckID == coverage.CheckID {
				treeRoot = filepath.Join(filepath.Dir(item.Path), "tree")
				expected = filepath.Join(treeRoot, coverage.Report)
			}
		}
		if expected == "" || expected != out.CoverageReport {
			return fmt.Errorf("checks: coverage report does not match producer")
		}
		percent, hash, err := verifiedCoverageReport(treeRoot, expected)
		if err != nil {
			return err
		}
		if out.CoveragePercent == nil || *out.CoveragePercent != percent || out.CoverageHash != hash {
			return fmt.Errorf("checks: coverage report changed")
		}
	}
	if !input.ReportOnly && coverage.Threshold > 0 && (out.CoveragePercent == nil || *out.CoveragePercent < coverage.Threshold) {
		return fmt.Errorf("checks: coverage threshold not met")
	}

	return verifyArtifacts(r.Artifacts)
}
func VerifyOutput(raw json.RawMessage) error { return verifyOutput(raw, false) }
func verifyOutput(raw json.RawMessage, allowFailed bool) error {
	var out ChecksOutput
	if err := strictJSON(raw, &out); err != nil {
		return err
	}
	if !allowFailed && !out.Passed {
		return fmt.Errorf("checks: required checks did not pass")
	}
	if err := checks.VerifySnapshot(out.Snapshot); err != nil {
		return err
	}
	if out.CoverageReport != "" {
		b, err := os.ReadFile(out.CoverageReport)
		if err != nil || checks.Hash(b) != out.CoverageHash {
			return fmt.Errorf("checks: missing or corrupt coverage report")
		}
	}
	for _, e := range out.Evidence {
		b, err := os.ReadFile(e.Path)
		if err != nil || checks.Hash(b) != e.Digest {
			return fmt.Errorf("checks: missing or corrupt receipt %s", e.Path)
		}
		if err := checks.VerifyReceipt(e.Path, out.Snapshot.TreeHash, out.ProfileHash, e.Required && !allowFailed); err != nil {
			return err
		}
	}
	return nil
}

func verifiedCoverageReport(root, report string) (float64, string, error) {
	if !within(root, report) {
		return 0, "", fmt.Errorf("checks: coverage report escapes check tree")
	}
	info, err := os.Lstat(report)
	if err != nil {
		return 0, "", err
	}
	if !info.Mode().IsRegular() {
		return 0, "", fmt.Errorf("checks: coverage report must be a regular file")
	}
	if info.Size() > checks.MaxLogBytes {
		return 0, "", fmt.Errorf("checks: coverage report exceeds artifact limit")
	}
	b, err := os.ReadFile(report)
	if err != nil {
		return 0, "", err
	}
	pct, err := checks.CoveragePercent(report)
	return pct, checks.Hash(b), err
}
func within(root, p string) bool {
	r, e := filepath.EvalSymlinks(root)
	if e != nil {
		return false
	}
	r, e = filepath.Abs(r)
	if e != nil {
		return false
	}
	a, e := filepath.EvalSymlinks(p)
	if e != nil {
		return false
	}
	a, e = filepath.Abs(a)
	if e != nil {
		return false
	}
	rel, e := filepath.Rel(r, a)
	return e == nil && rel != ".." && len(rel) > 0 && !(len(rel) > 3 && rel[:3] == "../")
}
func verifyArtifacts(items []activity.Artifact) error {
	for _, a := range items {
		b, e := os.ReadFile(a.URI)
		if e != nil || checks.Hash(b) != a.Digest {
			return fmt.Errorf("checks: missing or corrupt artifact %s", a.URI)
		}
	}
	return nil
}

type ChecksBaseExecutor struct{ ProjectDir string }

func (*ChecksBaseExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "checks.base", Version: "1", Effect: activity.EffectAtMostOnce}
}
func (e *ChecksBaseExecutor) Execute(ctx context.Context, r activity.Request) (activity.Result, error) {
	var in checksPrepareInput
	if err := strictJSON(r.Inputs, &in); err != nil {
		return activity.Result{}, contractError("checks.base", err)
	}
	base, err := checks.ResolveBase(ctx, e.ProjectDir, in.Base)
	if err != nil {
		return activity.Result{}, err
	}
	raw, _ := json.Marshal(map[string]string{"base_revision": base})
	return activity.Result{Output: raw}, nil
}
