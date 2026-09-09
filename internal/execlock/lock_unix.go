//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

// Package execlock provides crash-released local executor exclusion.
package execlock

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

// Acquire is nonblocking. Never unlink the lock file: waiters must share its inode.
func Acquire(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("executor already active or lock unavailable (%s): %w", path, err)
	}
	return func() { _ = f.Close() }, nil
}
