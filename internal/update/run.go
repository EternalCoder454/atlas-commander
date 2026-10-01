package update

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ErrNotUpdated means scripts/update.sh rebuilt and reinstalled Commander but
// could not bring in the newest version (offline, or the local branch has
// diverged). What is installed works; it is just not newer.
var ErrNotUpdated = errors.New("couldn't get the newest version")

// notUpdatedExit is the script's exit code for ErrNotUpdated.
const notUpdatedExit = 2

// LogPath is where scripts/update.sh writes its log.
func LogPath() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "atlas-commander", "update.log")
}

// LogTail is the last maxBytes of the update log, for the "Technical details"
// of a failed update. An unreadable log gives "".
func LogTail(maxBytes int64) string {
	f, err := os.Open(LogPath())
	if err != nil {
		return ""
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > maxBytes {
		if _, err := f.Seek(-maxBytes, 2); err != nil {
			return ""
		}
	}
	b := make([]byte, maxBytes)
	n, _ := f.Read(b)
	return string(b[:n])
}

// Run applies an update to a source install: it runs scripts/update.sh from the
// checkout, which pulls the channel fast-forward only, rebuilds, and reinstalls
// into the prefix the running copy lives under. It blocks for as long as the
// build takes (minutes on a cold cache), so call it off the Qt thread.
//
// It returns nil when the newest version is installed, ErrNotUpdated when the
// script fell back to rebuilding what was there, and another error when the
// build failed; the log has the details in each case.
func Run(in Install, channel string) error {
	if !in.SelfUpdatable() {
		return errors.New("this copy can't update itself")
	}
	script := filepath.Join(in.Source, "scripts", "update.sh")
	if _, err := os.Stat(script); err != nil {
		return errors.New("the update script is missing from " + in.Source)
	}
	args := []string{script, channel}
	if p := in.Prefix(); p != "" {
		args = append(args, p)
	}
	cmd := exec.Command("bash", args...)
	cmd.Dir = in.Source
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ee) && ee.ExitCode() == notUpdatedExit:
		return ErrNotUpdated
	case errors.As(err, &ee):
		return fmt.Errorf("the build failed (exit %d)", ee.ExitCode())
	}
	return fmt.Errorf("couldn't run the update script: %w", err)
}
