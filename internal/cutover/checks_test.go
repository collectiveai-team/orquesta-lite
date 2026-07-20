package cutover

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/flow"
)

func TestCheckOpensWithCompleteObservedEvidence(t *testing.T) {
	root := t.TempDir()
	evidence, path := completeEvidence(t, root)
	writeEvidence(t, path, evidence)

	report, err := Check(context.Background(), Options{EvidencePath: path, ExpectedCommit: evidence.RuntimeCommit})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !report.Open {
		t.Fatalf("expected open report, got %+v", report)
	}
	for _, gate := range report.Gates {
		if !gate.Passed {
			t.Errorf("gate %s did not pass: %s", gate.Name, gate.Detail)
		}
	}
}

func TestCheckFailsClosedForActiveLegacyStateAndTamperedPack(t *testing.T) {
	root := t.TempDir()
	evidence, path := completeEvidence(t, root)
	project := filepath.Join(root, evidence.ControlledProjects[0].Path)
	writeFile(t, filepath.Join(project, ".orquestalite", "tasks.json"), []byte(`{"tasks":[{"id":"T002","status":"in_progress"}]}`))
	packRoot := filepath.Join(root, evidence.DevelopmentPack.Root)
	writeFile(t, filepath.Join(packRoot, "flows", "task-list@1.json"), []byte(`{"tampered":true}`))
	writeEvidence(t, path, evidence)

	report, err := Check(context.Background(), Options{EvidencePath: path, ExpectedCommit: evidence.RuntimeCommit})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if report.Open {
		t.Fatal("expected closed report")
	}
	assertGate(t, report, "legacy-runs", false)
	assertGate(t, report, "development-pack", false)
}

func TestCheckReportsEveryMissingOperationalGate(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "evidence.json")
	writeEvidence(t, path, Template())

	report, err := Check(context.Background(), Options{EvidencePath: path})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if report.Open || len(report.Gates) != 7 {
		t.Fatalf("unexpected report: %+v", report)
	}
	for _, gate := range report.Gates {
		if gate.Passed {
			t.Errorf("empty template unexpectedly passed %s", gate.Name)
		}
	}
}

func TestCheckRejectsEvidenceFromAnotherCommit(t *testing.T) {
	root := t.TempDir()
	evidence, path := completeEvidence(t, root)
	writeEvidence(t, path, evidence)

	report, err := Check(context.Background(), Options{EvidencePath: path, ExpectedCommit: "newer-commit"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	assertGate(t, report, "parity", false)
	if report.Open {
		t.Fatal("stale evidence opened the gate")
	}
}

func TestCheckRejectsMalformedLegacyState(t *testing.T) {
	root := t.TempDir()
	evidence, path := completeEvidence(t, root)
	project := filepath.Join(root, evidence.ControlledProjects[0].Path)
	writeFile(t, filepath.Join(project, ".orquestalite", "tasks.json"), []byte(`{}`))
	writeEvidence(t, path, evidence)

	report, err := Check(context.Background(), Options{EvidencePath: path, ExpectedCommit: evidence.RuntimeCommit})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	assertGate(t, report, "legacy-runs", false)
}

func TestLoadRejectsUnknownFieldsAndTrailingValues(t *testing.T) {
	root := t.TempDir()
	unknown := filepath.Join(root, "unknown.json")
	writeFile(t, unknown, []byte(`{"apiVersion":"orq.cutover/v1","unknown":true}`))
	if _, err := Load(unknown); err == nil {
		t.Fatal("expected unknown field error")
	}

	trailing := filepath.Join(root, "trailing.json")
	writeFile(t, trailing, []byte(`{"apiVersion":"orq.cutover/v1"} {}`))
	if _, err := Load(trailing); err == nil {
		t.Fatal("expected trailing value error")
	}
}

func completeEvidence(t *testing.T, root string) (Evidence, string) {
	t.Helper()
	projectRel := "projects/controlled"
	project := filepath.Join(root, projectRel)
	writeFile(t, filepath.Join(project, ".orquestalite", "tasks.json"), []byte(`{"tasks":[{"id":"T001","status":"done"}]}`))
	writeFile(t, filepath.Join(project, ".orquestalite", "factory.json"), []byte(`{"features":[{"id":"F001","status":"done"}]}`))

	packRel := "packs/development/1"
	packRoot := filepath.Join(root, packRel)
	pack := writeDevelopmentPack(t, packRoot)

	evidence := Template()
	evidence.RuntimeCommit = "9f82ff6"
	for i := range evidence.Parity {
		evidence.Parity[i].LegacyBaseline = "legacy-fixture@9f82ff6"
		evidence.Parity[i].V2Passed = true
		evidence.Parity[i].Commit = "9f82ff6"
	}
	for i := 1; i <= 3; i++ {
		evidence.Benchmarks = append(evidence.Benchmarks, BenchmarkResult{
			ID: "benchmark-" + string(rune('0'+i)), Commit: evidence.RuntimeCommit, Model: "model-a", ConfigDigest: "sha256:config",
			Passed: true, CompletedAt: "2026-07-15T12:00:00Z",
		})
	}
	for i := range evidence.Chaos {
		evidence.Chaos[i].Passed = true
		evidence.Chaos[i].Commit = "9f82ff6"
		evidence.Chaos[i].CompletedAt = "2026-07-15T12:00:00Z"
	}
	evidence.Canary = CanaryResult{Release: "v0.3.0-rc.1", Commit: evidence.RuntimeCommit, Project: "controlled", V2Default: true, Passed: true, CompletedAt: "2026-07-15T12:00:00Z"}
	evidence.Rollback = RollbackResult{FromVersion: "v0.3.0-rc.1", ToVersion: "v0.2.9", Project: "controlled", Passed: true, CompletedAt: "2026-07-15T12:15:00Z"}
	evidence.ControlledProjects = []ControlledProject{{Name: "controlled", Path: projectRel}}
	evidence.DevelopmentPack = DevelopmentPack{Root: packRel, Name: pack.Name, Version: pack.Version, ManifestDigest: string(pack.Snapshot().Digest)}
	return evidence, filepath.Join(root, "evidence.json")
}

func writeDevelopmentPack(t *testing.T, root string) *flow.Pack {
	t.Helper()
	files := map[string]flow.Digest{}
	for _, name := range requiredDevelopmentFlows {
		relative := filepath.ToSlash(filepath.Join("flows", name+"@1.json"))
		raw := []byte(`{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"` + name + `","version":"1"},"steps":[]}`)
		writeFile(t, filepath.Join(root, filepath.FromSlash(relative)), raw)
		sum := sha256.Sum256(raw)
		files[relative] = flow.Digest(hex.EncodeToString(sum[:]))
	}
	pack := &flow.Pack{APIVersion: flow.PackAPIVersion, Name: "development", Version: "1", Files: files}
	raw, err := json.MarshalIndent(pack, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "pack.json"), raw)
	loaded, err := flow.LoadPack(root)
	if err != nil {
		t.Fatalf("LoadPack: %v", err)
	}
	return loaded
}

func writeEvidence(t *testing.T, path string, evidence Evidence) {
	t.Helper()
	raw, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, raw)
}

func writeFile(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertGate(t *testing.T, report Report, name string, passed bool) {
	t.Helper()
	for _, gate := range report.Gates {
		if gate.Name == name {
			if gate.Passed != passed {
				t.Fatalf("gate %s passed=%t, want %t (%s)", name, gate.Passed, passed, gate.Detail)
			}
			return
		}
	}
	t.Fatalf("gate %s not found", name)
}
