package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunDevelopmentAliasRejectsUnknownCommand(t *testing.T) {
	err := RunDevelopmentAlias(context.Background(), t.TempDir(), "unknown", nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `no development flow alias for "unknown"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestFactoryAliasRunsFactoryGovernedV2(t *testing.T) {
	dir := t.TempDir()
	flowRaw := []byte(`{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"factory-governed","version":"2"},"steps":[{"id":"done","uses":"activity:command.run@1","with":{"argv":["true"]}}],"outputs":{}}`)
	flowPath := filepath.Join(dir, ".orquestalite", "packs", "development", "5", "flows", "factory-governed@2.json")
	if err := os.MkdirAll(filepath.Dir(flowPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(flowPath, flowRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"apiVersion": "orq.pack/v1",
		"name":       "development",
		"version":    "5",
		"files": map[string]string{
			"flows/factory-governed@2.json": fmt.Sprintf("%x", sha256.Sum256(flowRaw)),
		},
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, ".orquestalite", "packs", "development", "5", "pack.json"), manifestRaw, 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err = RunDevelopmentAlias(context.Background(), dir, "factory", nil, &out); err != nil {
		t.Fatal(err)
	}
}
