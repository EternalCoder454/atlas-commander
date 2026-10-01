package atomicfile

import (
	"os"
	"time"
)

// rename retries because antivirus or an editor can briefly hold the target
// open, which makes the rename fail with a sharing violation.
func rename(from, to string) error {
	var err error
	for i := 0; i < 5; i++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return err
}

// syncDir does nothing: Windows cannot fsync a directory.
func syncDir(string) {}
