package gate

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Decider answers a Request. It may block for a long time (waiting for the
// user, or while the agent is held). ctx is cancelled when the hook
// connection drops, for example because the agent was killed.
type Decider func(ctx context.Context, r Request) Decision

// Server accepts hook requests on a local socket.
type Server struct {
	ln     net.Listener
	addr   string
	decide Decider

	mu         sync.Mutex
	tokens     map[string]string
	onActivate func()
	conns      map[net.Conn]struct{}
	closed     bool
	wg         sync.WaitGroup

	lock   *os.File // exclusive launch lock, held until Close
	ctx    context.Context
	cancel context.CancelFunc
	sem    chan struct{}
}

// Listen creates dir (mode 0700) and listens on dir/gate.sock. The dir must
// be private to the user (see the package comment for Windows).
//
// An exclusive lock on dir/commander.lock, held for the server's lifetime,
// serialises concurrent launches; if it is held, or something answers on the
// socket, ErrRunning is returned. A leftover socket file nobody answers on
// (connection refused or missing) is replaced. Anything else at the path, such
// as a regular file, or a dial that fails any other way, is never removed.
func Listen(dir string, decide Decider) (*Server, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("can't create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("can't secure %s: %w", dir, err)
	}
	lock, err := lockFile(filepath.Join(dir, "commander.lock"))
	if err != nil {
		return nil, err
	}
	addr := filepath.Join(dir, "gate.sock")
	if fi, err := os.Lstat(addr); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			unlockFile(lock)
			return nil, fmt.Errorf("%s exists and is not a socket", addr)
		}
		c, err := net.DialTimeout("unix", addr, readTimeout)
		if err == nil {
			c.Close()
			unlockFile(lock)
			return nil, ErrRunning
		}
		if !isStaleDialError(err) {
			unlockFile(lock)
			return nil, ErrRunning
		}
		os.Remove(addr)
	}
	ln, err := net.Listen("unix", addr)
	if err != nil {
		unlockFile(lock)
		return nil, fmt.Errorf("can't listen on %s: %w", addr, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{ln: ln, addr: addr, decide: decide,
		tokens: map[string]string{}, conns: map[net.Conn]struct{}{},
		lock: lock, ctx: ctx, cancel: cancel, sem: make(chan struct{}, maxConns)}
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

// Addr is the socket path to put in ATLAS_GATE_ADDR.
func (s *Server) Addr() string { return s.addr }

// Register issues a token for an agent. Requests carrying it are attributed
// to that agent.
func (s *Server) Register(agentID string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the OS random source failing is unrecoverable
	}
	tok := hex.EncodeToString(b)
	s.mu.Lock()
	s.tokens[tok] = agentID
	s.mu.Unlock()
	return tok
}

// Unregister revokes a token.
func (s *Server) Unregister(token string) {
	s.mu.Lock()
	delete(s.tokens, token)
	s.mu.Unlock()
}

// SetOnActivate sets what runs when a second launch asks to show the window.
// It is called from a server goroutine, not the UI thread.
func (s *Server) SetOnActivate(f func()) {
	s.mu.Lock()
	s.onActivate = f
	s.mu.Unlock()
}

// Close stops listening, cancels pending deciders, drops open connections and
// waits up to five seconds for handlers to finish, so a stuck Decider can't
// hang shutdown. It then releases the launch lock.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancel()
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	err := s.ln.Close()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(closeWait):
	}
	unlockFile(s.lock)
	return err
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		select {
		case s.sem <- struct{}{}:
		default:
			c.Close() // over the connection cap
			continue
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			<-s.sem
			c.Close()
			return
		}
		s.conns[c] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.handle(c)
	}
}

func (s *Server) handle(c net.Conn) {
	defer s.wg.Done()
	defer func() { <-s.sem }()
	defer func() {
		c.Close()
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
	}()
	enc := json.NewEncoder(c)

	// Deadline covers the request line only; cleared before the decision wait.
	c.SetReadDeadline(deadline())
	br := bufio.NewReaderSize(io.LimitReader(c, maxLine+1), 4096)
	line, err := br.ReadBytes('\n')
	if err != nil && !(errors.Is(err, io.EOF) && len(line) > 0) {
		return
	}
	if len(line) > maxLine {
		enc.Encode(reply{Reason: "request too large"})
		return
	}
	c.SetReadDeadline(zeroTime)

	var m message
	if json.Unmarshal(line, &m) != nil {
		enc.Encode(reply{Reason: "bad request"})
		return
	}
	switch m.Op {
	case "activate":
		s.mu.Lock()
		f := s.onActivate
		s.mu.Unlock()
		if f != nil {
			f()
		}
		enc.Encode(reply{OK: true})
	case "gate":
		s.gate(c, enc, m)
	default:
		enc.Encode(reply{Reason: "unknown request"})
	}
}

func (s *Server) gate(c net.Conn, enc *json.Encoder, m message) {
	s.mu.Lock()
	agent, ok := s.tokens[m.Token]
	s.mu.Unlock()
	if !ok || m.Request == nil {
		enc.Encode(reply{Reason: "unknown session"})
		return
	}
	req := *m.Request
	req.AgentID = agent // never trust the client's claim
	req.Token = m.Token

	// The client sends nothing after its request, so any read result means it
	// hung up; that cancels the decision.
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	go func() {
		io.Copy(io.Discard, c)
		cancel()
	}()
	d := s.safeDecide(ctx, req)
	if ctx.Err() != nil {
		return
	}
	enc.Encode(reply{Allow: d.Allow, Reason: d.Reason})
}

// safeDecide turns a panic in the Decider into a deny, so a bug there blocks
// one tool call instead of crashing Commander or leaving the hook hanging.
func (s *Server) safeDecide(ctx context.Context, r Request) (d Decision) {
	defer func() {
		if recover() != nil {
			d = Decision{Reason: "Commander hit an error deciding this tool call, so it was blocked."}
		}
	}()
	return s.decide(ctx, r)
}
