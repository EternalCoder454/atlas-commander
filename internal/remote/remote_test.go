package remote

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"atlas-commander/internal/agent"
	"atlas-commander/internal/fleet"
)

// fake is a Controller that records what the server asked of it.
type fake struct {
	mu      sync.Mutex
	snap    *fleet.Snapshot
	decided []string
	started []string
	err     error
}

func (f *fake) Snapshot() *fleet.Snapshot { return f.snap }
func (f *fake) Transcript(id string, from int) ([]fleet.Entry, int) {
	all := []fleet.Entry{
		{At: time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC), Kind: agent.EventText, Text: "hi", User: true},
		{Kind: agent.EventToolUse, Tool: "Bash", Text: "ls"},
		{Kind: agent.EventResult, Text: "done"},
	}
	if from >= len(all) {
		return nil, len(all)
	}
	return all[from:], len(all)
}
func (f *fake) Start(id, prompt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, id+":"+prompt)
	return f.err
}
func (f *fake) Send(string, string) error { return f.err }
func (f *fake) Hold(string) error         { return f.err }
func (f *fake) Resume(string) error       { return f.err }
func (f *fake) Stop(string) error         { return f.err }
func (f *fake) Kill(string) error         { return f.err }
func (f *fake) KillAll() int              { return 2 }
func (f *fake) Decide(id string, allow bool, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.decided = append(f.decided, id+":"+map[bool]string{true: "allow", false: "deny"}[allow]+":"+reason)
	return f.err
}

func newFake() *fake {
	return &fake{snap: &fleet.Snapshot{
		Seq: 812, Spent: 1.84,
		Fleets: []fleet.FleetView{{ID: "f1", Name: "Website", BudgetUSD: 20, SpentUSD: 3.1, Agents: 3, Live: 1}},
		Agents: []fleet.AgentView{
			{ID: "a1", Name: "Builder", FleetID: "f1", FleetName: "Website", Backend: "claude-code", Model: "sonnet",
				Status: fleet.StatusRunning, Task: "Fix it", LastTool: "Bash: go test", SessionCost: 0.42, CostUSD: 2.9,
				CapUSD: 5, Pending: 1, StartedAt: time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC)},
			{ID: "old", Name: "Gone", Archived: true},
		},
		Approvals: []fleet.ApprovalView{{ID: "p9", AgentID: "a1", AgentName: "Builder", Tool: "Bash", Summary: "rm -rf build",
			Input: "{}", At: time.Date(2026, 10, 1, 7, 3, 10, 0, time.UTC)}},
	}}
}

func newServer(t *testing.T) (*Server, *fake) {
	t.Helper()
	f := newFake()
	s, err := New(Options{Controller: f, Dir: t.TempDir(), Name: "zach-pc", Version: "0.1.0", Demo: true})
	if err != nil {
		t.Fatal(err)
	}
	return s, f
}

func do(t *testing.T, s *Server, method, path, token, body, remote string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if remote != "" {
		r.RemoteAddr = remote
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// A request with no token must never reach a handler: the phone's token is
// the only thing keeping other devices on the LAN out.
func TestRequestWithoutTokenIsRejected(t *testing.T) {
	s, _ := newServer(t)
	w := do(t, s, "GET", "/api/v1/state", "", "", "")
	if w.Code != 401 {
		t.Fatalf("got status %d, want 401", w.Code)
	}
	var e map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &e)
	if e["error"] != unpairedMessage {
		t.Errorf("got message %q, want %q", e["error"], unpairedMessage)
	}
}

// A wrong token is the case the phone sees after "Forget paired phones".
func TestWrongTokenGives401AndRightTokenWorks(t *testing.T) {
	s, _ := newServer(t)
	if w := do(t, s, "GET", "/api/v1/ping", "nope", "", ""); w.Code != 401 {
		t.Errorf("wrong token: got status %d, want 401", w.Code)
	}
	if w := do(t, s, "GET", "/api/v1/ping", s.Token(), "", ""); w.Code != 200 {
		t.Errorf("right token: got status %d, want 200", w.Code)
	}
	old := s.Token()
	if err := s.ResetToken(); err != nil {
		t.Fatal(err)
	}
	if w := do(t, s, "GET", "/api/v1/ping", old, "", ""); w.Code != 401 {
		t.Errorf("old token after reset: got status %d, want 401", w.Code)
	}
}

// Without the limit the token could be guessed at network speed.
func TestTooManyWrongTokensGive429(t *testing.T) {
	s, _ := newServer(t)
	for i := range maxFailures {
		if w := do(t, s, "GET", "/api/v1/ping", "bad", "", "10.0.0.9:1000"); w.Code != 401 {
			t.Fatalf("try %d: got status %d, want 401", i, w.Code)
		}
	}
	if w := do(t, s, "GET", "/api/v1/ping", "bad", "", "10.0.0.9:1001"); w.Code != 429 {
		t.Errorf("after %d failures: got status %d, want 429", maxFailures, w.Code)
	}
	if w := do(t, s, "GET", "/api/v1/ping", s.Token(), "", "10.0.0.9:1002"); w.Code != 429 {
		t.Errorf("right token while blocked: got status %d, want 429", w.Code)
	}
	if w := do(t, s, "GET", "/api/v1/ping", s.Token(), "", "10.0.0.8:1000"); w.Code != 200 {
		t.Errorf("another address: got status %d, want 200", w.Code)
	}
	// A minute later the block is over.
	s.limit.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if w := do(t, s, "GET", "/api/v1/ping", s.Token(), "", "10.0.0.9:1003"); w.Code != 200 {
		t.Errorf("after the block: got status %d, want 200", w.Code)
	}
}

// The Android app parses this JSON by the names in docs/phone-api.md.
func TestStateJSONShape(t *testing.T) {
	s, _ := newServer(t)
	w := do(t, s, "GET", "/api/v1/state", s.Token(), "", "")
	if w.Code != 200 {
		t.Fatalf("got status %d, want 200", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["seq"] != float64(812) || got["name"] != "zach-pc" || got["demo"] != true || got["spent_usd"] != 1.84 {
		t.Errorf("top level wrong: %v", got)
	}
	agents := got["agents"].([]any)
	if len(agents) != 1 {
		t.Fatalf("got %d agents, want 1 (archived left out)", len(agents))
	}
	a := agents[0].(map[string]any)
	for k, want := range map[string]any{
		"id": "a1", "provider": "claude-code", "status": "running", "fleet_name": "Website",
		"last_tool": "Bash: go test", "session_cost_usd": 0.42, "cost_usd": 2.9, "cap_usd": float64(5),
		"pending": float64(1), "started_at": "2026-10-01T07:00:00Z", "error": "",
	} {
		if a[k] != want {
			t.Errorf("agent %s: got %v, want %v", k, a[k], want)
		}
	}
	p := got["approvals"].([]any)[0].(map[string]any)
	if p["id"] != "p9" || p["agent_name"] != "Builder" || p["at"] != "2026-10-01T07:03:10Z" || p["summary"] != "rm -rf build" {
		t.Errorf("approval wrong: %v", p)
	}
	f := got["fleets"].([]any)[0].(map[string]any)
	if f["budget_usd"] != float64(20) || f["live"] != float64(1) {
		t.Errorf("fleet wrong: %v", f)
	}
}

// Transcript kinds are the API's five, and an unset time must be null, not
// "0001-01-01".
func TestTranscriptKindsAndNext(t *testing.T) {
	s, _ := newServer(t)
	w := do(t, s, "GET", "/api/v1/agents/a1/transcript?from=0", s.Token(), "", "")
	var got struct {
		Entries []map[string]any `json:"entries"`
		Next    int              `json:"next"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range got.Entries {
		kinds = append(kinds, e["kind"].(string))
	}
	if strings.Join(kinds, ",") != "text,tool,result" || got.Next != 3 {
		t.Errorf("got kinds %v next %d, want text,tool,result next 3", kinds, got.Next)
	}
	if got.Entries[0]["user"] != true || got.Entries[1]["at"] != nil {
		t.Errorf("user flag or null time wrong: %v", got.Entries)
	}
	if w := do(t, s, "GET", "/api/v1/agents/zzz/transcript", s.Token(), "", ""); w.Code != 404 {
		t.Errorf("unknown agent: got status %d, want 404", w.Code)
	}
}

// Every event kind has a name; init and exit are "info".
func TestKindNameCoversAllKinds(t *testing.T) {
	want := map[agent.EventKind]string{
		agent.EventInit: "info", agent.EventText: "text", agent.EventToolUse: "tool",
		agent.EventToolResult: "result", agent.EventUsage: "info", agent.EventResult: "result",
		agent.EventError: "error", agent.EventExit: "info",
	}
	for k, w := range want {
		if got := kindName(k); got != w {
			t.Errorf("kind %v: got %q, want %q", k, got, w)
		}
	}
}

// The whole point of the phone: an approval answered there reaches the
// supervisor with the same arguments.
func TestApprovalDecisionReachesController(t *testing.T) {
	s, f := newServer(t)
	w := do(t, s, "POST", "/api/v1/approvals/p9", s.Token(), `{"allow":false,"reason":"too risky"}`, "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"ok":true}` {
		t.Fatalf("got %d %s, want 200 {\"ok\":true}", w.Code, w.Body.String())
	}
	if len(f.decided) != 1 || f.decided[0] != "p9:deny:too risky" {
		t.Errorf("got decisions %v, want [p9:deny:too risky]", f.decided)
	}
	if w := do(t, s, "POST", "/api/v1/approvals/nope", s.Token(), `{"allow":true}`, ""); w.Code != 404 {
		t.Errorf("unknown approval: got status %d, want 404", w.Code)
	}
	if w := do(t, s, "POST", "/api/v1/approvals/p9", s.Token(), `not json`, ""); w.Code != 400 {
		t.Errorf("bad body: got status %d, want 400", w.Code)
	}
}

// Errors from the controller are shown to the user as they are, as 409.
func TestActionErrorsAreConflicts(t *testing.T) {
	s, f := newServer(t)
	f.err = errors.New("Builder is busy.")
	w := do(t, s, "POST", "/api/v1/agents/a1/start", s.Token(), `{"prompt":"go"}`, "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "Builder is busy.") {
		t.Errorf("got %d %s, want 409 with the sentence", w.Code, w.Body.String())
	}
	f.err = nil
	if w := do(t, s, "POST", "/api/v1/agents/a1/hold", s.Token(), "", ""); w.Code != 200 {
		t.Errorf("hold: got status %d, want 200", w.Code)
	}
	w = do(t, s, "POST", "/api/v1/kill-all", s.Token(), "", "")
	if strings.TrimSpace(w.Body.String()) != `{"killed":2,"ok":true}` {
		t.Errorf("kill-all: got %s", w.Body.String())
	}
}

// The phone trusts only this fingerprint, so it must be the one the server
// really presents, and it must survive a restart.
func TestPinFingerprintMatchesServedCertificate(t *testing.T) {
	dir := t.TempDir()
	f := newFake()
	s, err := New(Options{Controller: f, Dir: dir, Name: "pc"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	var seen string
	conn, err := tls.Dial("tcp", s.Addr(), &tls.Config{
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			sum := sha256.Sum256(raw[0])
			seen = hex.EncodeToString(sum[:])
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if seen != s.Fingerprint() {
		t.Errorf("served certificate has fingerprint %s, want %s", seen, s.Fingerprint())
	}
	again, err := New(Options{Controller: f, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if again.Fingerprint() != s.Fingerprint() || again.Token() != s.Token() {
		t.Error("certificate or token changed after a restart; every phone would be unpaired")
	}
}

// A real request over TLS with a client that pins, as the phone does.
func TestPingOverTLS(t *testing.T) {
	s, _ := newServer(t)
	if err := s.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	r, _ := http.NewRequest("GET", "https://"+s.Addr()+"/api/v1/ping", nil)
	r.Header.Set("Authorization", "Bearer "+s.Token())
	resp, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var p pingJSON
	_ = json.NewDecoder(resp.Body).Decode(&p)
	if resp.StatusCode != 200 || p.Name != "zach-pc" || !p.Demo || p.Version != "0.1.0" {
		t.Errorf("got %d %+v, want 200 and the PC's name", resp.StatusCode, p)
	}
}

// The phone and the QR code both read this link, so its shape is a contract.
func TestPairingLinkFormat(t *testing.T) {
	link := PairingLink("zach's pc", []string{"192.168.1.5:47821", "100.64.0.2:47821"}, "tok-en_1", "ab12")
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "atlascommander" || u.Host != "pair" {
		t.Errorf("got %s://%s, want atlascommander://pair", u.Scheme, u.Host)
	}
	q := u.Query()
	if q.Get("v") != "1" || q.Get("n") != "zach's pc" || q.Get("t") != "tok-en_1" || q.Get("f") != "ab12" {
		t.Errorf("query wrong: %v", q)
	}
	if q.Get("h") != "192.168.1.5:47821,100.64.0.2:47821" {
		t.Errorf("got h=%q, want both hosts in order", q.Get("h"))
	}
	if strings.Contains(link, " ") || strings.Contains(link, "'") {
		t.Errorf("link %q has an unencoded character", link)
	}
}

// LAN addresses come first so the phone's first try is the likely one, and
// loopback and link-local addresses never leak into the QR code.
func TestHostsOrderAndFilter(t *testing.T) {
	ips := []net.IP{
		net.ParseIP("127.0.0.1"), net.ParseIP("100.101.102.103"), net.ParseIP("fe80::1"),
		net.ParseIP("169.254.3.4"), net.ParseIP("192.168.1.5"), net.ParseIP("::1"),
		net.ParseIP("fd7a::5"), net.ParseIP("10.0.0.7"), net.ParseIP("8.8.4.4"),
	}
	got := strings.Join(hostsFrom(ips, 47821), " ")
	want := "192.168.1.5:47821 10.0.0.7:47821 100.101.102.103:47821 8.8.4.4:47821 [fd7a::5]:47821"
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// Turning access on and off in Settings must really open and close the port.
func TestHostEnableDisable(t *testing.T) {
	h := NewHost(Options{Controller: newFake(), Dir: t.TempDir(), Name: "pc"})
	if st := h.Status(); st.On || st.Link != "" {
		t.Fatalf("new host should be off, got %+v", st)
	}
	h.Enable(0)
	st := h.Status()
	if !st.On || st.Err != "" || !strings.HasPrefix(st.Link, "atlascommander://pair?") {
		t.Fatalf("got %+v, want on with a link", st)
	}
	h.Disable()
	if st := h.Status(); st.On || st.Link != "" {
		t.Errorf("got %+v, want off", st)
	}
	// A port that is taken gives a sentence, not a crash.
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	h.Enable(ln.Addr().(*net.TCPAddr).Port)
	if st := h.Status(); st.Err == "" || !strings.Contains(st.Err, "in use") {
		t.Errorf("got %+v, want an in-use error", st)
	}
}
