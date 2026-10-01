package procgroup

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// startTree starts a leader with a backgrounded grandchild and returns the
// leader and the grandchild's pid (printed by the shell).
func startTree(t *testing.T, g Group, env ...string) (*exec.Cmd, int) {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep 30 & echo $!; sleep 30")
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Start(cmd); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 32)
	n, _ := out.Read(buf)
	gc, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil {
		t.Fatalf("grandchild pid: %v", err)
	}
	return cmd, gc
}

func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	// A zombie still answers signal 0; check its state.
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	s := string(b)
	i := strings.LastIndex(s, ")")
	return i < 0 || len(s) < i+3 || s[i+2] != 'Z'
}

func waitDead(t *testing.T, pid int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if !alive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("pid %d still running, want it ended", pid)
}

func testStop(t *testing.T, stop func(Group) error) {
	g, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	cmd, gc := startTree(t, g)
	if err := stop(g); err != nil {
		t.Fatalf("stop: %v", err)
	}
	cmd.Wait()
	waitDead(t, gc)
	if err := stop(g); err != nil {
		t.Errorf("second stop after exit: got %v, want nil (ESRCH is not an error)", err)
	}
}

// Kill must reach the grandchild too, or a stopped agent leaves work running.
func TestKillEndsLeaderAndGrandchild(t *testing.T) {
	testStop(t, Group.Kill)
}

// Terminate has the same reach as Kill, just politely.
func TestTerminateEndsLeaderAndGrandchild(t *testing.T) {
	testStop(t, Group.Terminate)
}

// ReapOrphans must only touch processes from a different instance, or a
// healthy fleet would be killed at startup.
func TestReapOrphansKillsOtherInstanceOnly(t *testing.T) {
	g1, _ := New()
	g2, _ := New()
	defer g1.Close()
	defer g2.Close()
	oldCmd, oldGC := startTree(t, g1, InstanceEnv+"=old-instance")
	curCmd, curGC := startTree(t, g2, InstanceEnv+"=current-instance")
	defer func() {
		g1.Kill()
		g2.Kill()
		oldCmd.Wait()
		curCmd.Wait()
	}()
	n, err := ReapOrphans("current-instance")
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Errorf("reaped %d, want at least 1 (the old group)", n)
	}
	oldCmd.Wait()
	waitDead(t, oldGC)
	if !alive(curGC) || !alive(curCmd.Process.Pid) {
		t.Errorf("current-instance process was killed, want it left alone")
	}
}

// Close twice must be safe, and Kill afterwards must not signal a pgid that
// may have been recycled.
func TestKillAfterCloseIsNoOp(t *testing.T) {
	g, _ := New()
	cmd, gc := startTree(t, g)
	defer func() {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		cmd.Wait()
	}()
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Errorf("second Close: got %v, want nil", err)
	}
	if err := g.Kill(); err != nil {
		t.Errorf("Kill after Close: got %v, want nil", err)
	}
	if err := g.Terminate(); err != nil {
		t.Errorf("Terminate after Close: got %v, want nil", err)
	}
	if !alive(gc) {
		t.Error("grandchild was killed by Kill after Close, want untouched")
	}
}

// A group owns one command; reusing it would orphan the first.
func TestStartTwiceFails(t *testing.T) {
	g, _ := New()
	cmd, _ := startTree(t, g)
	defer func() {
		g.Kill()
		cmd.Wait()
	}()
	if err := g.Start(exec.Command("true")); err == nil {
		t.Error("second Start: got nil, want error")
	}
}
