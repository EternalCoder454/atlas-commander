//go:build unix

package update

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// CanSelfInstall is true here: pulling the checkout and running
// scripts/update.sh is how a Linux install of Commander updates itself.
const CanSelfInstall = true

// PlatformWording has nothing to add on Unix; the package manager or the
// missing checkout is the whole story. See Wording.
func PlatformWording() (heading, body string, ok bool) { return "", "", false }

// Relaunch starts binary again once this process has exited. The new copy has
// to wait: Commander's single-instance check is a socket this process holds, and
// a second launch that finds it taken just asks the first to raise its window
// and exits. The helper polls for this process to be gone, for at most about
// twenty seconds, then replaces itself with the new binary. It is detached so
// that it outlives us.
func Relaunch(binary string) error {
	const script = `n=0; while kill -0 "$1" 2>/dev/null && [ "$n" -lt 100 ]; do sleep 0.2; n=$((n+1)); done; exec "$2"`
	cmd := exec.Command("sh", "-c", script, "atlas-relaunch", fmt.Sprint(os.Getpid()), binary)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

// FindTerminal returns the first terminal emulator that is installed, or nil.
// The dialog offers "Run in Terminal" only when there is one: a button that
// silently does nothing is worse than no button.
func FindTerminal() *Terminal {
	for _, t := range terminals {
		if which(t.cmd) != "" {
			found := t
			return &found
		}
	}
	return nil
}

// Run opens the terminal on cmd. It is detached into its own session, so it
// stays open if Commander quits, and reaped in the background.
func (t Terminal) Run(cmd string) error {
	c := exec.Command(t.cmd, t.argv(cmd)...)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return err
	}
	go c.Wait()
	return nil
}
