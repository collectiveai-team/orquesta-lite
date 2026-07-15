package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/cutover"
)

func TestCutoverCLITemplateIsStrictVersionedEvidence(t *testing.T) {
	var out bytes.Buffer
	if err := CutoverCLI(context.Background(), t.TempDir(), []string{"template"}, &out); err != nil {
		t.Fatalf("CutoverCLI: %v", err)
	}
	var evidence cutover.Evidence
	if err := json.Unmarshal(out.Bytes(), &evidence); err != nil {
		t.Fatalf("template JSON: %v", err)
	}
	if evidence.APIVersion != cutover.APIVersion || len(evidence.Parity) == 0 || len(evidence.Chaos) == 0 {
		t.Fatalf("incomplete template: %+v", evidence)
	}
}

func TestCutoverCLICheckPrintsAllFailuresAndReturnsSentinel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "evidence.json")
	raw, err := json.Marshal(cutover.Template())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = CutoverCLI(context.Background(), dir, []string{"check", "--evidence", path}, &out)
	if !errors.Is(err, cutover.ErrGateClosed) {
		t.Fatalf("expected cutover sentinel, got %v", err)
	}
	if !strings.Contains(out.String(), "[FAIL] parity:") || !strings.Contains(out.String(), "cutover gate: CLOSED") {
		t.Fatalf("unexpected output:\n%s", out.String())
	}
}
