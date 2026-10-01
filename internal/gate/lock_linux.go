package gate

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// lockFile takes an exclusive, non-blocking flock on path. The kernel drops
// the lock if the process dies, so a crash never leaves it stuck.
func lockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("can't open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
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
// left over: nobody is listening, or the file vanished.
func isStaleDialError(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT)
}
