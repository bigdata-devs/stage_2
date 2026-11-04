//go:build unix

package localstate

import (
	"fmt"
	"os"
	"syscall"
)

func lockExclusive(file *os.File) error {
	return flock(file, syscall.LOCK_EX)
}

func lockShared(file *os.File) error {
	return flock(file, syscall.LOCK_SH)
}

func unlock(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

func flock(file *os.File, mode int) error {
	if err := syscall.Flock(int(file.Fd()), mode); err != nil {
		return fmt.Errorf("lock %s: %w", file.Name(), err)
	}
	return nil
}
