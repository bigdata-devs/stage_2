//go:build unix

package control

import (
	"fmt"
	"os"
	"syscall"
)

// lockExclusive blocks until an exclusive flock over the whole file is taken.
func lockExclusive(file *os.File) error {
	return flock(file, syscall.LOCK_EX)
}

// lockShared blocks until a shared flock over the whole file is taken.
func lockShared(file *os.File) error {
	return flock(file, syscall.LOCK_SH)
}

// unlock releases the flock; the deferred file.Close would release it anyway.
func unlock(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

func flock(file *os.File, mode int) error {
	if err := syscall.Flock(int(file.Fd()), mode); err != nil {
		return fmt.Errorf("lock %s: %w", file.Name(), err)
	}
	return nil
}
