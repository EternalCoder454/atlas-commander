package remote

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"atlas-commander/internal/fleet"
)

// pinnedClient talks to s over TLS without verifying the certificate, which
// is fine for a test against our own listener.
func pinnedClient(*Server) *http.Client {
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives: true,
	}}
}

func newReq(method, url, token string) (*http.Request, error) {
	r, err := http.NewRequest(method, url, nil)
	if err == nil {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r, err
}

// A slow or silent client must not hold a connection forever, and a header
// flood must not eat memory; these limits are the only defence once the
// token check has let a connection in.
func TestServerSetsTimeoutsAndLimits(t *testing.T) {
	s, _ := newServer(t)
	if err := s.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	s.mu.Lock()
	srv := s.srv
	s.mu.Unlock()
	if srv.WriteTimeout != 30*time.Second || srv.IdleTimeout != time.Minute || srv.MaxHeaderBytes != 16<<10 {
		t.Errorf("got write %v idle %v header %d, want 30s 1m0s 16384", srv.WriteTimeout, srv.IdleTimeout, srv.MaxHeaderBytes)
	}
	if srv.TLSConfig.MinVersion != tls.VersionTLS13 {
		t.Errorf("got min TLS version %#x, want TLS 1.3", srv.TLSConfig.MinVersion)
	}
}

// Without a cap, a flood of connections costs a goroutine and a handshake
// each. The third connection must wait until one of the first two closes.
func TestLimitListenerCapsOpenConnections(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newLimitListener(raw, 2)
	defer ln.Close()
	for range 3 {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
	}
	var accepted []net.Conn
	for range 2 {
		c, err := ln.Accept()
		if err != nil {
			t.Fatal(err)
		}
		accepted = append(accepted, c)
	}
	got := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		got <- c
	}()
	select {
	case <-got:
		t.Fatal("got a third connection while two were open, want it to wait")
	case <-time.After(150 * time.Millisecond):
	}
	accepted[0].Close()
	accepted[0].Close() // closing twice must not free two slots
	select {
	case c := <-got:
		c.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("third connection still waiting after one closed, want it accepted")
	}
}

// One host owns a whole IPv6 /64, so counting per address would let it try a
// new address for every guess.
func TestLimiterKeysIPv6ByPrefix(t *testing.T) {
	s, _ := newServer(t)
	for i := range maxFailures {
		addr := "[2001:db8:1:2::" + strconv.Itoa(i+1) + "]:1000"
		if w := do(t, s, "GET", "/api/v1/ping", "bad", "", addr); w.Code != 401 {
			t.Fatalf("try %d: got status %d, want 401", i, w.Code)
		}
	}
	if w := do(t, s, "GET", "/api/v1/ping", "bad", "", "[2001:db8:1:2:ffff::1]:1000"); w.Code != 429 {
		t.Errorf("same /64: got status %d, want 429", w.Code)
	}
	if w := do(t, s, "GET", "/api/v1/ping", s.Token(), "", "[2001:db8:1:3::1]:1000"); w.Code != 200 {
		t.Errorf("another /64: got status %d, want 200", w.Code)
	}
	if got, want := limitKey("::ffff:10.0.0.9"), "10.0.0.9"; got != want {
		t.Errorf("v4-mapped key: got %s, want %s", got, want)
	}
}

// The table must not grow without bound, yet filling it from many addresses
// must not switch the limit off for the next one, and a phone with the right
// token must still get in.
func TestLimiterTableIsCappedAndTokenStillWorks(t *testing.T) {
	s, _ := newServer(t)
	for i := range maxTracked + 50 {
		s.limit.fail(net.IPv4(10, byte(i>>16), byte(i>>8), byte(i)).String())
	}
	if n := len(s.limit.by); n != maxTracked {
		t.Errorf("got %d tracked addresses, want %d", n, maxTracked)
	}
	for range maxFailures {
		do(t, s, "GET", "/api/v1/ping", "bad", "", "192.168.9.9:1")
	}
	if w := do(t, s, "GET", "/api/v1/ping", "bad", "", "192.168.9.9:1"); w.Code != 429 {
		t.Errorf("new address with a full table: got status %d, want 429", w.Code)
	}
	if n := len(s.limit.by); n > maxTracked {
		t.Errorf("got %d tracked addresses, want at most %d", n, maxTracked)
	}
	if w := do(t, s, "GET", "/api/v1/ping", s.Token(), "", "192.168.9.10:1"); w.Code != 200 {
		t.Errorf("right token with a full table: got status %d, want 200", w.Code)
	}
}

// Walking the table on every request is a cost an attacker controls; pruning
// happens at most once a second, and then really removes stale entries.
func TestLimiterPrunesAtMostOncePerSecond(t *testing.T) {
	now := time.Now()
	l := limiter{now: func() time.Time { return now }}
	l.fail("10.0.0.1")
	now = now.Add(2 * time.Minute)
	l.blockedNow("10.0.0.2") // prunes: the entry is stale
	if len(l.by) != 0 {
		t.Fatalf("got %d entries after a prune, want 0", len(l.by))
	}
	l.fail("10.0.0.3")
	now = now.Add(2*time.Minute - 500*time.Millisecond)
	l.lastPrune = now.Add(-100 * time.Millisecond)
	l.blockedNow("10.0.0.2")
	if len(l.by) != 1 {
		t.Errorf("got %d entries, want 1 (not pruned again inside a second)", len(l.by))
	}
}

// slow is a controller whose Snapshot holds a request open, to stand in for a
// handler that is mid-flight when the server stops.
type slow struct {
	*fake
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *slow) Snapshot() *fleet.Snapshot {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return s.fake.Snapshot()
}

func startSlow(t *testing.T) (*Server, *slow) {
	t.Helper()
	c := &slow{fake: newFake(), entered: make(chan struct{}), release: make(chan struct{})}
	s, err := New(Options{Controller: c, Dir: t.TempDir(), Name: "pc"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	return s, c
}

// The caller closes the supervisor right after Stop, so a request still
// running must be allowed to finish first.
func TestStopWaitsForRequestsInFlight(t *testing.T) {
	s, c := startSlow(t)
	type result struct {
		code int
		err  error
	}
	res := make(chan result, 1)
	go func() {
		req, _ := newReq("GET", "https://"+s.Addr()+"/api/v1/state", s.Token())
		resp, err := pinnedClient(s).Do(req)
		if err != nil {
			res <- result{err: err}
			return
		}
		resp.Body.Close()
		res <- result{code: resp.StatusCode}
	}()
	<-c.entered
	stopped := make(chan struct{})
	go func() { s.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("Stop returned while a request was running, want it to wait")
	case <-time.After(200 * time.Millisecond):
	}
	close(c.release)
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop didn't return after the request finished")
	}
	if r := <-res; r.err != nil || r.code != 200 {
		t.Errorf("got %v and status %d, want the in-flight request to finish with 200", r.err, r.code)
	}
}

// Stop must not hang on a request that never finishes: after the grace
// period the connection is closed.
func TestStopGivesUpOnStuckRequests(t *testing.T) {
	s, c := startSlow(t)
	defer close(c.release)
	go func() {
		req, _ := newReq("GET", "https://"+s.Addr()+"/api/v1/state", s.Token())
		if resp, err := pinnedClient(s).Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	<-c.entered
	start := time.Now()
	s.Stop()
	if d := time.Since(start); d < stopGrace-200*time.Millisecond || d > stopGrace+2*time.Second {
		t.Errorf("Stop took %v, want about %v", d, stopGrace)
	}
}

// Files left loose by an older build or a restored backup would let another
// local user read the key and token.
func TestNewTightensPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes mean little on Windows")
	}
	dir := filepath.Join(t.TempDir(), "phone")
	if _, err := New(Options{Controller: newFake(), Dir: dir, Name: "pc"}); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Chmod(dir, 0o755))
	must(os.Chmod(filepath.Join(dir, "key.pem"), 0o644))
	must(os.Chmod(filepath.Join(dir, "token"), 0o644))
	if _, err := New(Options{Controller: newFake(), Dir: dir, Name: "pc"}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]os.FileMode{".": 0o700, "key.pem": 0o600, "token": 0o600} {
		fi, err := os.Stat(filepath.Join(dir, name))
		must(err)
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s: got mode %o, want %o", name, got, want)
		}
	}
}

// A new certificate means a new fingerprint, so every phone is unpaired; the
// user has to be told, but a first run or a normal restart says nothing.
func TestCertificateRenewalIsReportedInStatus(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Options{Controller: newFake(), Dir: dir, Name: "pc"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Notice() != "" {
		t.Errorf("first run: got notice %q, want none", s.Notice())
	}
	if s, _ = New(Options{Controller: newFake(), Dir: dir, Name: "pc"}); s.Notice() != "" {
		t.Errorf("restart: got notice %q, want none", s.Notice())
	}
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHost(Options{Controller: newFake(), Dir: dir, Name: "pc"})
	h.Enable(0)
	defer h.Disable()
	want := "The certificate was renewed, so phones must pair again."
	if st := h.Status(); st.Note != want {
		t.Errorf("got note %q, want %q", st.Note, want)
	}
}

// The status shown on screen must never carry the pairing link: it is only
// handed out by PairingLink, while the user has chosen to show it.
func TestHostGivesPairingLinkOnlyOnRequest(t *testing.T) {
	h := NewHost(Options{Controller: newFake(), Dir: t.TempDir(), Name: "pc"})
	if h.PairingLink() != "" {
		t.Error("got a link while off, want none")
	}
	h.Enable(0)
	defer h.Disable()
	if l := h.PairingLink(); !strings.HasPrefix(l, "atlascommander://pair?") {
		t.Errorf("got link %q, want a pairing link", l)
	}
}

// The audit log is the only place the desk user can see that a decision came
// from the phone.
func TestDecisionReasonIsMarkedFromPhone(t *testing.T) {
	s, f := newServer(t)
	for _, body := range []string{`{"allow":true}`, `{"allow":false,"reason":"too risky"}`} {
		if w := do(t, s, "POST", "/api/v1/approvals/p9", s.Token(), body, ""); w.Code != 200 {
			t.Fatalf("%s: got status %d, want 200", body, w.Code)
		}
	}
	want := []string{"p9:allow:From phone", "p9:deny:From phone: too risky"}
	if strings.Join(f.decided, "|") != strings.Join(want, "|") {
		t.Errorf("got %q, want %q", f.decided, want)
	}
}

// Text goes straight to an agent or the audit log, so its size is bounded.
func TestOverlongTextGives400(t *testing.T) {
	s, f := newServer(t)
	long := strings.Repeat("a", maxText+1)
	for path, body := range map[string]string{
		"/api/v1/agents/a1/start": `{"prompt":"` + long + `"}`,
		"/api/v1/agents/a1/send":  `{"text":"` + long + `"}`,
		"/api/v1/approvals/p9":    `{"allow":true,"reason":"` + long + `"}`,
	} {
		if w := do(t, s, "POST", path, s.Token(), body, ""); w.Code != 400 {
			t.Errorf("%s: got status %d, want 400", path, w.Code)
		}
	}
	if len(f.started)+len(f.decided) != 0 {
		t.Errorf("got %d calls, want none", len(f.started)+len(f.decided))
	}
	ok := strings.Repeat("a", maxText)
	if w := do(t, s, "POST", "/api/v1/agents/a1/start", s.Token(), `{"prompt":"`+ok+`"}`, ""); w.Code != 200 {
		t.Errorf("exactly %d bytes: got status %d, want 200", maxText, w.Code)
	}
}

// A body over the cap is cut off with 413, and a typo in a field name is a
// 400 rather than silently ignored.
func TestBodyLimitAndUnknownFields(t *testing.T) {
	s, _ := newServer(t)
	big := `{"prompt":"` + strings.Repeat("a", maxBody) + `"}`
	if w := do(t, s, "POST", "/api/v1/agents/a1/start", s.Token(), big, ""); w.Code != 413 {
		t.Errorf("oversized body: got status %d, want 413", w.Code)
	}
	if w := do(t, s, "POST", "/api/v1/agents/a1/start", s.Token(), `{"prompt":"x","extra":1}`, ""); w.Code != 400 {
		t.Errorf("unknown field: got status %d, want 400", w.Code)
	}
	if w := do(t, s, "POST", "/api/v1/agents/a1/start", s.Token(), `not json`, ""); w.Code != 400 {
		t.Errorf("bad JSON: got status %d, want 400", w.Code)
	}
}

// With every slot taken, Accept waits; Close must still wake it, or Shutdown
// (which waits for Serve, which waits for Accept) hangs until a phone hangs up.
func TestLimitListenerCloseWakesAFullAccept(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := newLimitListener(ln, 1)
	go func() {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err == nil {
			defer c.Close()
			time.Sleep(time.Second)
		}
	}()
	first, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	got := make(chan error, 1)
	go func() { _, err := l.Accept(); got <- err }()
	time.Sleep(50 * time.Millisecond)
	l.Close()
	select {
	case err := <-got:
		if !errors.Is(err, net.ErrClosed) {
			t.Errorf("got %v, want net.ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("got Accept still waiting after Close, want it to return")
	}
}
