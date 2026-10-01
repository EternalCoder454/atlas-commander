package procgroup

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type group struct {
	mu      sync.Mutex
	job     windows.Handle
	started bool
}

// New creates a Job Object that kills its members when the last handle to it
// closes, which includes Commander crashing.
func New() (Group, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("can't create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("can't configure job object: %w", err)
	}
	return &group{job: job}, nil
}

// Start starts cmd, then assigns it to the job. There is a window of a few
// milliseconds between the two in which a child the agent spawns would escape
// the job. That is acceptable: the agent is a runtime that spends far longer
// than that starting up before it launches anything. CREATE_SUSPENDED would
// close it, but os/exec doesn't expose the thread handle needed to resume.
func (g *group) Start(cmd *exec.Cmd) error {
	g.mu.Lock()
	if g.started || g.job == 0 {
		g.mu.Unlock()
		return errors.New("this process group was already started or closed")
	}
	g.started = true
	g.mu.Unlock()
	if err := cmd.Start(); err != nil {
		return err
	}
	const access = windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE
	h, err := windows.OpenProcess(access, false, uint32(cmd.Process.Pid))
	if err != nil {
		cmd.Process.Kill()
		return fmt.Errorf("can't open the started process: %w", err)
	}
	defer windows.CloseHandle(h)
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := windows.AssignProcessToJobObject(g.job, h); err != nil {
		cmd.Process.Kill()
		return fmt.Errorf("can't add the process to its job: %w", err)
	}
	return nil
}

func (g *group) Terminate() error { return g.Kill() }

func (g *group) Kill() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.job == 0 {
		return nil
	}
	return windows.TerminateJobObject(g.job, 1)
}

// Close is safe to call twice; the second call does nothing, and Kill after
// Close is a no-op.
func (g *group) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.job == 0 {
		return nil
	}
	err := windows.CloseHandle(g.job)
	g.job = 0
	return err
}

// ReapOrphans is a no-op on Windows: kill-on-close already ended everything
// a crashed Commander started.
func ReapOrphans(current string) (int, error) { return 0, nil }
