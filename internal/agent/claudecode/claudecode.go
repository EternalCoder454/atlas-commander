// Package claudecode is the agent backend that drives the Claude Code CLI in
// its headless stream-json mode.
//
// Goroutine model: each Session starts up to four goroutines. One reads stdout
// lines, feeds them to the parser and sends events (blocking, so none is
// lost); a supervising one waits for that reader and the process, then emits
// EventExit last and closes the events channel; one waits for the process; the
// stderr copy is done by os/exec. Send, Stop and Kill may be called from any
// goroutine and never block on the events consumer.
//
// Platform split: the process group comes from the NewGroup factory
// (internal/procgroup), so this package has no platform files. Windows only
// differs in Detect (Git Bash check) and in how the hook path is quoted.
package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"atlas-commander/internal/agent"
	"atlas-commander/internal/procgroup"
)

// Options configures the backend.
type Options struct {
	// Binary is the claude executable; "" looks it up on PATH.
	Binary string
	// HookCommand is the absolute path of atlas-hook; "" registers no hook.
	HookCommand string
	// HookTimeout bounds one hook call; a human may be deciding, so it is
	// long. Zero leaves Claude Code's default.
	HookTimeout time.Duration
	// NewGroup makes the process group each session runs in.
	NewGroup func() (procgroup.Group, error)
	// ExtraEnv is added to every session's environment.
	ExtraEnv []string
}

const (
	stderrTail      = 4096
	eventBufferSize = 256
)

// Stop's grace periods; variables so tests do not wait the full time.
var (
	stopGrace      = 5 * time.Second
	terminateGrace = 3 * time.Second
	readGrace      = 2 * time.Second // how long stdout may outlast the process
)

type backend struct{ o Options }

// New returns the Claude Code backend.
func New(o Options) agent.Backend { return &backend{o: o} }

func (b *backend) Name() string { return agent.BackendClaudeCode }

func (b *backend) Start(ctx context.Context, spec agent.Spec) (agent.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.o.NewGroup == nil {
		return nil, errors.New("no process group factory is configured")
	}
	bin := b.o.Binary
	if bin == "" {
		p, err := exec.LookPath("claude")
		if err != nil {
			return nil, errors.New("claude was not found; install it with: npm install -g @anthropic-ai/claude-code")
		}
		bin = p
	}
	args, err := buildArgs(b.o, spec)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(bin, args...) // not CommandContext: ctx bounds only the start
	cmd.Dir = spec.WorkDir
	env := append(os.Environ(), b.o.ExtraEnv...)
	cmd.Env = append(env, spec.Env...) // later entries win

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// A raw os.Pipe for stdout, so cmd.Wait reports process exit without
	// waiting for (or closing) our reader: a stalled event consumer must never
	// stop Stop from returning.
	pr, pw, err := os.Pipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	cmd.Stdout = pw
	tail := &tailBuffer{max: stderrTail}
	cmd.Stderr = tail
	cmd.WaitDelay = 2 * time.Second

	group, err := b.o.NewGroup()
	if err != nil {
		stdin.Close()
		pr.Close()
		pw.Close()
		return nil, fmt.Errorf("could not create a process group: %w", err)
	}
	if err := group.Start(cmd); err != nil {
		stdin.Close()
		pr.Close()
		pw.Close()
		group.Close()
		return nil, fmt.Errorf("could not start claude: %w", err)
	}
	pw.Close() // the child holds its own copy

	s := &session{
		stdin:  stdin,
		group:  group,
		events: make(chan agent.Event, eventBufferSize),
		exited: make(chan struct{}),
		stderr: tail,
	}
	go func() {
		s.waitErr = cmd.Wait()
		if errors.Is(s.waitErr, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
			// A grandchild kept stderr open past the exit; claude itself was fine.
			s.waitErr = nil
		}
		// Whether Stop or Kill got there first decides how the exit is
		// reported; one that arrives later changes nothing.
		s.stoppedAtExit = s.stopped.Load()
		close(s.exited)
	}()
	go s.read(pr)

	if spec.Prompt != "" {
		if err := s.Send(spec.Prompt); err != nil {
			s.Kill()
			return nil, fmt.Errorf("could not send the first prompt: %w", err)
		}
	}
	return s, nil
}

func buildArgs(o Options, spec agent.Spec) ([]string, error) {
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", spec.SystemPrompt)
	}
	if spec.ResumeID != "" {
		args = append(args, "--resume", spec.ResumeID)
	}
	if o.HookCommand != "" {
		settings, err := hookSettings(o.HookCommand, o.HookTimeout, runtime.GOOS)
		if err != nil {
			return nil, err
		}
		args = append(args, "--settings", settings)
	}
	return args, nil
}

// hookSettings builds the --settings JSON that registers atlas-hook for every
// tool call. The command is quoted because install paths contain spaces
// ("Atlas Commander"), and a path may hold $ or ' too. On Windows Claude Code runs hook commands through Git
// Bash, which wants forward slashes: a backslash path would be eaten as
// escapes.
func hookSettings(hook string, timeout time.Duration, goos string) (string, error) {
	if goos == "windows" {
		hook = strings.ReplaceAll(hook, `\`, "/")
	}
	type hookCmd struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout,omitempty"`
	}
	type matcher struct {
		Matcher string    `json:"matcher"`
		Hooks   []hookCmd `json:"hooks"`
	}
	secs := 0 // zero is omitted, which leaves Claude Code's default
	if timeout > 0 {
		secs = int((timeout + time.Second - 1) / time.Second)
	}
	cfg := struct {
		Hooks map[string][]matcher `json:"hooks"`
	}{Hooks: map[string][]matcher{
		"PreToolUse": {{Matcher: "*", Hooks: []hookCmd{{Type: "command", Command: quoteHook(hook, goos), Timeout: secs}}}},
	}}
	b, err := json.Marshal(cfg)
	return string(b), err
}

// quoteHook quotes the hook path for the shell Claude Code runs it in. On
// Windows that is Git Bash and the path has no quotes (Windows forbids `"` in
// file names), so double quotes are enough. Elsewhere it is sh, where single
// quotes switch off every expansion ($, backticks); a literal ' is written
// as '\” (close, escaped quote, reopen).
func quoteHook(hook, goos string) string {
	if goos == "windows" {
		return `"` + hook + `"`
	}
	return `'` + strings.ReplaceAll(hook, `'`, `'\''`) + `'`
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

type session struct {
	stdin  io.WriteCloser
	group  procgroup.Group
	events chan agent.Event
	stderr *tailBuffer

	writeMu sync.Mutex // serialises Send
	stdinMu sync.Once  // closes stdin once

	exited        chan struct{} // closed when the process has been waited for
	waitErr       error         // valid after exited is closed
	stoppedAtExit bool          // valid after exited is closed
	stopped       atomic.Bool   // Stop or Kill was called while the process ran

	groupMu     sync.Mutex // guards group against use after Close
	groupClosed bool
}

// hasExited reports whether the process has been waited for.
func (s *session) hasExited() bool {
	select {
	case <-s.exited:
		return true
	default:
		return false
	}
}

// groupDo runs f on the process group unless it is already closed.
func (s *session) groupDo(f func(procgroup.Group) error) error {
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	if s.groupClosed {
		return nil
	}
	return f(s.group)
}

func (s *session) closeGroup() {
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	if !s.groupClosed {
		s.groupClosed = true
		s.group.Close()
	}
}

func (s *session) Events() <-chan agent.Event { return s.events }

var errEnded = errors.New("the session has ended")

func (s *session) Send(text string) error {
	select {
	case <-s.exited:
		return errEnded
	default:
	}
	line, err := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": text},
	})
	if err != nil {
		return err
	}
	line = append(line, '\n')
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.stopped.Load() {
		return errEnded
	}
	if _, err := s.stdin.Write(line); err != nil {
		return errEnded
	}
	return nil
}

func (s *session) closeStdin() { s.stdinMu.Do(func() { s.stdin.Close() }) }

func (s *session) Stop() error {
	if s.hasExited() {
		return nil
	}
	s.stopped.Store(true)
	s.closeStdin() // end of input is the polite way to ask claude to finish
	if s.waitExit(stopGrace) {
		return nil
	}
	s.groupDo(procgroup.Group.Terminate)
	if s.waitExit(terminateGrace) {
		return nil
	}
	err := s.groupDo(procgroup.Group.Kill)
	<-s.exited
	return err
}

func (s *session) Kill() error {
	if s.hasExited() {
		return nil
	}
	s.stopped.Store(true)
	s.closeStdin()
	return s.groupDo(procgroup.Group.Kill)
}

func (s *session) waitExit(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-s.exited:
		return true
	case <-t.C:
		return false
	}
}

// read parses stdout until EOF, then reports the exit. Lines can be several
// MB (a Read of a big file), so it uses bufio.Reader.ReadBytes rather than a
// Scanner with its fixed token limit.
//
// Stdout reaching EOF needs every holder of the pipe to exit, and a dev server
// the Bash tool left running holds it. So once the process itself has exited
// the rest of its group is killed, and if stdout still has not ended 2 s
// later the read end is closed. EventExit is always sent and Events always
// closed.
func (s *session) read(r io.ReadCloser) {
	defer close(s.events)
	defer r.Close()
	var sending atomic.Bool // the loop is blocked on a slow consumer, not on stdout
	var progress atomic.Int64
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		var p parser
		br := bufio.NewReaderSize(r, 64*1024)
		for {
			line, err := br.ReadBytes('\n')
			if line = bytes.TrimSpace(line); len(line) > 0 {
				for _, ev := range p.parse(line, time.Now()) {
					sending.Store(true)
					s.events <- ev
					sending.Store(false)
				}
			}
			progress.Add(1)
			if err != nil {
				return
			}
		}
	}()

	select {
	case <-loopDone:
	case <-s.exited:
		// Leftover children (a dev server, a watcher) must not outlive the agent.
		s.groupDo(procgroup.Group.Kill)
		for last := progress.Load(); ; {
			select {
			case <-loopDone:
			case <-time.After(readGrace):
				if sending.Load() || progress.Load() != last {
					last = progress.Load()
					continue
				}
				r.Close() // unblocks the pending read
				<-loopDone
			}
			break
		}
	}
	<-s.exited
	s.closeGroup()
	s.events <- s.exitEvent()
}

func (s *session) exitEvent() agent.Event {
	ev := agent.Event{Kind: agent.EventExit, Time: time.Now()}
	if s.stoppedAtExit {
		ev.Text = "stopped"
		return ev
	}
	if s.waitErr == nil {
		ev.Text = "Claude Code exited."
		return ev
	}
	ev.IsError = true
	var ee *exec.ExitError
	switch {
	case errors.As(s.waitErr, &ee) && ee.ExitCode() >= 0:
		ev.Text = "Claude Code exited with code " + strconv.Itoa(ee.ExitCode())
	case ee != nil:
		ev.Text = "Claude Code was ended (" + ee.ProcessState.String() + ")"
	default:
		ev.Text = "Claude Code failed: " + s.waitErr.Error()
	}
	if t := s.stderr.String(); t != "" {
		ev.Text += ": " + t
	} else {
		ev.Text += "."
	}
	return ev
}
