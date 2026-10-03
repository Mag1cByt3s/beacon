//go:build unix

// The line above is a build constraint: this file is only compiled on
// Unix-like systems (Linux, FreeBSD, macOS), where flock exists.

package queue

import (
	"os"
	"syscall"
)

// lock waits until this process holds an exclusive lock on f. The lock is
// released by unlock, or automatically if the process dies.
func lock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

func unlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
