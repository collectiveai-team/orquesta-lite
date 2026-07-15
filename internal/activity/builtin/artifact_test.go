package builtin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/activity"
)

func TestArtifactCaptureSupportsTextBytesAndAllowedFile(t *testing.T) {
	root := t.TempDir()
	readRoot := t.TempDir()
	file := filepath.Join(readRoot, "input.txt")
	_ = os.WriteFile(file, []byte("from-file"), 0o644)
	executor := &ArtifactExecutor{Root: root, MaxBytes: 1024, AllowedReadRoots: []string{readRoot}}
	inputs := []map[string]any{
		{"name": "text.txt", "kind": "text", "text": "hello"},
		{"name": "bytes.bin", "kind": "bytes", "bytesBase64": base64.StdEncoding.EncodeToString([]byte{0, 1, 2})},
		{"name": "file.txt", "kind": "file", "file": file},
	}
	for index, input := range inputs {
		raw, _ := json.Marshal(input)
		result, err := executor.Execute(context.Background(), activity.Request{RunID: "r", ScopePath: "root", StepID: "capture", ForeachKey: string(rune('a' + index)), Inputs: raw})
		if err != nil || len(result.Artifacts) != 1 {
			t.Fatalf("input=%+v result=%+v err=%v", input, result, err)
		}
	}
}

func TestArtifactCaptureRejectsFileOutsideAllowedRoot(t *testing.T) {
	file := filepath.Join(t.TempDir(), "secret")
	_ = os.WriteFile(file, []byte("secret"), 0o644)
	raw, _ := json.Marshal(map[string]any{"name": "secret", "kind": "file", "file": file})
	_, err := (&ArtifactExecutor{Root: t.TempDir(), AllowedReadRoots: []string{t.TempDir()}}).Execute(context.Background(), activity.Request{Inputs: raw})
	if activity.Classify(err) != activity.ErrorPermissionDenied {
		t.Fatalf("err=%v", err)
	}
}
