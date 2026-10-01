package remote

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"atlas-commander/internal/agent"
	"atlas-commander/internal/fleet"
)

const (
	maxBody       = 1 << 20 // a prompt is text; a megabyte is far more than needed
	maxFailures   = 10
	failureWindow = time.Minute
	blockFor      = time.Minute

	unpairedMessage = "This phone isn't paired any more. Pair it again from Commander's Settings."
)

// limiter counts failed tokens per address. After maxFailures inside
// failureWindow the address is blocked for blockFor, even if it then sends the
// right token: a guesser must not learn which guess was right.
type limiter struct {
	now func() time.Time
	mu  sync.Mutex
	by  map[string]*attempts
}

type attempts struct {
	n       int
	first   time.Time
	blocked time.Time // zero, or when the block ends
}

// blockedNow reports whether ip is blocked, and prunes stale entries.
func (l *limiter) blockedNow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for k, a := range l.by {
		if now.After(a.blocked) && now.Sub(a.first) > failureWindow {
			delete(l.by, k)
		}
	}
	a := l.by[ip]
	return a != nil && now.Before(a.blocked)
}

func (l *limiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.by == nil {
		l.by = map[string]*attempts{}
	}
	now := l.now()
	a := l.by[ip]
	if a == nil || now.Sub(a.first) > failureWindow {
		a = &attempts{first: now}
		l.by[ip] = a
	}
	a.n++
	if a.n >= maxFailures {
		a.blocked = now.Add(blockFor)
		a.n = 0
		a.first = now
	}
}

// Handler is the whole API, behind the token check. It is exported so tests
// can drive it without a socket.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/ping", s.ping)
	mux.HandleFunc("GET /api/v1/state", s.state)
	mux.HandleFunc("GET /api/v1/agents/{id}/transcript", s.transcript)
	mux.HandleFunc("POST /api/v1/agents/{id}/start", s.start)
	mux.HandleFunc("POST /api/v1/agents/{id}/send", s.send)
	for name, act := range map[string]func(string) error{
		"hold": s.opt.Controller.Hold, "resume": s.opt.Controller.Resume,
		"stop": s.opt.Controller.Stop, "kill": s.opt.Controller.Kill,
	} {
		mux.HandleFunc("POST /api/v1/agents/{id}/"+name, s.simple(act))
	}
	mux.HandleFunc("POST /api/v1/approvals/{id}", s.decide)
	mux.HandleFunc("POST /api/v1/kill-all", s.killAll)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "Commander doesn't know that request.")
	})
	return s.auth(mux)
}

// auth checks the bearer token in constant time and rate-limits failures.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		if s.limit.blockedNow(ip) {
			w.Header().Set("Retry-After", strconv.Itoa(int(blockFor.Seconds())))
			writeError(w, http.StatusTooManyRequests, "Too many wrong tokens from this address. Try again in a minute.")
			return
		}
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(s.Token())) != 1 {
			s.limit.fail(ip)
			writeError(w, http.StatusUnauthorized, unpairedMessage)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeOK(w http.ResponseWriter) { writeJSON(w, http.StatusOK, map[string]bool{"ok": true}) }

// decodeBody reads a JSON body into v; false means it already answered 400.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "Commander couldn't read that request.")
		return false
	}
	return true
}

// actionError answers a controller error: it is "not possible in this state",
// with the controller's own sentence.
func actionError(w http.ResponseWriter, err error) {
	msg := err.Error()
	if msg == "" {
		msg = "That isn't possible right now."
	}
	writeError(w, http.StatusConflict, msg)
}

type pingJSON struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Demo    bool   `json:"demo"`
}

func (s *Server) ping(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, pingJSON{s.opt.Name, s.opt.Version, s.opt.Demo})
}

type stateJSON struct {
	Seq       uint64         `json:"seq"`
	Name      string         `json:"name"`
	Version   string         `json:"version"`
	Demo      bool           `json:"demo"`
	SpentUSD  float64        `json:"spent_usd"`
	Fleets    []fleetJSON    `json:"fleets"`
	Agents    []agentJSON    `json:"agents"`
	Approvals []approvalJSON `json:"approvals"`
}

type fleetJSON struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	BudgetUSD float64 `json:"budget_usd"`
	SpentUSD  float64 `json:"spent_usd"`
	Agents    int     `json:"agents"`
	Live      int     `json:"live"`
}

type agentJSON struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	FleetID        string  `json:"fleet_id"`
	FleetName      string  `json:"fleet_name"`
	Provider       string  `json:"provider"`
	Model          string  `json:"model"`
	Status         string  `json:"status"`
	Task           string  `json:"task"`
	LastTool       string  `json:"last_tool"`
	SessionCostUSD float64 `json:"session_cost_usd"`
	CostUSD        float64 `json:"cost_usd"`
	CapUSD         float64 `json:"cap_usd"`
	Error          string  `json:"error"`
	Pending        int     `json:"pending"`
	StartedAt      *string `json:"started_at"`
}

type approvalJSON struct {
	ID        string `json:"id"`
	AgentID   string `json:"agent_id"`
	AgentName string `json:"agent_name"`
	Tool      string `json:"tool"`
	Summary   string `json:"summary"`
	Input     string `json:"input"`
	At        string `json:"at"`
}

// stamp is t as RFC 3339 UTC, or nil for the zero time.
func stamp(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func buildState(snap *fleet.Snapshot, o Options) stateJSON {
	st := stateJSON{
		Name: o.Name, Version: o.Version, Demo: o.Demo,
		Fleets: []fleetJSON{}, Agents: []agentJSON{}, Approvals: []approvalJSON{},
	}
	if snap == nil {
		return st
	}
	st.Seq, st.SpentUSD = snap.Seq, snap.Spent
	for _, f := range snap.Fleets {
		st.Fleets = append(st.Fleets, fleetJSON{f.ID, f.Name, f.BudgetUSD, f.SpentUSD, f.Agents, f.Live})
	}
	for _, a := range snap.Agents {
		if a.Archived {
			continue
		}
		st.Agents = append(st.Agents, agentJSON{
			ID: a.ID, Name: a.Name, FleetID: a.FleetID, FleetName: a.FleetName,
			Provider: a.Backend, Model: a.Model, Status: string(a.Status),
			Task: a.Task, LastTool: a.LastTool,
			SessionCostUSD: a.SessionCost, CostUSD: a.CostUSD, CapUSD: a.CapUSD,
			Error: a.Error, Pending: a.Pending, StartedAt: stamp(a.StartedAt),
		})
	}
	for _, p := range snap.Approvals {
		at := stamp(p.At)
		if at == nil {
			at = new(string)
		}
		st.Approvals = append(st.Approvals, approvalJSON{p.ID, p.AgentID, p.AgentName, p.Tool, p.Summary, p.Input, *at})
	}
	return st
}

func (s *Server) state(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, buildState(s.opt.Controller.Snapshot(), s.opt))
}

type entryJSON struct {
	At      *string `json:"at"`
	Kind    string  `json:"kind"`
	Text    string  `json:"text"`
	Tool    string  `json:"tool"`
	IsError bool    `json:"is_error"`
	User    bool    `json:"user"`
}

// kindName maps the supervisor's event kinds to the API's five.
func kindName(k agent.EventKind) string {
	switch k {
	case agent.EventText:
		return "text"
	case agent.EventToolUse:
		return "tool"
	case agent.EventToolResult, agent.EventResult:
		return "result"
	case agent.EventError:
		return "error"
	}
	return "info" // init, exit and anything added later
}

// knownAgent reports whether the snapshot has this agent.
func (s *Server) knownAgent(id string) bool {
	snap := s.opt.Controller.Snapshot()
	if snap == nil {
		return false
	}
	for _, a := range snap.Agents {
		if a.ID == id && !a.Archived {
			return true
		}
	}
	return false
}

func (s *Server) unknownAgent(w http.ResponseWriter, id string) bool {
	if s.knownAgent(id) {
		return false
	}
	writeError(w, http.StatusNotFound, "That agent doesn't exist any more.")
	return true
}

func (s *Server) transcript(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.unknownAgent(w, id) {
		return
	}
	from := 0
	if v := r.URL.Query().Get("from"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "Commander couldn't read that request.")
			return
		}
		from = max(n, 0)
	}
	entries, next := s.opt.Controller.Transcript(id, from)
	out := make([]entryJSON, 0, len(entries))
	for _, e := range entries {
		out = append(out, entryJSON{stamp(e.At), kindName(e.Kind), e.Text, e.Tool, e.IsError, e.User})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": out, "next": next})
}

func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Prompt string `json:"prompt"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	if s.unknownAgent(w, id) {
		return
	}
	if err := s.opt.Controller.Start(id, body.Prompt); err != nil {
		actionError(w, err)
		return
	}
	writeOK(w)
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		writeError(w, http.StatusBadRequest, "Write something to send.")
		return
	}
	id := r.PathValue("id")
	if s.unknownAgent(w, id) {
		return
	}
	if err := s.opt.Controller.Send(id, body.Text); err != nil {
		actionError(w, err)
		return
	}
	writeOK(w)
}

// simple is a handler for the actions that take an agent and no body.
func (s *Server) simple(act func(string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if s.unknownAgent(w, id) {
			return
		}
		if err := act(id); err != nil {
			actionError(w, err)
			return
		}
		writeOK(w)
	}
}

func (s *Server) decide(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Allow  bool   `json:"allow"`
		Reason string `json:"reason"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	found := false
	if snap := s.opt.Controller.Snapshot(); snap != nil {
		for _, p := range snap.Approvals {
			found = found || p.ID == id
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "That approval was already answered.")
		return
	}
	if err := s.opt.Controller.Decide(id, body.Allow, body.Reason); err != nil {
		actionError(w, err)
		return
	}
	writeOK(w)
}

func (s *Server) killAll(w http.ResponseWriter, _ *http.Request) {
	n := s.opt.Controller.KillAll()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "killed": n})
}
