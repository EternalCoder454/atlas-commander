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
	cert, fp, err := loadOrCreateCert(o.Dir)
	if err != nil {
		return nil, fmt.Errorf("couldn't prepare the phone certificate: %w", err)
	}
	tok, err := loadOrCreateToken(o.Dir)
	if err != nil {
		return nil, fmt.Errorf("couldn't prepare the phone token: %w", err)
	}
	return &Server{opt: o, cert: cert, fp: fp, token: tok, limit: limiter{now: time.Now}}, nil
}

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
		IdleTimeout:       2 * time.Minute,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{s.cert}, MinVersion: tls.VersionTLS12},
	}
	s.ln, s.srv = ln, srv
	go func() { _ = srv.Serve(tls.NewListener(ln, srv.TLSConfig)) }()
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

// Stop closes the listener and every open connection. It is safe to call
// when stopped.
func (s *Server) Stop() {
	s.mu.Lock()
	srv := s.srv
	s.ln, s.srv = nil, nil
	s.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
}

// listenError turns a bind failure into a sentence for the settings page.
func listenError(addr string, err error) error {
	_, port, _ := net.SplitHostPort(addr)
	if errors.Is(err, syscall.EADDRINUSE) {
		return fmt.Errorf("port %s is already in use. Pick another port", port)
	}
	return fmt.Errorf("couldn't listen on port %s: %v", port, err)
}
