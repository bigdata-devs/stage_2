//go:build windows

package localstate

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

const wholeFileLength = 0xFFFFFFFF

func lockExclusive(file *os.File) error {
	return lockRange(file, windows.LOCKFILE_EXCLUSIVE_LOCK)
}

func lockShared(file *os.File) error {
	return lockRange(file, 0)
}

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
