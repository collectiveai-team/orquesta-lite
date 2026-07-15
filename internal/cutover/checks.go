package cutover

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/activity"
	"github.com/lionelchamorro/orquestalite/internal/activity/builtin"
	"github.com/lionelchamorro/orquestalite/internal/flow"
	"github.com/lionelchamorro/orquestalite/internal/legacytest"
)

var requiredChaosScenarios = []string{
	"crash_before_effect",
	"crash_after_effect",
	"outbox_replay",
	"parallel_isolation",
}

var requiredDevelopmentFlows = []string{
	"factory-fast",
	"factory-governed",
	"issue-fix",
	"plan-tickets",
	"pr-review",
	"task-list",
}

type Options struct {
	EvidencePath   string
	ExpectedCommit string
}

func Check(_ context.Context, options Options) (Report, error) {
	evidence, err := Load(options.EvidencePath)
	if err != nil {
		return Report{}, err
	}
	base := filepath.Dir(options.EvidencePath)
	gates := []Gate{
		checkParity(evidence.RuntimeCommit, options.ExpectedCommit, evidence.Parity),
		checkBenchmarks(evidence.RuntimeCommit, evidence.Benchmarks),
		checkChaos(evidence.RuntimeCommit, evidence.Chaos),
		checkCanary(evidence.RuntimeCommit, evidence.Canary),
		checkRollback(evidence.Canary, evidence.Rollback),
		checkLegacyProjects(base, evidence.ControlledProjects),
		checkDevelopmentPack(base, evidence.DevelopmentPack),
	}
	report := Report{Open: true, Gates: gates}
	for _, gate := range gates {
		if !gate.Passed {
			report.Open = false
		}
	}
	return report, nil
}

func checkParity(runtimeCommit, expectedCommit string, results []ParityResult) Gate {
	if expectedCommit == "" {
		return failedGate("parity", "--commit is required to bind evidence to the deletion candidate")
	}
	if runtimeCommit == "" || runtimeCommit != expectedCommit {
		return failedGate("parity", fmt.Sprintf("evidence runtimeCommit=%q does not match deletion candidate %q", runtimeCommit, expectedCommit))
	}
	byName := make(map[string]ParityResult, len(results))
	duplicates := []string{}
	for _, result := range results {
		if _, exists := byName[result.Scenario]; exists {
			duplicates = append(duplicates, result.Scenario)
		}
		byName[result.Scenario] = result
	}
	missing := []string{}
	failed := []string{}
	for _, scenario := range legacytest.Scenarios {
		result, ok := byName[scenario.Name]
		if !ok {
			missing = append(missing, scenario.Name)
			continue
		}
		if !result.V2Passed || strings.TrimSpace(result.LegacyBaseline) == "" || result.Commit != runtimeCommit {
			failed = append(failed, scenario.Name)
		}
	}
	if len(missing) > 0 || len(failed) > 0 || len(duplicates) > 0 {
		return failedGate("parity", parts("missing", missing, "not proven", failed, "duplicates", duplicates))
	}
	return passedGate("parity", fmt.Sprintf("%d/%d scenarios have a legacy baseline and a passing v2 result", len(legacytest.Scenarios), len(legacytest.Scenarios)))
}

func checkBenchmarks(runtimeCommit string, results []BenchmarkResult) Gate {
	if len(results) < 3 {
		return failedGate("benchmarks", fmt.Sprintf("need at least 3 comparable runs; found %d", len(results)))
	}
	first := results[0]
	seen := map[string]bool{}
	issues := []string{}
	for _, result := range results {
		if result.ID == "" || seen[result.ID] {
			issues = append(issues, "missing or duplicate run id")
		}
		seen[result.ID] = true
		if result.Model == "" || result.ConfigDigest == "" || result.Model != first.Model || result.ConfigDigest != first.ConfigDigest {
			issues = append(issues, result.ID+": model/config differs")
		}
		if result.Commit != runtimeCommit {
			issues = append(issues, result.ID+": commit differs from runtimeCommit")
		}
		if !result.Passed || result.CriticalRegressions != 0 {
			issues = append(issues, fmt.Sprintf("%s: passed=%t critical_regressions=%d", result.ID, result.Passed, result.CriticalRegressions))
		}
		if !validTimestamp(result.CompletedAt) {
			issues = append(issues, result.ID+": invalid completedAt")
		}
	}
	if len(issues) > 0 {
		return failedGate("benchmarks", strings.Join(unique(issues), "; "))
	}
	return passedGate("benchmarks", fmt.Sprintf("%d comparable runs with model=%s config=%s and zero critical regressions", len(results), first.Model, first.ConfigDigest))
}

func checkChaos(runtimeCommit string, results []ChaosResult) Gate {
	byName := map[string]ChaosResult{}
	duplicates := []string{}
	for _, result := range results {
		if _, exists := byName[result.Scenario]; exists {
			duplicates = append(duplicates, result.Scenario)
		}
		byName[result.Scenario] = result
	}
	failed := []string{}
	for _, name := range requiredChaosScenarios {
		result, ok := byName[name]
		if !ok || !result.Passed || result.Commit != runtimeCommit || !validTimestamp(result.CompletedAt) {
			failed = append(failed, name)
		}
	}
	if len(failed) > 0 || len(duplicates) > 0 {
		return failedGate("chaos", parts("missing or failing", failed, "duplicates", duplicates))
	}
	return passedGate("chaos", fmt.Sprintf("%d required crash-boundary scenarios passed", len(requiredChaosScenarios)))
}

func checkCanary(runtimeCommit string, result CanaryResult) Gate {
	if result.Release == "" || result.Commit != runtimeCommit || result.Project == "" || !result.V2Default || !result.Passed || !validTimestamp(result.CompletedAt) {
		return failedGate("canary", "requires the runtime commit, a named release/project, v2Default=true, passed=true, and an RFC3339 completedAt")
	}
	return passedGate("canary", fmt.Sprintf("%s dogfooded with v2 default on %s", result.Release, result.Project))
}

func checkRollback(canary CanaryResult, result RollbackResult) Gate {
	if result.FromVersion == "" || result.FromVersion != canary.Release || result.ToVersion == "" || result.ToVersion == result.FromVersion || result.Project == "" || !result.Passed || !validTimestamp(result.CompletedAt) {
		return failedGate("rollback", "requires from/to versions, project, passed=true, and an RFC3339 completedAt")
	}
	return passedGate("rollback", fmt.Sprintf("rollback %s -> %s passed on %s", result.FromVersion, result.ToVersion, result.Project))
}

func checkLegacyProjects(base string, projects []ControlledProject) Gate {
	if len(projects) == 0 {
		return failedGate("legacy-runs", "no controlled projects declared")
	}
	issues := []string{}
	for _, project := range projects {
		if project.Name == "" || project.Path == "" {
			issues = append(issues, "controlled project is missing name or path")
			continue
		}
		root := resolvePath(base, project.Path)
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			issues = append(issues, project.Name+": project path is not a directory")
			continue
		}
		for _, path := range []string{filepath.Join(root, "tasks.json"), filepath.Join(root, ".orquestalite", "tasks.json")} {
			if issue := activeTasks(path); issue != "" {
				issues = append(issues, project.Name+": "+issue)
			}
		}
		if issue := activeFactory(filepath.Join(root, ".orquestalite", "factory.json")); issue != "" {
			issues = append(issues, project.Name+": "+issue)
		}
	}
	if len(issues) > 0 {
		return failedGate("legacy-runs", strings.Join(issues, "; "))
	}
	return passedGate("legacy-runs", fmt.Sprintf("%d controlled projects have no unfinished legacy state", len(projects)))
}

func activeTasks(path string) string {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		return filepath.Base(path) + " cannot be read"
	}
	var envelope struct {
		Tasks json.RawMessage `json:"tasks"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil || len(envelope.Tasks) == 0 || string(envelope.Tasks) == "null" {
		return filepath.Base(path) + " is invalid legacy state"
	}
	var tasks []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err = json.Unmarshal(envelope.Tasks, &tasks); err != nil {
		return filepath.Base(path) + " has invalid tasks"
	}
	active := []string{}
	for _, task := range tasks {
		if task.Status != "done" && task.Status != "decomposed" {
			active = append(active, task.ID+"="+task.Status)
		}
	}
	if len(active) > 0 {
		return filepath.Base(path) + " has active tasks " + strings.Join(active, ",")
	}
	return ""
}

func activeFactory(path string) string {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		return "factory.json cannot be read"
	}
	var envelope struct {
		Features json.RawMessage `json:"features"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil || len(envelope.Features) == 0 || string(envelope.Features) == "null" {
		return "factory.json is invalid legacy state"
	}
	var features []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err = json.Unmarshal(envelope.Features, &features); err != nil {
		return "factory.json has invalid features"
	}
	active := []string{}
	for _, feature := range features {
		if feature.Status != "done" {
			active = append(active, feature.ID+"="+feature.Status)
		}
	}
	if len(active) > 0 {
		return "factory.json has active features " + strings.Join(active, ",")
	}
	return ""
}

func checkDevelopmentPack(base string, expected DevelopmentPack) Gate {
	if expected.Root == "" || expected.Name == "" || expected.Version == "" || expected.ManifestDigest == "" {
		return failedGate("development-pack", "root, name, version, and manifestDigest are required")
	}
	root := resolvePath(base, expected.Root)
	pack, err := flow.LoadPack(root)
	if err != nil {
		return failedGate("development-pack", err.Error())
	}
	actualDigest := string(pack.Snapshot().Digest)
	if pack.Name != expected.Name || pack.Version != expected.Version || actualDigest != expected.ManifestDigest {
		return failedGate("development-pack", fmt.Sprintf("installed pack identity mismatch: got %s@%s digest=%s", pack.Name, pack.Version, actualDigest))
	}
	if pack.Name != "development" {
		return failedGate("development-pack", fmt.Sprintf("CLI aliases require pack name development; got %s", pack.Name))
	}
	if pack.Version != "1" {
		return failedGate("development-pack", fmt.Sprintf("CLI aliases require development pack version 1; got %s", pack.Version))
	}
	catalog := flow.NewDirectoryCatalog(root, cutoverBuiltinSpecs())
	issues := []string{}
	for _, name := range requiredDevelopmentFlows {
		ref := flow.ResourceRef{Kind: "flow", Name: name, Version: pack.Version}
		doc, _, resolveErr := catalog.ResolveDocument(ref)
		if resolveErr != nil {
			issues = append(issues, resolveErr.Error())
			continue
		}
		_, diagnostics := flow.Compile(doc, catalog)
		if diagnostics.HasErrors() {
			issues = append(issues, fmt.Sprintf("%s does not compile", ref.String()))
		}
	}
	if len(issues) > 0 {
		return failedGate("development-pack", strings.Join(issues, "; "))
	}
	return passedGate("development-pack", fmt.Sprintf("%s@%s digest=%s resolves offline with %d required flows", pack.Name, pack.Version, actualDigest, len(requiredDevelopmentFlows)))
}

func cutoverBuiltinSpecs() []activity.Spec {
	return []activity.Spec{
		(&builtin.AgentExecutor{}).Spec(),
		(&builtin.CommandExecutor{}).Spec(),
		(&builtin.GateExecutor{}).Spec(),
		(&builtin.ArtifactExecutor{}).Spec(),
		(builtin.ApprovalExecutor{}).Spec(),
	}
}

func validTimestamp(value string) bool {
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}

func resolvePath(base, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(base, path))
}

func parts(labelsAndValues ...any) string {
	items := []string{}
	for i := 0; i < len(labelsAndValues); i += 2 {
		label := labelsAndValues[i].(string)
		values := labelsAndValues[i+1].([]string)
		if len(values) > 0 {
			sort.Strings(values)
			items = append(items, label+": "+strings.Join(values, ", "))
		}
	}
	return strings.Join(items, "; ")
}

func unique(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func passedGate(name, detail string) Gate { return Gate{Name: name, Passed: true, Detail: detail} }
func failedGate(name, detail string) Gate { return Gate{Name: name, Detail: detail} }
