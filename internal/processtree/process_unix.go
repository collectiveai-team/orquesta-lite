//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

// Package processtree configures cancellation of owned subprocess groups.
package processtree

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

func Configure(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
}
