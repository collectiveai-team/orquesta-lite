package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadAll(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.md")
	if value, err := ReadAll(missing); err != nil || value != "" {
		t.Fatalf("missing value=%q err=%v", value, err)
	}
	path := filepath.Join(dir, "memory.md")
	if err := os.WriteFile(path, []byte("durable context"), 0o644); err != nil {
		t.Fatal(err)
	}
	if value, err := ReadAll(path); err != nil || value != "durable context" {
		t.Fatalf("value=%q err=%v", value, err)
	}
}
