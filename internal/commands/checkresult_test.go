package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const checkResultSchema = `{"type":"object","additionalProperties":false,"required":["status"],"properties":{"status":{"type":"string","enum":["complete","partial"]}}}`

func writeCheckResultPack(t *testing.T, project string) {
	t.Helper()
	dir := filepath.Join(project, ".orquestalite", "packs", "development", "6", "schemas")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "verdict@1.json"), []byte(checkResultSchema), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFlowCheckResultResolvesInstalledSchemaByRef(t *testing.T) {
	project := t.TempDir()
	writeCheckResultPack(t, project)
	result := filepath.Join(project, "result.json")
	if err := os.WriteFile(result, []byte(`{"status":"complete"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := FlowCLI(t.Context(), project, []string{"check-result", "schema:verdict@1", result}, &out); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	if !strings.Contains(out.String(), "valid") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestFlowCheckResultReportsTheViolation(t *testing.T) {
	project := t.TempDir()
	writeCheckResultPack(t, project)
	result := filepath.Join(project, "result.json")
	if err := os.WriteFile(result, []byte(`{"status":"done"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	err := FlowCLI(t.Context(), project, []string{"check-result", "verdict@1", result}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `"done"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestFlowCheckResultAcceptsASchemaPath(t *testing.T) {
	project := t.TempDir()
	schema := filepath.Join(project, "verdict.json")
	if err := os.WriteFile(schema, []byte(checkResultSchema), 0o644); err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(project, "result.json")
	if err := os.WriteFile(result, []byte(`{"status":"partial"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := FlowCLI(t.Context(), project, []string{"check-result", schema, result}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}
