//go:build !windows

package atomicfile

import "os"

func rename(from, to string) error { return os.Rename(from, to) }

// syncDir makes the rename itself durable across power loss.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
