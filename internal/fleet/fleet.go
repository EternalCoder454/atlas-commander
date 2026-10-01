// Package fleet is the supervisor: it joins the agent backends, the approval
// gate, the store and the cost caps, and publishes what the UI shows as
// immutable snapshots.
//
// Goroutine model: one mutex (Supervisor.mu) guards all state. Each running
// session has one goroutine draining its events, plus one that delivers
// queued Send texts in order. A builder goroutine rebuilds the Snapshot at
// most every 250 ms when state changed and every second regardless (that tick
// also samples burn), and a flusher goroutine writes buffered audit rows in
// batches. Controller methods run on the Qt thread and never wait on a
// session: Stop, Kill and Send run on goroutines of their own. Gate calls
// come from the gate server's connection goroutines and may block for as long
// as a human takes to answer; they never hold the mutex while waiting.
//
// Platform split: none. Process handling lives in the backends and
// internal/procgroup; this package only passes them the environment.
package fleet

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"atlas-commander/internal/agent"
	"atlas-commander/internal/gate"
	"atlas-commander/internal/pricing"
	"atlas-commander/internal/store"
)

// Options is everything the supervisor is built from.
type Options struct {
	Store        *store.Store
	Prices       pricing.Table
	Backends     map[string]agent.Backend
	WorktreeRoot string
	// Instance is the value for procgroup.InstanceEnv in every agent.
	Instance string
	// Setup reports what is installed; nil means nothing is known.
	Setup func() SetupInfo
}

const (
	maxTranscript   = 5000 // entries kept per agent
	rebuildEvery    = 250 * time.Millisecond
	flushEvery      = 500 * time.Millisecond
	flushAt         = 200  // rows
	flushMax        = 1000 // rows in one transaction, so a backlog doesn't hold the writer
	maxAuditBacklog = 50000
	stopWait        = 5 * time.Second
	maxDetailText   = 4 << 10
)

// agentState is the supervisor's live record of one agent. Only touched with
// Supervisor.mu held.
type agentState struct {
	cfg store.Agent // always the full current row, status included

	base   Status // starting, running, waiting, idle, stopped, error or capped
	status Status // base adjusted for approvals and hold; what is shown
	errMsg string

	task, taskID string
	lastTool     string
	lastToolAt   time.Time
	latency      time.Duration
	startedAt    time.Time
	sessionID    string
	model        string // concrete model reported by the session, for pricing

	session, turn, total agent.Usage
	sessionCost, cost    float64 // cost is all-time
	turnEst              float64 // live estimate for the turn in progress

	sess        agent.Session
	token       string
	gen         int
	outbox      chan string
	held        bool
	heldCh      chan struct{} // closed when the hold ends
	userStopped bool
	capped      bool
	starting    bool // a start is in flight (sess is still nil)
	cancelStart bool // Stop, Kill or KillAll asked while starting
	approve     []string
	pending     int

	burn      ring
	burnTotal int64 // tokens counted for burn (no cache reads)
	burnLast  int64

	tr      []Entry
	trOff   int // index of tr[0] in the agent's whole history
	loaded  bool
	loading bool // a history query is in flight
}

type pendingApproval struct {
	id, agentID, tool, input, summary string
	at                                time.Time
	ch                                chan gate.Decision // buffered 1; exactly one send
}

type taskRow struct {
	t     store.Task
	deps  []string
	ready bool
}

// Supervisor implements ui.Controller.
type Supervisor struct {
	o  Options
	st *store.Store

	mu         sync.Mutex
	fleets     []*store.Fleet
	agents     map[string]*agentState
	list       []*agentState // creation order
	pending    map[string]*pendingApproval
	srv        *gate.Server
	notifier   func(Notice)
	closing    bool
	dirty      bool
	seq        uint64
	spent      float64 // since this run started
	costFlow   float64 // cost added since the last burn tick
	burn       ring
	costRate   ring
	taskRows   []taskRow
	tasksDirty bool

	snap atomic.Pointer[Snapshot]

	auditMu     sync.Mutex
	auditBuf    []store.AuditEntry
	auditClosed bool // the final flush ran; later rows are written at once
	flushMu     sync.Mutex
	kick        chan struct{}

	// ctx is cancelled by Close so a start waiting on its backend gives up.
	ctx    context.Context
	cancel context.CancelFunc

	// wg tracks session, start and housekeeping goroutines. Add is only
	// called with mu held and closing false (spawnLocked), so none can begin
	// after close() has started waiting.
	wg        sync.WaitGroup
	bg        sync.WaitGroup // builder and flusher
	quit      chan struct{}
	closeOnce sync.Once
}

const crashReason = "Commander closed while this agent was running"

// New loads fleets and agents from the store, repairs what a crash left
// behind, and starts the builder and flusher.
func New(o Options) (*Supervisor, error) {
	if o.Store == nil {
		return nil, errors.New("The fleet needs a database.")
	}
	s := &Supervisor{
		o: o, st: o.Store,
		agents: map[string]*agentState{}, pending: map[string]*pendingApproval{},
		tasksDirty: true, dirty: true,
		kick: make(chan struct{}, 1), quit: make(chan struct{}),
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	fleets, err := s.st.ListFleets()
	if err != nil {
		return nil, err
	}
	if len(fleets) == 0 {
		home, _ := os.UserHomeDir()
		f := store.Fleet{Name: "Default", Workdir: home}
		if err := s.st.CreateFleet(&f); err != nil {
			return nil, err
		}
		fleets = []store.Fleet{f}
	}
	stats, err := s.st.AgentStats(store.Filter{})
	if err != nil {
		return nil, err
	}
	byAgent := map[string]store.AgentStats{}
	for _, as := range stats {
		byAgent[as.AgentID] = as
	}
	for i := range fleets {
		f := fleets[i]
		s.fleets = append(s.fleets, &f)
		ags, err := s.st.ListAgents(f.ID, true)
		if err != nil {
			return nil, err
		}
		for _, ca := range ags {
			a := newAgentState(ca)
			as := byAgent[ca.ID]
			a.total, a.cost = as.Usage, as.CostUSD
			if Status(ca.Status).Live() {
				a.base, a.status = StatusStopped, StatusStopped
				a.cfg.Status = string(StatusStopped)
				a.errMsg = crashReason
				if err := s.st.SetAgentStatus(ca.ID, string(StatusStopped)); err != nil {
					return nil, err
				}
			}
			s.agents[a.cfg.ID] = a
			s.list = append(s.list, a)
		}
	}
	// The hooks that asked are gone, so nobody can act on these any more.
	pend, err := s.st.PendingApprovals()
	if err != nil {
		return nil, err
	}
	for _, p := range pend {
		if err := s.st.DecideApproval(p.ID, store.DecisionDeny, "Commander restarted"); err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	// A task still marked running lost its agent in the crash.
	for _, f := range s.fleets {
		run, err := s.st.ListTasks(f.ID, store.TaskRunning)
		if err != nil {
			return nil, err
		}
		for _, t := range run {
			_ = s.st.SetTaskStatus(t.ID, store.TaskFailed, crashReason)
		}
	}
	s.rebuild(false)
	s.bg.Add(2)
	go s.builder()
	go s.flusher()
	// Rebuild the transcripts in the background so opening an agent's detail
	// view doesn't query the audit log on the Qt thread.
	s.mu.Lock()
	all := append([]*agentState(nil), s.list...)
	s.spawnLocked(func() {
		for _, a := range all {
			select {
			case <-s.ctx.Done():
				return
			default:
			}
			s.loadHistory(a)
		}
	})
	s.mu.Unlock()
	return s, nil
}

// spawnLocked runs fn on a goroutine tracked by wg. It refuses (false) once
// Close has begun. Call with mu held.
func (s *Supervisor) spawnLocked(fn func()) bool {
	if s.closing {
		return false
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn()
	}()
	return true
}

// spawn is spawnLocked for callers without mu.
func (s *Supervisor) spawn(fn func()) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spawnLocked(fn)
}

func newAgentState(ca store.Agent) *agentState {
	a := &agentState{cfg: ca, base: Status(ca.Status), sessionID: ca.SessionID}
	switch a.base {
	case StatusIdle, StatusStopped, StatusError, StatusCapped:
	default:
		a.base = StatusIdle
	}
	a.status = a.base
	a.approve = parseApprove(ca.GatePolicy)
	a.loaded = false
	return a
}

// Attach gives the supervisor the gate server, once Listen has succeeded.
func (s *Supervisor) Attach(srv *gate.Server) {
	s.mu.Lock()
	s.srv = srv
	s.mu.Unlock()
}

// Snapshot returns the latest immutable snapshot.
func (s *Supervisor) Snapshot() *Snapshot { return s.snap.Load() }

// SetNotifier sets the function called for approvals, errors and caps. It is
// called from supervisor goroutines, never with the mutex held.
func (s *Supervisor) SetNotifier(f func(Notice)) {
	s.mu.Lock()
	s.notifier = f
	s.mu.Unlock()
}

func (s *Supervisor) emit(notes []Notice) {
	if len(notes) == 0 {
		return
	}
	s.mu.Lock()
	f := s.notifier
	s.mu.Unlock()
	if f == nil {
		return
	}
	for _, n := range notes {
		f(n)
	}
}

// Setup reports what is installed.
func (s *Supervisor) Setup() SetupInfo {
	if s.o.Setup != nil {
		return s.o.Setup()
	}
	return SetupInfo{}
}

// Close stops every session (politely for 5 s, then by force), flushes the
// audit buffer and stops the goroutines. Safe to call twice.
func (s *Supervisor) Close() {
	s.closeOnce.Do(s.close)
}

func (s *Supervisor) close() {
	var sessions []agent.Session
	s.mu.Lock()
	s.closing = true
	s.cancel()
	for _, a := range s.list {
		if a.sess != nil {
			a.userStopped = true
			s.releaseHeld(a)
			sessions = append(sessions, a.sess)
		}
	}
	var open []string
	for id := range s.pending {
		open = append(open, id)
	}
	s.mu.Unlock()
	for _, id := range open {
		_ = s.decide(id, false, "Commander closed", true)
	}

	var stopWG sync.WaitGroup
	for _, sess := range sessions {
		stopWG.Add(1)
		go func(sess agent.Session) {
			defer stopWG.Done()
			done := make(chan struct{})
			go func() { _ = sess.Stop(); close(done) }()
			select {
			case <-done:
			case <-time.After(stopWait):
				_ = sess.Kill()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
				}
			}
		}(sess)
	}
	stopWG.Wait()

	drained := make(chan struct{})
	go func() { s.wg.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(10 * time.Second):
	}
	close(s.quit)
	s.bg.Wait()
	s.flushFinal()
	s.mu.Lock()
	s.dirty = true
	s.mu.Unlock()
	s.rebuild(false)
}

// ---- builder and flusher ----

func (s *Supervisor) builder() {
	defer s.bg.Done()
	t := time.NewTicker(rebuildEvery)
	defer t.Stop()
	lastSample := time.Now()
	for {
		select {
		case <-s.quit:
			return
		case now := <-t.C:
			sample := now.Sub(lastSample) >= time.Second-rebuildEvery/2
			if sample {
				lastSample = now
			}
			s.rebuild(sample)
		}
	}
}

func (s *Supervisor) flusher() {
	defer s.bg.Done()
	t := time.NewTicker(flushEvery)
	defer t.Stop()
	for {
		select {
		case <-s.quit:
			return
		case <-t.C:
		case <-s.kick:
		}
		s.flushAudit()
	}
}

// touch marks the snapshot stale. Call with mu held.
func (s *Supervisor) touch() { s.dirty = true }

func (s *Supervisor) touchTasks() {
	s.dirty = true
	s.tasksDirty = true
}

// sampleLocked pushes one second's burn and cost rate.
func (s *Supervisor) sampleLocked() {
	var total float64
	for _, a := range s.list {
		d := float64(a.burnTotal - a.burnLast)
		a.burnLast = a.burnTotal
		a.burn.push(d)
		total += d
	}
	s.burn.push(total)
	s.costRate.push(s.costFlow * 60)
	s.costFlow = 0
}

func (s *Supervisor) loadTasks(fleetIDs []string) ([]taskRow, bool) {
	var rows []taskRow
	for _, fid := range fleetIDs {
		ts, err := s.st.ListTasks(fid, "")
		if err != nil {
			return nil, false
		}
		ready := map[string]bool{}
		if rt, err := s.st.ReadyTasks(fid); err == nil {
			for _, t := range rt {
				ready[t.ID] = true
			}
		}
		for _, t := range ts {
			if t.Status == store.TaskCancelled {
				continue
			}
			deps, _ := s.st.TaskDeps(t.ID)
			rows = append(rows, taskRow{t: t, deps: deps, ready: ready[t.ID]})
		}
	}
	return rows, true
}

// rebuild publishes a new snapshot if state changed (or sample is set).
func (s *Supervisor) rebuild(sample bool) {
	s.mu.Lock()
	needTasks := s.tasksDirty
	s.tasksDirty = false
	ids := make([]string, len(s.fleets))
	for i, f := range s.fleets {
		ids[i] = f.ID
	}
	s.mu.Unlock()
	var rows []taskRow
	if needTasks {
		var ok bool
		if rows, ok = s.loadTasks(ids); !ok {
			needTasks = false
			s.mu.Lock()
			s.tasksDirty = true
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if needTasks {
		s.taskRows = rows
		s.dirty = true
	}
	if sample {
		s.sampleLocked()
	}
	if !s.dirty && !sample && s.snap.Load() != nil {
		return
	}
	s.dirty = false
	s.snap.Store(s.buildLocked())
}

func (s *Supervisor) fleetByID(id string) *store.Fleet {
	for _, f := range s.fleets {
		if f.ID == id {
			return f
		}
	}
	return nil
}

func (s *Supervisor) effectiveDir(a *agentState) string {
	if a.cfg.Workdir != "" {
		return a.cfg.Workdir
	}
	if f := s.fleetByID(a.cfg.FleetID); f != nil {
		return f.Workdir
	}
	return ""
}

func (s *Supervisor) buildLocked() *Snapshot {
	s.seq++
	sn := &Snapshot{
		Seq:      s.seq,
		Burn:     s.burn.slice(),
		CostRate: s.costRate.slice(),
		Spent:    s.spent,
	}
	names := map[string]string{}
	for _, a := range s.list {
		names[a.cfg.ID] = a.cfg.Name
	}
	for _, f := range s.fleets {
		fv := FleetView{ID: f.ID, Name: f.Name, WorkDir: f.Workdir, BudgetUSD: f.BudgetUSD}
		for _, a := range s.list {
			if a.cfg.FleetID != f.ID {
				continue
			}
			fv.SpentUSD += a.cost + a.turnEst
			if !a.cfg.Archived {
				fv.Agents++
				if a.status.Live() {
					fv.Live++
				}
			}
			sn.Agents = append(sn.Agents, AgentView{
				ID: a.cfg.ID, Name: a.cfg.Name, FleetID: f.ID, FleetName: f.Name,
				Backend: a.cfg.Backend, Model: a.cfg.Model, WorkDir: s.effectiveDir(a),
				Worktree: a.cfg.WorktreePath, Branch: a.cfg.Branch,
				PinnedPrompt: a.cfg.PinnedPrompt, UseWorktree: a.cfg.UseWorktree,
				Status: a.status, Task: a.task, TaskID: a.taskID,
				LastTool: a.lastTool, LastToolAt: a.lastToolAt, Latency: a.latency,
				Session: a.session, Total: a.total,
				SessionCost: a.sessionCost + a.turnEst, CostUSD: a.cost + a.turnEst,
				CapUSD: a.cfg.CostCapUSD, Error: a.errMsg, StartedAt: a.startedAt,
				SessionID: a.sessionID, Burn: a.burn.slice(), Pending: a.pending,
				Archived: a.cfg.Archived,
			})
		}
		sn.Fleets = append(sn.Fleets, fv)
	}
	for _, p := range s.pendingSorted() {
		sn.Approvals = append(sn.Approvals, ApprovalView{
			ID: p.id, AgentID: p.agentID, AgentName: names[p.agentID],
			Tool: p.tool, Input: p.input, Summary: p.summary, At: p.at,
		})
	}
	for _, r := range s.taskRows {
		sn.Tasks = append(sn.Tasks, TaskView{
			ID: r.t.ID, FleetID: r.t.FleetID, Title: r.t.Title, Prompt: r.t.Prompt,
			Priority: r.t.Priority, Status: r.t.Status, Ready: r.ready && r.t.Status == store.TaskQueued,
			AgentID: r.t.AgentID, AgentName: names[r.t.AgentID],
			DependsOn: append([]string(nil), r.deps...), Created: r.t.CreatedAt,
		})
	}
	return sn
}

func (s *Supervisor) pendingSorted() []*pendingApproval {
	out := make([]*pendingApproval, 0, len(s.pending))
	for _, p := range s.pending {
		out = append(out, p)
	}
	// Insertion sort: a handful of entries, oldest first.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && (out[j].at.Before(out[j-1].at) || (out[j].at.Equal(out[j-1].at) && out[j].id < out[j-1].id)); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ---- audit buffer ----

func (s *Supervisor) pushAudit(e store.AuditEntry) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	s.auditMu.Lock()
	if s.auditClosed {
		// A goroutine that outlived Close's wait: the flusher is gone, so
		// write the row now rather than lose it.
		s.auditMu.Unlock()
		_ = s.st.AppendBatch([]store.AuditEntry{e})
		return
	}
	s.auditBuf = append(s.auditBuf, e)
	n := len(s.auditBuf)
	s.auditMu.Unlock()
	if n >= flushAt {
		select {
		case s.kick <- struct{}{}:
		default:
		}
	}
}

// flushAudit writes the buffered rows, at most flushMax per transaction. On
// failure the rows go back in front so the next flush retries them, up to a
// bound so a broken disk can't eat memory.
func (s *Supervisor) flushAudit() { s.flush(false) }

// flushFinal is the flush at Close. After it, pushAudit writes directly.
func (s *Supervisor) flushFinal() { s.flush(true) }

func (s *Supervisor) flush(final bool) {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	for {
		s.auditMu.Lock()
		n := len(s.auditBuf)
		if n > flushMax {
			n = flushMax
		}
		rows := s.auditBuf[:n:n]
		s.auditBuf = s.auditBuf[n:]
		if final && len(s.auditBuf) == 0 {
			// Set in the same critical section as the last take, so a row
			// pushed after this point sees it and is written directly.
			s.auditClosed = true
		}
		s.auditMu.Unlock()
		if len(rows) == 0 {
			return
		}
		if err := s.st.AppendBatch(rows); err != nil {
			s.auditMu.Lock()
			s.auditBuf = append(rows, s.auditBuf...)
			if over := len(s.auditBuf) - maxAuditBacklog; over > 0 {
				s.auditBuf = s.auditBuf[over:]
			}
			s.auditMu.Unlock()
			return
		}
	}
}

// row starts an audit row for agent a. Call with mu held.
func (s *Supervisor) row(a *agentState, kind, tool string, det map[string]any) store.AuditEntry {
	return store.AuditEntry{
		At: time.Now(), FleetID: a.cfg.FleetID, AgentID: a.cfg.ID, SessionID: a.sessionID,
		TaskID: a.taskID, Kind: kind, Tool: tool, Detail: detail(det),
	}
}

// userAudit records something the user did. Call with mu held.
func (s *Supervisor) userAudit(a *agentState, kind string, det map[string]any) {
	s.pushAudit(s.row(a, kind, "", det))
}
