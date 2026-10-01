package gate

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive, non-blocking LockFileEx lock on path. Windows
// drops it when the process exits.
func lockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("can't open %s: %w", path, err)
	}
	ol := new(windows.Overlapped)
	err = windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if err != nil {
		f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
			return nil, ErrRunning
		}
		return nil, fmt.Errorf("can't lock %s: %w", path, err)
	}
	return f, nil
}

// unlockFile releases the lock by closing the file.
func unlockFile(f *os.File) {
	if f != nil {
		f.Close()
	}
}

// isStaleDialError reports whether a failed dial means the socket file is
// left over. AF_UNIX on Windows reports WSAECONNREFUSED, not ECONNREFUSED.
func isStaleDialError(err error) bool {
	return errors.Is(err, windows.WSAECONNREFUSED) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND)
}
