package gate

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A short path: unix socket paths are limited to ~108 bytes.
func sockDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "gt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return filepath.Join(d, "g")
}

func listen(t *testing.T, d Decider) *Server {
	t.Helper()
	s, err := Listen(sockDir(t), d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// The core promise: the decision comes back, and the agent id is the one
// registered for the token, not the one the client claims.
func TestAskReturnsDecisionAndServerSetsAgentID(t *testing.T) {
	var got Request
	s := listen(t, func(ctx context.Context, r Request) Decision {
		got = r
		return Decision{Allow: r.Tool == "Read", Reason: "because"}
	})
	tok := s.Register("agent-1")
	in := json.RawMessage(`{"file_path":"/x"}`)

	d, err := Ask(context.Background(), s.Addr(), tok, Request{AgentID: "liar", Tool: "Read", Input: in})
	if err != nil || !d.Allow || d.Reason != "because" {
		t.Fatalf("allow: got %+v, %v; want allow", d, err)
	}
	if got.AgentID != "agent-1" || string(got.Input) != string(in) {
		t.Errorf("request: got %+v, want agent-1 and input kept", got)
	}
	d, err = Ask(context.Background(), s.Addr(), tok, Request{Tool: "Bash"})
	if err != nil || d.Allow {
		t.Errorf("deny: got %+v, %v; want deny", d, err)
	}
}

// A token nobody issued must never reach the Decider.
func TestAskUnknownTokenDeniedWithoutDecider(t *testing.T) {
	called := false
	s := listen(t, func(context.Context, Request) Decision { called = true; return Decision{Allow: true} })
	d, err := Ask(context.Background(), s.Addr(), "nope", Request{Tool: "Bash"})
	if err != nil || d.Allow || d.Reason != "unknown session" || called {
		t.Errorf("got %+v, %v, called=%v; want deny 'unknown session', not called", d, err, called)
	}
	tok := s.Register("a")
	s.Unregister(tok)
	if d, _ := Ask(context.Background(), s.Addr(), tok, Request{}); d.Allow {
		t.Errorf("unregistered token allowed, want denied")
	}
}

// A killed agent drops the hook; the pending approval must be cancelled so
// it doesn't sit in the UI forever.
func TestDeciderCancelledWhenClientGoesAway(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	s := listen(t, func(ctx context.Context, r Request) Decision {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return Decision{}
	})
	tok := s.Register("a")
	ctx, cancel := context.WithCancel(context.Background())
	go Ask(ctx, s.Addr(), tok, Request{Tool: "Bash"})
	<-started
	cancel()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("decider context not cancelled, want cancelled after client left")
	}
}

// After a crash the socket file stays behind; the next start must not fail.
func TestListenReplacesStaleSocket(t *testing.T) {
	dir := sockDir(t)
	s1, err := Listen(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Close the fd without unlinking, like a crash.
	s1.ln.(*net.UnixListener).SetUnlinkOnClose(false)
	s1.Close()
	if _, err := os.Lstat(s1.Addr()); err != nil {
		t.Fatalf("stale socket not left behind: %v", err)
	}
	s2, err := Listen(dir, nil)
	if err != nil {
		t.Fatalf("got %v, want stale socket replaced", err)
	}
	s2.Close()
}

// Listen doubles as the single-instance check.
func TestListenTwiceReturnsErrRunning(t *testing.T) {
	dir := sockDir(t)
	s := listen2(t, dir)
	defer s.Close()
	if _, err := Listen(dir, nil); !errors.Is(err, ErrRunning) {
		t.Errorf("got %v, want ErrRunning", err)
	}
}

func listen2(t *testing.T, dir string) *Server {
	s, err := Listen(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A second launch relies on this to raise the first window.
func TestActivateCallsHook(t *testing.T) {
	s := listen(t, nil)
	ch := make(chan struct{}, 1)
	s.SetOnActivate(func() { ch <- struct{}{} })
	if !Activate(s.Addr()) {
		t.Fatal("Activate returned false, want true")
	}
	select {
	case <-ch:
	default:
		t.Error("hook not called, want called before Activate returns")
	}
	if Activate(filepath.Join(t.TempDir(), "none")) {
		t.Error("Activate with no server returned true, want false")
	}
}

// A hostile or buggy client must not make the server buffer unbounded data.
func TestOversizedRequestRejected(t *testing.T) {
	called := false
	s := listen(t, func(context.Context, Request) Decision { called = true; return Decision{Allow: true} })
	tok := s.Register("a")
	big := json.RawMessage(`"` + strings.Repeat("a", maxLine+10) + `"`)
	d, err := Ask(context.Background(), s.Addr(), tok, Request{Tool: "Bash", Input: big})
	if called || (err == nil && d.Allow) {
		t.Errorf("got %+v, %v, called=%v; want rejected", d, err, called)
	}
}

// A leftover regular file at the socket path is somebody's data; Listen must
// fail rather than delete it.
func TestListenKeepsNonSocketFile(t *testing.T) {
	dir := sockDir(t)
	os.MkdirAll(dir, 0o700)
	p := filepath.Join(dir, "gate.sock")
	if err := os.WriteFile(p, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := Listen(dir, nil); err == nil {
		s.Close()
		t.Fatal("Listen: got nil, want error")
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != "precious" {
		t.Errorf("file: got %q, %v; want it untouched", b, err)
	}
}

// Two launches racing must not both win; the lock is what decides, even when
// the socket is not yet answering.
func TestLockFileBlocksSecondListen(t *testing.T) {
	dir := sockDir(t)
	os.MkdirAll(dir, 0o700)
	f, err := lockFile(filepath.Join(dir, "commander.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(dir, nil); !errors.Is(err, ErrRunning) {
		t.Errorf("got %v, want ErrRunning while lock is held", err)
	}
	unlockFile(f)
	s, err := Listen(dir, nil)
	if err != nil {
		t.Fatalf("after unlock: got %v, want success", err)
	}
	s.Close()
	// Close must release the lock for the next launch.
	s2, err := Listen(dir, nil)
	if err != nil {
		t.Fatalf("after Close: got %v, want success", err)
	}
	s2.Close()
}

// A bug in the Decider must block that one call, not hang the hook or crash.
func TestDeciderPanicYieldsDeny(t *testing.T) {
	s := listen(t, func(context.Context, Request) Decision { panic("boom") })
	tok := s.Register("a")
	d, err := Ask(context.Background(), s.Addr(), tok, Request{Tool: "Bash"})
	if err != nil || d.Allow || !strings.Contains(d.Reason, "error deciding") {
		t.Errorf("got %+v, %v; want deny with reason", d, err)
	}
}

// Shutdown must cancel a waiting Decider and return, not hang on it.
func TestCloseCancelsPendingDecider(t *testing.T) {
	started := make(chan struct{})
	s, err := Listen(sockDir(t), func(ctx context.Context, r Request) Decision {
		close(started)
		<-ctx.Done()
		return Decision{}
	})
	if err != nil {
		t.Fatal(err)
	}
	tok := s.Register("a")
	go Ask(context.Background(), s.Addr(), tok, Request{Tool: "Bash"})
	<-started
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not return, want it to cancel the decider")
	}
}
