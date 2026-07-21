package commands

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestWatch_V2FailsFastOnMissingFlow(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := Watch(ctx, WatchOptions{
		ProjectDir: dir,
		Engine:     "v2",
		Issues:     true,
		Out:        io.Discard,
	})
	if err == nil {
		t.Fatal("expected startup error for missing v2 flow")
	}
	if !strings.Contains(err.Error(), "development/issue-fix@1") {
		t.Fatalf("error should name the flow ref, got: %v", err)
	}
}
