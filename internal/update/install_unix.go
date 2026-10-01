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
