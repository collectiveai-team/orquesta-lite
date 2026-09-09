package processtree

import (
	"os/exec"
	"strconv"
	"time"
)

func Configure(cmd *exec.Cmd) {
	cmd.WaitDelay = time.Second
	cmd.Cancel = func() error {
		// taskkill /T covers descendants on Windows; direct kill remains a fallback.
		err := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
		if err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
