package procgroup

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// forker runs every fork on one OS thread that never exits. Pdeathsig fires
// when the *thread* that forked the child dies, not the process, and Go
// retires threads freely. A locked goroutine that never returns keeps its
// thread alive for the life of Commander, so the signal fires only when
// Commander itself goes away.
type forker struct {
	once sync.Once
	jobs chan forkJob
}

type forkJob struct {
	cmd  *exec.Cmd
	done chan error
}

var theForker forker

func (f *forker) start(cmd *exec.Cmd) error {
	f.once.Do(func() {
		f.jobs = make(chan forkJob)
		go f.loop()
	})
	j := forkJob{cmd: cmd, done: make(chan error, 1)}
	f.jobs <- j
	return <-j.done
}

func (f *forker) loop() {
	lockThread()
	for j := range f.jobs {
		j.done <- j.cmd.Start()
	}
}

type group struct {
	mu     sync.Mutex
	pgid   int
	closed bool
}

// New returns an empty group; Start fills it.
func New() (Group, error) { return &group{}, nil }

func (g *group) Start(cmd *exec.Cmd) error {
	g.mu.Lock()
	busy := g.pgid != 0 || g.closed
	g.mu.Unlock()
	if busy {
		return errors.New("this process group was already started or closed")
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Setpgid makes the child a group leader so -pgid reaches grandchildren.
	// Pdeathsig covers the leader only; ReapOrphans covers the rest after a crash.
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
	if err := theForker.start(cmd); err != nil {
		return err
	}
	g.mu.Lock()
	g.pgid = cmd.Process.Pid
	g.mu.Unlock()
	return nil
}

func (g *group) signal(sig syscall.Signal) error {
	g.mu.Lock()
	pgid := g.pgid
	g.mu.Unlock()
	if pgid <= 0 {
		return nil // never started, or already closed
	}
	if err := syscall.Kill(-pgid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func (g *group) Terminate() error { return g.signal(syscall.SIGTERM) }
func (g *group) Kill() error      { return g.signal(syscall.SIGKILL) }

// Close releases nothing on Linux, but forgets the group id so a later Kill
// can't signal a recycled pid. Killing is left to the caller so a clean exit
// isn't turned into a kill. Calling it twice is safe.
func (g *group) Close() error {
	g.mu.Lock()
	g.closed = true
	g.pgid = 0
	g.mu.Unlock()
	return nil
}

// ReapOrphans kills processes of this user that carry another Commander
// instance's id in their environment. That finds grandchildren a crashed
// Commander left behind. It returns how many it signalled.
//
// It must only run after this process has won the single-instance check
// (gate.Listen succeeded). Otherwise a second launch would kill the agents of
// the live first instance.
func ReapOrphans(current string) (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, fmt.Errorf("can't read /proc: %w", err)
	}
	uid := uint32(os.Getuid())
	self := os.Getpid()
	prefix := []byte(InstanceEnv + "=")
	n := 0
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Uid != uid {
			continue
		}
		data, err := os.ReadFile("/proc/" + e.Name() + "/environ")
		if err != nil {
			continue
		}
		for _, kv := range bytes.Split(data, []byte{0}) {
			if !bytes.HasPrefix(kv, prefix) {
				continue
			}
			id := strings.TrimSpace(string(kv[len(prefix):]))
			if id != "" && id != current && stillOrphan(pid, id, current) {
				// Kill the whole group when the process leads one, so its
				// descendants go too; otherwise just the process.
				target := pid
				if pg, err := syscall.Getpgid(pid); err == nil && pg == pid {
					target = -pid
				}
				if syscall.Kill(target, syscall.SIGKILL) == nil {
					n++
				}
			}
			break
		}
	}
	return n, nil
}

// stillOrphan re-reads the environment right before the kill, which narrows
// the window in which the pid could have been recycled by an unrelated
// process since the scan.
func stillOrphan(pid int, id, current string) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return false
	}
	want := []byte(InstanceEnv + "=" + id)
	for _, kv := range bytes.Split(data, []byte{0}) {
		if bytes.Equal(kv, want) {
			return id != current
		}
	}
	return false
}
