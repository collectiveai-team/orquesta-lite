package execlock

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLockProcess(t *testing.T) {
	if path := os.Getenv("ORQ_TEST_LOCK"); path != "" {
		release, err := Acquire(path)
		if err != nil {
			os.Exit(23)
		}
		defer release()
		return
	}
	path := filepath.Join(t.TempDir(), "executor.lock")
	release, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	child := func() error {
		cmd := exec.Command(os.Args[0], "-test.run=^TestLockProcess$")
		cmd.Env = append(os.Environ(), "ORQ_TEST_LOCK="+path)
		return cmd.Run()
	}
	if err := child(); err == nil {
		t.Fatal("second process acquired owned lock")
	}
	release()
	if err := child(); err != nil {
		t.Fatalf("recovery: %v", err)
	}
}
