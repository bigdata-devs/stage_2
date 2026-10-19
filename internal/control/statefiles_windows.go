//go:build windows

package control

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

const wholeFileLength = 0xFFFFFFFF

// lockExclusive blocks until an exclusive byte-range lock over the whole file is taken.
func lockExclusive(file *os.File) error {
	return lockRange(file, windows.LOCKFILE_EXCLUSIVE_LOCK)
}

// lockShared blocks until a shared byte-range lock over the whole file is taken.
func lockShared(file *os.File) error {
	return lockRange(file, 0)
}

// unlock releases the byte-range lock; the deferred file.Close would release it anyway.
func unlock(file *os.File) {
	overlapped := new(windows.Overlapped)
	_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, wholeFileLength, wholeFileLength, overlapped)
}

func lockRange(file *os.File, flags uint32) error {
	overlapped := new(windows.Overlapped)
	err := windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, wholeFileLength, wholeFileLength, overlapped)
	if err != nil {
		return fmt.Errorf("lock %s: %w", file.Name(), err)
	}
	return nil
}
