//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package processtree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCancelStopsDescendant(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", `(while :; do echo x >> "$1"; sleep 0.02; done) & wait`, "sh", marker)
	Configure(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if b, _ := os.ReadFile(marker); len(b) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation exceeded bound")
	}
	before, _ := os.ReadFile(marker)
	time.Sleep(150 * time.Millisecond)
	after, _ := os.ReadFile(marker)
	if len(before) != len(after) {
		t.Fatal("descendant survived cancellation")
	}
}
