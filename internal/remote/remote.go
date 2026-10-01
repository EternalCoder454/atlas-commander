// Package remote is the phone access server: a small HTTPS API that lets the
// Android app watch and steer the fleet while Commander runs on the PC. The
// contract is docs/phone-api.md; this package implements it and nothing else.
//
// Goroutines: net/http runs each request on its own goroutine, so every call
// into the Controller comes from a goroutine other than the Qt thread. The
// Controller (the supervisor, or the demo fleet) guards its own state with a
// mutex, which is what makes that safe. Server's own state (listener, token,
// rate-limit table) is guarded by its mutexes. Host owns a Server and is
// called from the Qt thread; it only takes a mutex briefly, so the 250 ms UI
// tick can read its status without blocking. Platform: none; the only
// platform-specific part, file modes, is a no-op on Windows.
package remote

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"syscall"
	"time"

	"atlas-commander/internal/fleet"
)

// Controller is what the server needs from the fleet. It is the part of the
// UI's Controller that the phone can use; both the supervisor and the demo
// fleet satisfy it.
type Controller interface {
	Snapshot() *fleet.Snapshot
	Transcript(agentID string, from int) ([]fleet.Entry, int)
	Start(agentID, prompt string) error
	Send(agentID, text string) error
	Hold(agentID string) error
	Resume(agentID string) error
	Stop(agentID string) error
	Kill(agentID string) error
	KillAll() int
	Decide(approvalID string, allow bool, reason string) error
}

const (
	maxConns       = 32
	maxHeaderBytes = 16 << 10
	stopGrace      = 2 * time.Second
)

// DefaultPort is where the server listens unless the user picks another.
const DefaultPort = 47821

// Options configures a Server.
type Options struct {
	Controller Controller
	// Dir holds cert.pem, key.pem and token; it is created if missing.
	Dir     string
	Name    string // the PC's name, shown on the phone
	Version string
	Demo    bool
}

// Server is the HTTPS endpoint. Create it with New, then Start and Stop it
// as often as needed.
type Server struct {
	opt  Options
	cert tls.Certificate
	fp   string
	// notice is set once in New and never changes, so it needs no lock.
	notice string

	mu    sync.Mutex
	token string
	ln    net.Listener
	srv   *http.Server

	limit limiter
}

// New loads, or on first use creates, the certificate and the token.
func New(o Options) (*Server, error) {
	if o.Controller == nil || o.Dir == "" {
		return nil, errors.New("the phone server needs a controller and a folder")
	}
	cert, fp, renewed, err := loadOrCreateCert(o.Dir)
	if err != nil {
		return nil, fmt.Errorf("couldn't prepare the phone certificate: %w", err)
	}
	tok, err := loadOrCreateToken(o.Dir)
	if err != nil {
		return nil, fmt.Errorf("couldn't prepare the phone token: %w", err)
	}
	tightenPerms(o.Dir)
	s := &Server{opt: o, cert: cert, fp: fp, token: tok, limit: limiter{now: time.Now}}
	if renewed {
		s.notice = certRenewedNotice
	}
	return s, nil
}

// Notice is a sentence for the settings page about something the user should
// know, such as a renewed certificate, or "" when there is nothing.
func (s *Server) Notice() string { return s.notice }

// Fingerprint is the lowercase hex SHA-256 of the certificate's DER bytes.
func (s *Server) Fingerprint() string { return s.fp }

// Token is the current bearer token.
func (s *Server) Token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}

// ResetToken makes a new token, so every paired phone is forgotten.
func (s *Server) ResetToken() error {
	tok, err := newToken(s.opt.Dir)
	if err != nil {
		return fmt.Errorf("couldn't make a new phone token: %w", err)
	}
	s.mu.Lock()
	s.token = tok
	s.mu.Unlock()
	return nil
}

// Start listens on addr (for example ":47821"). It returns once the socket is
// open; requests are served on goroutines.
func (s *Server) Start(addr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		return errors.New("phone access is already on")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return listenError(addr, err)
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    maxHeaderBytes,
		// The Android client is minSdk 29, which speaks TLS 1.3.
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{s.cert}, MinVersion: tls.VersionTLS13},
	}
	s.ln, s.srv = ln, srv
	// The cap sits under TLS so a half-finished handshake holds a slot too.
	capped := newLimitListener(ln, maxConns)
	go func() { _ = srv.Serve(tls.NewListener(capped, srv.TLSConfig)) }()
	return nil
}

// Addr is the address the server listens on, or "" while stopped.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Stop closes the listener, gives requests already running up to two seconds
// to finish, then closes every connection that is left. The wait matters
// because the caller closes the supervisor right after: a handler still
// inside it would touch a closed fleet. It is safe to call when stopped.
func (s *Server) Stop() {
	s.mu.Lock()
	srv := s.srv
	s.ln, s.srv = nil, nil
	s.mu.Unlock()
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), stopGrace)
	defer cancel()
	_ = srv.Shutdown(ctx)
	_ = srv.Close()
}

// limitListener lets at most n connections be open at once. Accept waits for
// a free slot instead of refusing, so a flood queues in the kernel backlog
// rather than costing a goroutine and a TLS handshake each.
type limitListener struct {
	net.Listener
	sem chan struct{}
}

func newLimitListener(ln net.Listener, n int) net.Listener {
	return &limitListener{Listener: ln, sem: make(chan struct{}, n)}
}

func (l *limitListener) Accept() (net.Conn, error) {
	l.sem <- struct{}{}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &limitConn{Conn: c, release: func() { <-l.sem }}, nil
}

type limitConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

// listenError turns a bind failure into a sentence for the settings page.
func listenError(addr string, err error) error {
	_, port, _ := net.SplitHostPort(addr)
	if errors.Is(err, syscall.EADDRINUSE) {
		return fmt.Errorf("port %s is already in use. Pick another port", port)
	}
	return fmt.Errorf("couldn't listen on port %s: %v", port, err)
}
