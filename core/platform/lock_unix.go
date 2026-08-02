//go:build unix

package platform

import (
	"os"
	"syscall"
)

// lockFile takes an exclusive advisory lock without blocking. The lock is
// released by the kernel if the process dies, so a crash cannot leave a stale
// lock behind — which is why this is flock rather than a PID file.
func lockFile(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if err == syscall.EWOULDBLOCK {
			return ErrLocked
		}
		return err
	}
	return nil
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
