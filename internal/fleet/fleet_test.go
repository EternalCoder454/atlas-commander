package fleet

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"atlas-commander/internal/agent"
	"atlas-commander/internal/gate"
	"atlas-commander/internal/pricing"
	"atlas-commander/internal/store"
)

const testModel = "claude-sonnet-5-5"

// fakeSession is a scripted session: the test pushes events through emit and
// ends it with finish, the way a real process would.
type fakeSession struct {
	spec agent.Spec
	ev   chan agent.Event
	once sync.Once

	mu    sync.Mutex
	sent  []string
	stops int
	kills int
}

func (f *fakeSession) emit(e agent.Event) { f.ev <- e }

func (f *fakeSession) finish(isErr bool, text string) {
	f.once.Do(func() {
		f.ev <- agent.Event{Kind: agent.EventExit, IsError: isErr, Text: text}
		close(f.ev)
	})
}

func (f *fakeSession) Send(text string) error {
	f.mu.Lock()
	f.sent = append(f.sent, text)
	f.mu.Unlock()
	return nil
}

func (f *fakeSession) Stop() error {
	f.mu.Lock()
	f.stops++
	f.mu.Unlock()
	f.finish(false, "stopped")
	return nil
}

func (f *fakeSession) Kill() error {
	f.mu.Lock()
	f.kills++
	f.mu.Unlock()
	f.finish(false, "killed")
	return nil
}

func (f *fakeSession) Events() <-chan agent.Event { return f.ev }

func (f *fakeSession) sentTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

type fakeBackend struct {
	mu       sync.Mutex
	sessions []*fakeSession
	// hold, when set, makes Start wait until it is closed or ctx ends.
	hold    chan struct{}
	entered chan struct{} // receives once per Start that has begun, if set
}

func (b *fakeBackend) Name() string { return agent.BackendClaudeCode }

func (b *fakeBackend) Start(ctx context.Context, spec agent.Spec) (agent.Session, error) {
	if b.entered != nil {
		b.entered <- struct{}{}
	}
	if b.hold != nil {
		select {
		case <-b.hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s := &fakeSession{spec: spec, ev: make(chan agent.Event, 256)}
	b.mu.Lock()
	b.sessions = append(b.sessions, s)
	b.mu.Unlock()
	return s, nil
}

func (b *fakeBackend) session(t *testing.T, i int) *fakeSession {
	t.Helper()
	var got *fakeSession
	waitFor(t, "a session to start", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		if len(b.sessions) > i {
			got = b.sessions[i]
		}
		return got != nil
	})
	return got
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type env struct {
	t       *testing.T
	st      *store.Store
	sup     *Supervisor
	be      *fakeBackend
	srv     *gate.Server
	dbPath  string
	fleetID string
	agentID string
}

func openStore(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func startSup(t *testing.T, st *store.Store, be *fakeBackend) (*Supervisor, *gate.Server) {
	t.Helper()
	sup, err := New(Options{
		Store: st, Prices: pricing.Default(), Backends: map[string]agent.Backend{agent.BackendClaudeCode: be},
		WorktreeRoot: t.TempDir(), Instance: "test-instance",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := gate.Listen(filepath.Join(t.TempDir(), "g"), sup.Gate)
	if err != nil {
		sup.Close()
		t.Fatal(err)
	}
	sup.Attach(srv)
	return sup, srv
}

// newEnv builds a supervisor with one fleet ("Work") holding one agent ("alpha").
func newEnv(t *testing.T, mod func(*FleetConfig, *AgentConfig)) *env {
	t.Helper()
	e := &env{t: t, be: &fakeBackend{}, dbPath: filepath.Join(t.TempDir(), "c.db")}
	e.st = openStore(t, e.dbPath)
	t.Cleanup(func() { e.st.Close() })
	e.sup, e.srv = startSup(t, e.st, e.be)
	t.Cleanup(func() { e.srv.Close(); e.sup.Close() })
	fc := FleetConfig{Name: "Work", WorkDir: t.TempDir()}
	ac := AgentConfig{Name: "alpha", Backend: agent.BackendClaudeCode, Model: testModel}
	if mod != nil {
		mod(&fc, &ac)
	}
	var err error
	if e.fleetID, err = e.sup.CreateFleet(fc); err != nil {
		t.Fatal(err)
	}
	ac.FleetID = e.fleetID
	if e.agentID, err = e.sup.RegisterAgent(ac); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) addAgent(name string) string {
	e.t.Helper()
	id, err := e.sup.RegisterAgent(AgentConfig{FleetID: e.fleetID, Name: name, Backend: agent.BackendClaudeCode, Model: testModel})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// view returns the agent's row, waiting for the builder to publish it (the
// snapshot lags changes by up to 250 ms).
func (e *env) view(id string) AgentView {
	var v AgentView
	waitFor(e.t, "the agent in the snapshot", func() bool {
		for _, a := range e.sup.Snapshot().Agents {
			if a.ID == id {
				v = a
				return true
			}
		}
		return false
	})
	return v
}

func (e *env) waitStatus(id string, want Status) AgentView {
	e.t.Helper()
	waitFor(e.t, "status "+string(want), func() bool { return e.view(id).Status == want })
	return e.view(id)
}

func result(cost float64, isErr bool, text string) agent.Event {
	return agent.Event{Kind: agent.EventResult, CostUSD: cost, IsError: isErr, Text: text, Duration: 1500 * time.Millisecond, Turns: 1}
}

func initEv() agent.Event {
	return agent.Event{Kind: agent.EventInit, SessionID: "sess-1", Model: testModel}
}

func toolUse(tool, input string) agent.Event {
	return agent.Event{Kind: agent.EventToolUse, Tool: tool, ToolInput: json.RawMessage(input), ToolUseID: "tu1"}
}

// gateReq is a hook request as the gate server hands it over: carrying the
// token of the agent's live session, which Gate checks after a hold.
func (e *env) gateReq(agentID, tool string) gate.Request {
	e.sup.mu.Lock()
	var token string
	if a := e.sup.agents[agentID]; a != nil {
		token = a.token
	}
	e.sup.mu.Unlock()
	return gate.Request{AgentID: agentID, Tool: tool, Input: json.RawMessage(`{"command":"ls"}`), Token: token}
}

// The whole point of the snapshot: events from a session show up as status,
// usage, cost, last tool, latency and transcript lines.
func TestEventFlowUpdatesSnapshot(t *testing.T) {
	e := newEnv(t, nil)
	if err := e.sup.Start(e.agentID, "do it"); err != nil {
		t.Fatal(err)
	}
	if got := e.view(e.agentID).Status; got != StatusStarting && got != StatusRunning {
		// The builder may not have run yet; Snapshot is allowed to lag.
		t.Logf("status right after Start: %s", got)
	}
	s := e.be.session(t, 0)
	env := map[string]bool{}
	for _, kv := range s.spec.Env {
		env[kv[:indexByte(kv, '=')]] = true
	}
	for _, k := range []string{gate.EnvAddr, gate.EnvToken, gate.EnvAgentID, "ATLAS_COMMANDER_INSTANCE"} {
		if !env[k] {
			t.Errorf("session env lacks %s: %v", k, s.spec.Env)
		}
	}
	s.emit(initEv())
	waitFor(t, "running", func() bool { return e.view(e.agentID).Status == StatusRunning })
	s.emit(agent.Event{Kind: agent.EventText, Text: "hello"})
	s.emit(toolUse("Bash", `{"command":"go test ./..."}`))
	s.emit(agent.Event{Kind: agent.EventToolResult, ToolUseID: "tu1", Text: "ok"})
	s.emit(agent.Event{Kind: agent.EventUsage, Usage: agent.Usage{Input: 1000, Output: 500}})
	s.emit(result(0.0123, false, "done"))
	e.waitStatus(e.agentID, StatusWaiting)
	waitFor(t, "cost", func() bool { return e.view(e.agentID).CostUSD > 0 })
	v := e.view(e.agentID)
	if v.CostUSD != 0.0123 || v.SessionCost != 0.0123 {
		t.Errorf("cost = %v / session %v, want 0.0123", v.CostUSD, v.SessionCost)
	}
	if v.Session.Input != 1000 || v.Session.Output != 500 || v.Total.Output != 500 {
		t.Errorf("usage = %+v total %+v, want 1000 in, 500 out", v.Session, v.Total)
	}
	if v.LastTool != "Bash: go test ./..." {
		t.Errorf("LastTool = %q, want %q", v.LastTool, "Bash: go test ./...")
	}
	if v.Latency != 1500*time.Millisecond {
		t.Errorf("Latency = %v, want 1.5s", v.Latency)
	}
	if v.SessionID != "sess-1" || v.Task != "do it" {
		t.Errorf("session %q task %q, want sess-1 and %q", v.SessionID, v.Task, "do it")
	}
	entries, next := e.sup.Transcript(e.agentID, 0)
	if next != len(entries) || len(entries) != 6 {
		t.Fatalf("transcript has %d entries (next %d), want 6", len(entries), next)
	}
	if !entries[0].User || entries[0].Text != "do it" {
		t.Errorf("first entry = %+v, want the user's prompt", entries[0])
	}
	if entries[1].Text != "Session started ("+testModel+")" {
		t.Errorf("init entry = %q", entries[1].Text)
	}
	more, _ := e.sup.Transcript(e.agentID, next)
	if len(more) != 0 {
		t.Errorf("polling from the end returned %d entries, want 0", len(more))
	}
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return len(s)
}

// A cap that only the live estimate has crossed must already stop the
// agent, otherwise one long turn can blow far past it.
func TestCostCapStopsSessionAndGateDenies(t *testing.T) {
	e := newEnv(t, func(_ *FleetConfig, a *AgentConfig) { a.CostCapUSD = 0.10 })
	if err := e.sup.Start(e.agentID, "go"); err != nil {
		t.Fatal(err)
	}
	s := e.be.session(t, 0)
	s.emit(initEv())
	s.emit(agent.Event{Kind: agent.EventUsage, Usage: agent.Usage{Output: 100_000}}) // $1.00 at 10/M
	v := e.waitStatus(e.agentID, StatusCapped)
	waitFor(t, "stop", func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.stops == 1 })
	waitFor(t, "exit", func() bool { return e.view(e.agentID).Status == StatusCapped && !e.sessionLive() })
	d := e.sup.Gate(context.Background(), e.gateReq(e.agentID, "Read"))
	if d.Allow || d.Reason != "This agent's cost cap has been reached." {
		t.Errorf("gate = %+v, want denial for the cap", d)
	}
	if v.CapUSD != 0.10 {
		t.Errorf("CapUSD = %v, want 0.10", v.CapUSD)
	}
	if err := e.sup.Start(e.agentID, "again"); err == nil {
		t.Error("Start on a capped agent succeeded, want a refusal")
	}
}

func (e *env) sessionLive() bool {
	e.sup.mu.Lock()
	defer e.sup.mu.Unlock()
	return e.sup.agents[e.agentID].sess != nil
}

// The fleet budget is shared: spend by one agent stops its siblings too.
func TestFleetBudgetStopsEveryLiveAgent(t *testing.T) {
	e := newEnv(t, func(f *FleetConfig, _ *AgentConfig) { f.BudgetUSD = 0.50 })
	b := e.addAgent("beta")
	if err := e.sup.Start(e.agentID, "a"); err != nil {
		t.Fatal(err)
	}
	if err := e.sup.Start(b, "b"); err != nil {
		t.Fatal(err)
	}
	sa, sb := e.be.session(t, 0), e.be.session(t, 1)
	sa.emit(initEv())
	sb.emit(initEv())
	waitFor(t, "both running", func() bool {
		return e.view(e.agentID).Status == StatusRunning && e.view(b).Status == StatusRunning
	})
	sa.emit(agent.Event{Kind: agent.EventResult, CostUSD: 0.60})
	e.waitStatus(e.agentID, StatusCapped)
	e.waitStatus(b, StatusCapped)
	if d := e.sup.Gate(context.Background(), e.gateReq(b, "Read")); d.Allow {
		t.Errorf("gate allowed a tool for an agent in an exhausted fleet: %+v", d)
	}
	if err := e.sup.Start(b, "again"); err == nil {
		t.Error("Start in an exhausted fleet succeeded, want a refusal")
	}
}

// A price-less model can't be capped, so Start must say so instead of
// pretending the cap works.
func TestStartRefusesCapWithUnknownModelPrice(t *testing.T) {
	e := newEnv(t, func(_ *FleetConfig, a *AgentConfig) { a.Model = "mystery-model"; a.CostCapUSD = 1 })
	err := e.sup.Start(e.agentID, "go")
	want := "No price is known for mystery-model, so its cost cap can't be enforced. Add it to prices.json."
	if err == nil || err.Error() != want {
		t.Errorf("Start error = %v, want %q", err, want)
	}
}

// Hold must stop the agent at its next tool call, and only Resume (or a
// stop, or the hook going away) lets it through.
func TestHoldBlocksGateUntilResume(t *testing.T) {
	e := newEnv(t, nil)
	if err := e.sup.Start(e.agentID, "go"); err != nil {
		t.Fatal(err)
	}
	s := e.be.session(t, 0)
	s.emit(initEv())
	e.waitStatus(e.agentID, StatusRunning)
	if err := e.sup.Hold(e.agentID); err != nil {
		t.Fatal(err)
	}
	e.waitStatus(e.agentID, StatusHeld)
	got := make(chan gate.Decision, 1)
	go func() { got <- e.sup.Gate(context.Background(), e.gateReq(e.agentID, "Read")) }()
	select {
	case d := <-got:
		t.Fatalf("Gate returned %+v while held", d)
	case <-time.After(150 * time.Millisecond):
	}
	if err := e.sup.Resume(e.agentID); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-got:
		if !d.Allow {
			t.Errorf("after Resume gate = %+v, want allow (Read needs no approval)", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Gate still blocked after Resume")
	}
	e.waitStatus(e.agentID, StatusRunning)
}

// Stopping a held agent must release its blocked hook, as a denial.
func TestStopReleasesHeldGate(t *testing.T) {
	e := newEnv(t, nil)
	if err := e.sup.Start(e.agentID, "go"); err != nil {
		t.Fatal(err)
	}
	s := e.be.session(t, 0)
	s.emit(initEv())
	e.waitStatus(e.agentID, StatusRunning)
	_ = e.sup.Hold(e.agentID)
	got := make(chan gate.Decision, 1)
	go func() { got <- e.sup.Gate(context.Background(), e.gateReq(e.agentID, "Read")) }()
	time.Sleep(100 * time.Millisecond)
	if err := e.sup.Stop(e.agentID); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-got:
		if d.Allow {
			t.Errorf("gate = %+v after Stop, want deny", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Gate still blocked after Stop")
	}
	e.waitStatus(e.agentID, StatusStopped)
}

// A request held while its session ends must not run when the hold lifts:
// the process it was for is gone. On a slow machine a Stop's exit is handled
// before the waiter wakes, which is this case too.
func TestHeldGateDeniedWhenSessionEnds(t *testing.T) {
	e := newEnv(t, nil)
	if err := e.sup.Start(e.agentID, "go"); err != nil {
		t.Fatal(err)
	}
	s := e.be.session(t, 0)
	s.emit(initEv())
	e.waitStatus(e.agentID, StatusRunning)
	_ = e.sup.Hold(e.agentID)
	got := make(chan gate.Decision, 1)
	go func() { got <- e.sup.Gate(context.Background(), e.gateReq(e.agentID, "Read")) }()
	time.Sleep(100 * time.Millisecond)
	s.finish(false, "done")
	time.Sleep(100 * time.Millisecond)
	_ = e.sup.Resume(e.agentID) // in case the exit kept the hold
	select {
	case d := <-got:
		if d.Allow {
			t.Errorf("gate = %+v after the session ended, want deny", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Gate still blocked after the session ended")
	}
}

func approvalAgent(_ *FleetConfig, a *AgentConfig) { a.Approve = []string{"Bash"} }

func waitApproval(e *env) ApprovalView {
	e.t.Helper()
	var ap ApprovalView
	waitFor(e.t, "an approval", func() bool {
		if s := e.sup.Snapshot(); len(s.Approvals) > 0 {
			ap = s.Approvals[0]
			return true
		}
		return false
	})
	return ap
}

// The user's yes releases the hook, and the board shows the approval state
// while it waits.
func TestApprovalAllow(t *testing.T) {
	e := newEnv(t, approvalAgent)
	var notes []Notice
	var mu sync.Mutex
	e.sup.SetNotifier(func(n Notice) { mu.Lock(); notes = append(notes, n); mu.Unlock() })
	if err := e.sup.Start(e.agentID, "go"); err != nil {
		t.Fatal(err)
	}
	e.be.session(t, 0).emit(initEv())
	e.waitStatus(e.agentID, StatusRunning)
	got := make(chan gate.Decision, 1)
	go func() { got <- e.sup.Gate(context.Background(), e.gateReq(e.agentID, "Bash")) }()
	ap := waitApproval(e)
	if ap.Summary != "ls" || ap.Tool != "Bash" || ap.AgentName != "alpha" {
		t.Errorf("approval = %+v, want Bash/ls for alpha", ap)
	}
	if v := e.waitStatus(e.agentID, StatusApproval); v.Pending != 1 {
		t.Errorf("Pending = %d, want 1", v.Pending)
	}
	if err := e.sup.Decide(ap.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if d := <-got; !d.Allow {
		t.Errorf("gate = %+v, want allow", d)
	}
	e.waitStatus(e.agentID, StatusRunning)
	if err := e.sup.Decide(ap.ID, true, ""); err == nil {
		t.Error("deciding twice succeeded, want an error")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notes) != 1 || notes[0].Body != "alpha wants to run Bash" {
		t.Errorf("notices = %+v, want one 'alpha wants to run Bash'", notes)
	}
	a, err := e.st.GetApproval(ap.ID)
	if err != nil || a.Decision != store.DecisionAllow {
		t.Errorf("stored approval = %+v, %v; want allow", a, err)
	}
}

func TestApprovalDeny(t *testing.T) {
	e := newEnv(t, approvalAgent)
	got := make(chan gate.Decision, 1)
	go func() { got <- e.sup.Gate(context.Background(), e.gateReq(e.agentID, "Bash")) }()
	ap := waitApproval(e)
	if err := e.sup.Decide(ap.ID, false, ""); err != nil {
		t.Fatal(err)
	}
	d := <-got
	if d.Allow || d.Reason != "Denied in Atlas Commander." {
		t.Errorf("gate = %+v, want denial with the default reason", d)
	}
	// A tool outside the list needs no approval.
	if d := e.sup.Gate(context.Background(), e.gateReq(e.agentID, "Read")); !d.Allow {
		t.Errorf("Read gate = %+v, want allow", d)
	}
	if d := e.sup.Gate(context.Background(), e.gateReq("nobody", "Read")); d.Allow || d.Reason != "Unknown agent." {
		t.Errorf("unknown agent gate = %+v, want 'Unknown agent.'", d)
	}
}

// When the hook dies, its request must not stay on the board forever.
func TestGateDeniesWhenContextCancelled(t *testing.T) {
	e := newEnv(t, approvalAgent)
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan gate.Decision, 1)
	go func() { got <- e.sup.Gate(ctx, e.gateReq(e.agentID, "Bash")) }()
	ap := waitApproval(e)
	cancel()
	select {
	case d := <-got:
		if d.Allow || d.Reason != "No answer" {
			t.Errorf("gate = %+v, want deny 'No answer'", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Gate did not return after cancel")
	}
	a, _ := e.st.GetApproval(ap.ID)
	if a.Decision != store.DecisionDeny || a.Reason != "No answer" {
		t.Errorf("stored approval = %+v, want deny/No answer", a)
	}
	waitFor(t, "approval to leave the board", func() bool { return len(e.sup.Snapshot().Approvals) == 0 })
}

func addTask(t *testing.T, e *env, title string, deps ...string) string {
	t.Helper()
	id, err := e.sup.AddTask(TaskConfig{FleetID: e.fleetID, Title: title, Prompt: "prompt " + title, DependsOn: deps})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func taskStatus(t *testing.T, e *env, id string) string {
	t.Helper()
	tk, err := e.st.GetTask(id)
	if err != nil {
		t.Fatal(err)
	}
	return tk.Status
}

// A dispatched task follows its agent: the next good result completes it.
func TestDispatchRunsTaskToDone(t *testing.T) {
	e := newEnv(t, nil)
	task := addTask(t, e, "write docs")
	if err := e.sup.Dispatch(task, e.agentID); err != nil {
		t.Fatal(err)
	}
	s := e.be.session(t, 0)
	if s.spec.Prompt != "prompt write docs" {
		t.Errorf("prompt = %q, want the task prompt", s.spec.Prompt)
	}
	s.emit(initEv())
	s.emit(result(0.01, false, "all done"))
	e.waitStatus(e.agentID, StatusWaiting)
	waitFor(t, "task done", func() bool { return taskStatus(t, e, task) == store.TaskDone })
	tk, _ := e.st.GetTask(task)
	if tk.Result != "all done" || tk.AgentID != e.agentID {
		t.Errorf("task = %+v, want result 'all done' on the agent", tk)
	}
	waitFor(t, "snapshot task", func() bool {
		for _, tv := range e.sup.Snapshot().Tasks {
			if tv.ID == task {
				return tv.Status == store.TaskDone && tv.AgentName == "alpha"
			}
		}
		return false
	})
	// The agent is waiting, so the next task goes in by Send.
	task2 := addTask(t, e, "second")
	if err := e.sup.Dispatch(task2, e.agentID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "send", func() bool { return len(s.sentTexts()) == 1 })
	if got := s.sentTexts()[0]; got != "prompt second" {
		t.Errorf("sent %q, want the second task's prompt", got)
	}
	s.emit(result(0.01, true, "it broke"))
	waitFor(t, "task failed", func() bool { return taskStatus(t, e, task2) == store.TaskFailed })
}

func TestDispatchErrorResultFailsTask(t *testing.T) {
	e := newEnv(t, nil)
	task := addTask(t, e, "risky")
	if err := e.sup.Dispatch(task, e.agentID); err != nil {
		t.Fatal(err)
	}
	s := e.be.session(t, 0)
	s.emit(initEv())
	s.emit(result(0.01, true, "it broke"))
	waitFor(t, "task failed", func() bool { return taskStatus(t, e, task) == store.TaskFailed })
}

func TestExitBeforeResultFailsTask(t *testing.T) {
	e := newEnv(t, nil)
	task := addTask(t, e, "doomed")
	if err := e.sup.Dispatch(task, e.agentID); err != nil {
		t.Fatal(err)
	}
	s := e.be.session(t, 0)
	s.emit(initEv())
	s.finish(true, "exit status 1")
	e.waitStatus(e.agentID, StatusError)
	if got := taskStatus(t, e, task); got != store.TaskFailed {
		t.Errorf("task status = %s, want failed", got)
	}
	if v := e.view(e.agentID); v.Error != "exit status 1" {
		t.Errorf("Error = %q, want the exit reason", v.Error)
	}
}

// Dependencies are the whole point of the board: a task that waits on
// another must not be started early.
func TestDispatchRefusedWhileDependencyUnfinished(t *testing.T) {
	e := newEnv(t, nil)
	first := addTask(t, e, "first")
	second := addTask(t, e, "second", first)
	err := e.sup.Dispatch(second, e.agentID)
	if err == nil || err.Error() != "This task is waiting for tasks it depends on." {
		t.Errorf("Dispatch error = %v, want the dependency refusal", err)
	}
	e.be.mu.Lock()
	n := len(e.be.sessions)
	e.be.mu.Unlock()
	if n != 0 {
		t.Errorf("%d sessions started, want 0", n)
	}
	other, _ := e.sup.CreateFleet(FleetConfig{Name: "Other", WorkDir: t.TempDir()})
	oa, _ := e.sup.RegisterAgent(AgentConfig{FleetID: other, Name: "x", Backend: agent.BackendClaudeCode, Model: testModel})
	if err := e.sup.Dispatch(first, oa); err == nil {
		t.Error("dispatch to another fleet's agent succeeded, want a refusal")
	}
	waitFor(t, "ready flags", func() bool {
		ready := map[string]bool{}
		for _, tv := range e.sup.Snapshot().Tasks {
			ready[tv.Title] = tv.Ready
		}
		return len(ready) == 2 && ready["first"] && !ready["second"]
	})
}

// A crash leaves "running" in the database; the next start must not show a
// phantom live agent, and must deny what nobody can answer.
func TestCrashRecoveryStopsLiveAgentsAndDeniesApprovals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.db")
	st := openStore(t, path)
	f := store.Fleet{Name: "F", Workdir: t.TempDir()}
	if err := st.CreateFleet(&f); err != nil {
		t.Fatal(err)
	}
	a := store.Agent{FleetID: f.ID, Name: "a", Backend: agent.BackendClaudeCode, Model: testModel, Status: "running"}
	if err := st.CreateAgent(&a); err != nil {
		t.Fatal(err)
	}
	ap := store.Approval{AgentID: a.ID, Tool: "Bash"}
	if err := st.AddApproval(&ap); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st = openStore(t, path)
	defer st.Close()
	sup, err := New(Options{Store: st, Prices: pricing.Default()})
	if err != nil {
		t.Fatal(err)
	}
	defer sup.Close()
	var v AgentView
	for _, av := range sup.Snapshot().Agents {
		if av.ID == a.ID {
			v = av
		}
	}
	if v.Status != StatusStopped || v.Error != "Commander closed while this agent was running" {
		t.Errorf("agent = %s / %q, want stopped with the crash note", v.Status, v.Error)
	}
	got, _ := st.GetApproval(ap.ID)
	if got.Decision != store.DecisionDeny || got.Reason != "Commander restarted" {
		t.Errorf("approval = %+v, want deny 'Commander restarted'", got)
	}
	stored, _ := st.GetAgent(a.ID)
	if stored.Status != "stopped" {
		t.Errorf("stored status = %s, want stopped", stored.Status)
	}
	if len(sup.Snapshot().Fleets) != 1 {
		t.Errorf("got %d fleets, want 1 (no Default added when one exists)", len(sup.Snapshot().Fleets))
	}
}

func TestNewCreatesDefaultFleet(t *testing.T) {
	st := openStore(t, filepath.Join(t.TempDir(), "c.db"))
	defer st.Close()
	sup, err := New(Options{Store: st, Prices: pricing.Default()})
	if err != nil {
		t.Fatal(err)
	}
	defer sup.Close()
	fl := sup.Snapshot().Fleets
	if len(fl) != 1 || fl[0].Name != "Default" || fl[0].WorkDir == "" {
		t.Errorf("fleets = %+v, want one Default fleet in the home directory", fl)
	}
}

// Spend must sit on exactly one row per turn: the analytics sum every row,
// so a second copy would double the bill. Close must flush what is buffered.
func TestAuditFlushedOnCloseWithOneCostRowPerTurn(t *testing.T) {
	e := newEnv(t, nil)
	if err := e.sup.Start(e.agentID, "go"); err != nil {
		t.Fatal(err)
	}
	s := e.be.session(t, 0)
	s.emit(initEv())
	for i := 0; i < 2; i++ {
		s.emit(agent.Event{Kind: agent.EventUsage, Usage: agent.Usage{Input: 100, Output: 50}})
		s.emit(result(0.02, false, "ok"))
	}
	// A third turn killed mid-way: its estimate goes on the exit row.
	s.emit(agent.Event{Kind: agent.EventUsage, Usage: agent.Usage{Output: 1000}}) // $0.01
	waitFor(t, "usage", func() bool { return e.view(e.agentID).Session.Output == 1100 })
	e.sup.Close()

	rows, err := e.st.AuditEntries(store.Filter{AgentID: e.agentID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var costRows int
	var total float64
	kinds := map[string]int{}
	for _, r := range rows {
		kinds[r.Kind]++
		if r.CostUSD > 0 {
			costRows++
			total += r.CostUSD
			if r.Kind != "result" && r.Kind != "exit" {
				t.Errorf("cost on a %s row, want only result or exit rows", r.Kind)
			}
		}
		if (r.Kind == "result" || r.Kind == "exit") && !r.Usage.IsZero() {
			t.Errorf("%s row carries tokens %+v, want none", r.Kind, r.Usage)
		}
		if r.Kind == "usage" && r.CostUSD != 0 {
			t.Errorf("usage row has cost %v, want 0", r.CostUSD)
		}
	}
	if costRows != 3 {
		t.Errorf("%d rows with cost, want 3 (two results and the exit)", costRows)
	}
	if want := 0.02 + 0.02 + 0.01; total < want-1e-9 || total > want+1e-9 {
		t.Errorf("total cost = %v, want %v", total, want)
	}
	for kind, want := range map[string]int{"user_start": 1, "init": 1, "usage": 3, "result": 2, "exit": 1} {
		if kinds[kind] != want {
			t.Errorf("%d %q rows, want %d", kinds[kind], kind, want)
		}
	}
	tot, _ := e.st.FleetTotals(store.Filter{AgentID: e.agentID})
	if tot.Results != 2 {
		t.Errorf("Totals.Results = %d, want 2", tot.Results)
	}
}

// Rows past the batch size must not wait for the timer's next tick.
func TestAuditBatchSizeTriggersFlush(t *testing.T) {
	e := newEnv(t, nil)
	if err := e.sup.Start(e.agentID, "go"); err != nil {
		t.Fatal(err)
	}
	s := e.be.session(t, 0)
	s.emit(initEv())
	for i := 0; i < 250; i++ {
		s.emit(agent.Event{Kind: agent.EventText, Text: "x"})
	}
	waitFor(t, "rows in the database", func() bool {
		rows, _ := e.st.AuditEntries(store.Filter{AgentID: e.agentID, Kind: "text"}, 0)
		return len(rows) == 250
	})
}

func TestKillAll(t *testing.T) {
	e := newEnv(t, nil)
	b := e.addAgent("beta")
	idle := e.addAgent("gamma")
	for _, id := range []string{e.agentID, b} {
		if err := e.sup.Start(id, "go"); err != nil {
			t.Fatal(err)
		}
	}
	sa, sb := e.be.session(t, 0), e.be.session(t, 1)
	sa.emit(initEv())
	sb.emit(initEv())
	waitFor(t, "running", func() bool { return e.view(e.agentID).Status == StatusRunning })
	if n := e.sup.KillAll(); n != 2 {
		t.Errorf("KillAll = %d, want 2", n)
	}
	e.waitStatus(e.agentID, StatusStopped)
	e.waitStatus(b, StatusStopped)
	if got := e.view(idle).Status; got != StatusIdle {
		t.Errorf("idle agent status = %s, want idle", got)
	}
	for i, s := range []*fakeSession{sa, sb} {
		s.mu.Lock()
		if s.kills != 1 {
			t.Errorf("session %d killed %d times, want 1", i, s.kills)
		}
		s.mu.Unlock()
	}
	if n := e.sup.KillAll(); n != 0 {
		t.Errorf("second KillAll = %d, want 0", n)
	}
}

// The UI keeps a snapshot for up to a second after the supervisor moves on;
// a slice it scribbles on must not leak back into the next one.
func TestSnapshotDoesNotAliasState(t *testing.T) {
	e := newEnv(t, nil)
	task := addTask(t, e, "t")
	_ = task
	e.addAgent("beta")
	waitFor(t, "tasks", func() bool { return len(e.sup.Snapshot().Tasks) == 1 })
	s1 := e.sup.Snapshot()
	s1.Agents[0].Name = "scribble"
	s1.Agents[0].Burn[0] = 99
	s1.Fleets[0].Name = "scribble"
	s1.Burn[0] = 99
	s1.Tasks[0].Title = "scribble"
	s1.Tasks[0].DependsOn = append(s1.Tasks[0].DependsOn, "x")
	e.addAgent("gamma") // forces a rebuild
	waitFor(t, "a newer snapshot", func() bool { return e.sup.Snapshot().Seq > s1.Seq && len(e.sup.Snapshot().Agents) == 3 })
	s2 := e.sup.Snapshot()
	if s2.Agents[0].Name != "alpha" || s2.Agents[0].Burn[0] != 0 || s2.Fleets[0].Name == "scribble" || s2.Burn[0] != 0 ||
		s2.Tasks[0].Title != "t" || len(s2.Tasks[0].DependsOn) != 0 {
		t.Errorf("snapshot picked up changes made to an older one: %+v", s2.Agents[0])
	}
}

func TestSendRequiresLiveSessionAndRecordsUserText(t *testing.T) {
	e := newEnv(t, nil)
	if err := e.sup.Send(e.agentID, "hi"); err == nil || err.Error() != "This agent isn't running. Start it first." {
		t.Errorf("Send to an idle agent = %v, want the start-first error", err)
	}
	if err := e.sup.Start(e.agentID, "go"); err != nil {
		t.Fatal(err)
	}
	s := e.be.session(t, 0)
	s.emit(initEv())
	e.waitStatus(e.agentID, StatusRunning)
	for _, m := range []string{"one", "two", "three"} {
		if err := e.sup.Send(e.agentID, m); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "sends", func() bool { return len(s.sentTexts()) == 3 })
	if got := s.sentTexts(); got[0] != "one" || got[1] != "two" || got[2] != "three" {
		t.Errorf("sent %v, want in order", got)
	}
	ents, _ := e.sup.Transcript(e.agentID, 0)
	var users int
	for _, en := range ents {
		if en.User {
			users++
		}
	}
	if users != 4 {
		t.Errorf("%d user entries, want 4 (prompt and three sends)", users)
	}
}

// The pinned prompt rides in front of the first message.
func TestStartPrependsPinnedPrompt(t *testing.T) {
	e := newEnv(t, func(_ *FleetConfig, a *AgentConfig) { a.PinnedPrompt = "Be brief." })
	if err := e.sup.Start(e.agentID, "go"); err != nil {
		t.Fatal(err)
	}
	if got := e.be.session(t, 0).spec.Prompt; got != "Be brief.\n\ngo" {
		t.Errorf("prompt = %q, want pinned then prompt", got)
	}
	if err := e.sup.Start(e.agentID, "go"); err == nil {
		t.Error("second Start succeeded, want the already-running refusal")
	}
}

// After a restart the detail view must not be blank: history is rebuilt from
// the audit log.
func TestTranscriptRebuiltFromAuditAfterRestart(t *testing.T) {
	e := newEnv(t, nil)
	if err := e.sup.Start(e.agentID, "go"); err != nil {
		t.Fatal(err)
	}
	s := e.be.session(t, 0)
	s.emit(initEv())
	s.emit(toolUse("Read", `{"file_path":"/a/b.go"}`))
	s.emit(result(0.0123, false, "fine"))
	e.waitStatus(e.agentID, StatusWaiting)
	e.srv.Close()
	e.sup.Close()

	sup2, err := New(Options{Store: e.st, Prices: pricing.Default()})
	if err != nil {
		t.Fatal(err)
	}
	defer sup2.Close()
	// The history loads in the background, so the first calls may be empty.
	var ents []Entry
	var next int
	waitFor(t, "the history to load", func() bool {
		ents, next = sup2.Transcript(e.agentID, 0)
		return len(ents) >= 4
	})
	if next != len(ents) {
		t.Fatalf("rebuilt %d entries (next %d), want at least 4", len(ents), next)
	}
	var sawTool, sawResult bool
	for _, en := range ents {
		sawTool = sawTool || (en.Kind == agent.EventToolUse && en.Text == "/a/b.go")
		sawResult = sawResult || (en.Kind == agent.EventResult && en.Text == "Turn finished · $0.0123 · 1.5s")
	}
	if !sawTool || !sawResult {
		t.Errorf("rebuilt transcript lacks the tool call or result: %+v", ents)
	}
}

func TestTranscriptKeepsIndicesStableWhenTrimmed(t *testing.T) {
	e := newEnv(t, nil)
	e.sup.mu.Lock()
	a := e.sup.agents[e.agentID]
	for i := 0; i < maxTranscript+10; i++ {
		e.sup.addEntry(a, Entry{Text: "x"})
	}
	e.sup.mu.Unlock()
	ents, next := e.sup.Transcript(e.agentID, 0)
	if len(ents) != maxTranscript+10 || next != maxTranscript+10 {
		t.Errorf("got %d entries, next %d; want %d and %d", len(ents), next, maxTranscript+10, maxTranscript+10)
	}
	ents, _ = e.sup.Transcript(e.agentID, maxTranscript+5)
	if len(ents) != 5 {
		t.Errorf("polling from index %d returned %d entries, want 5", maxTranscript+5, len(ents))
	}
}

func TestRegisterAgentValidation(t *testing.T) {
	e := newEnv(t, nil)
	base := AgentConfig{FleetID: e.fleetID, Name: "z", Backend: agent.BackendClaudeCode, Model: testModel}
	cases := map[string]func(c *AgentConfig){
		"empty name":      func(c *AgentConfig) { c.Name = " " },
		"duplicate name":  func(c *AgentConfig) { c.Name = "alpha" },
		"unknown backend": func(c *AgentConfig) { c.Backend = "nope" },
		"empty model":     func(c *AgentConfig) { c.Model = "" },
		"missing dir":     func(c *AgentConfig) { c.WorkDir = "/definitely/not/here" },
		"negative cap":    func(c *AgentConfig) { c.CostCapUSD = -1 },
	}
	for name, mod := range cases {
		c := base
		mod(&c)
		if _, err := e.sup.RegisterAgent(c); err == nil {
			t.Errorf("%s: RegisterAgent succeeded, want an error", name)
		}
	}
}

func TestUpdateAndArchiveRefusedWhileLive(t *testing.T) {
	e := newEnv(t, nil)
	if err := e.sup.Start(e.agentID, "go"); err != nil {
		t.Fatal(err)
	}
	cfg, err := e.sup.AgentConfig(e.agentID)
	if err != nil {
		t.Fatal(err)
	}
	renamed := cfg
	renamed.Name = "renamed"
	if err := e.sup.UpdateAgent(e.agentID, renamed); err == nil {
		t.Error("renaming a live agent succeeded, want a refusal")
	}
	capped := cfg
	capped.CostCapUSD = 5
	capped.Approve = []string{"Bash"}
	if err := e.sup.UpdateAgent(e.agentID, capped); err != nil {
		t.Errorf("changing cap and approvals of a live agent: %v", err)
	}
	if err := e.sup.ArchiveAgent(e.agentID); err == nil {
		t.Error("archiving a live agent succeeded, want a refusal")
	}
	if err := e.sup.DeleteFleet(e.fleetID); err == nil {
		t.Error("deleting a fleet with a live agent succeeded, want a refusal")
	}
	if err := e.sup.Stop(e.agentID); err != nil {
		t.Fatal(err)
	}
	e.waitStatus(e.agentID, StatusStopped)
	if err := e.sup.ArchiveAgent(e.agentID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the archived flag in the snapshot", func() bool { return e.view(e.agentID).Archived })
	if err := e.sup.Start(e.agentID, "x"); err == nil {
		t.Error("starting an archived agent succeeded, want a refusal")
	}
}

func TestSummarize(t *testing.T) {
	cases := []struct {
		tool, in, want string
	}{
		{"Bash", `{"command":"ls -la\n  /tmp"}`, "ls -la /tmp"},
		{"Edit", `{"file_path":"/a.go","old_string":"x"}`, "/a.go"},
		{"Grep", `{"pattern":"foo.*"}`, "foo.*"},
		{"Other", `{ "a": 1 }`, `{"a":1}`},
	}
	for _, c := range cases {
		if got := summarize(c.tool, json.RawMessage(c.in)); got != c.want {
			t.Errorf("summarize(%s) = %q, want %q", c.tool, got, c.want)
		}
	}
}

// Trimming drops a chunk at once, so the copy isn't paid per event, and the
// indices pollers hold stay valid.
func TestTranscriptTrimsInChunks(t *testing.T) {
	e := newEnv(t, nil)
	total := maxTranscript + maxTranscript/4 + 1
	e.sup.mu.Lock()
	a := e.sup.agents[e.agentID]
	for i := 0; i < total; i++ {
		e.sup.addEntry(a, Entry{Text: "x"})
	}
	e.sup.mu.Unlock()
	ents, next := e.sup.Transcript(e.agentID, 0)
	want := total - maxTranscript/4
	if len(ents) != want || next != total {
		t.Errorf("got %d entries, next %d; want %d and %d", len(ents), next, want, total)
	}
}

// Start must not wait on the backend: the Qt thread calls it.
func TestStartReturnsAtOnceWithStatusStarting(t *testing.T) {
	e := newEnv(t, nil)
	e.be.hold = make(chan struct{})
	start := time.Now()
	if err := e.sup.Start(e.agentID, "go"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("Start took %v, want it to return at once", d)
	}
	e.waitStatus(e.agentID, StatusStarting)
	if err := e.sup.Start(e.agentID, "again"); err == nil {
		t.Error("second Start while starting succeeded, want a refusal")
	}
	close(e.be.hold)
	s := e.be.session(t, 0)
	s.emit(initEv())
	e.waitStatus(e.agentID, StatusRunning)
}

// A Kill that lands before the process exists must not be forgotten.
func TestKillDuringStartingEndsStopped(t *testing.T) {
	e := newEnv(t, nil)
	e.be.hold = make(chan struct{})
	e.be.entered = make(chan struct{}, 1)
	if err := e.sup.Start(e.agentID, "go"); err != nil {
		t.Fatal(err)
	}
	<-e.be.entered
	if err := e.sup.Hold(e.agentID); err != nil {
		t.Fatal(err)
	}
	if err := e.sup.Kill(e.agentID); err != nil {
		t.Fatalf("Kill while starting: %v", err)
	}
	close(e.be.hold)
	e.waitStatus(e.agentID, StatusStopped)
	s := e.be.session(t, 0)
	s.mu.Lock()
	kills := s.kills
	s.mu.Unlock()
	if kills != 1 {
		t.Errorf("session killed %d times, want 1", kills)
	}
	if e.sessionLive() {
		t.Error("agent still has a session after the cancelled start")
	}
	if err := e.sup.Start(e.agentID, "later"); err != nil {
		t.Errorf("Start after a cancelled start: %v", err)
	}
}

// A hold asked for while starting survives into the running session.
func TestHoldDuringStartingIsKept(t *testing.T) {
	e := newEnv(t, nil)
	e.be.hold = make(chan struct{})
	e.be.entered = make(chan struct{}, 1)
	if err := e.sup.Start(e.agentID, "go"); err != nil {
		t.Fatal(err)
	}
	<-e.be.entered
	if err := e.sup.Hold(e.agentID); err != nil {
		t.Fatal(err)
	}
	close(e.be.hold)
	s := e.be.session(t, 0)
	s.emit(initEv())
	e.waitStatus(e.agentID, StatusHeld)
	if err := e.sup.Resume(e.agentID); err != nil {
		t.Fatal(err)
	}
	e.waitStatus(e.agentID, StatusRunning)
}

// Close during a start must return promptly and leave no session behind.
func TestCloseWithStartInFlight(t *testing.T) {
	e := newEnv(t, nil)
	e.be.hold = make(chan struct{})
	e.be.entered = make(chan struct{}, 1)
	if err := e.sup.Start(e.agentID, "go"); err != nil {
		t.Fatal(err)
	}
	<-e.be.entered
	done := make(chan struct{})
	go func() { e.sup.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return with a start in flight")
	}
	if err := e.sup.Start(e.agentID, "late"); err == nil {
		t.Error("Start after Close succeeded, want a refusal")
	}
	if err := e.sup.Kill(e.agentID); err == nil {
		t.Error("Kill after Close succeeded, want a refusal")
	}
	if e.sessionLive() {
		t.Error("agent has a live session after Close")
	}
}

// A user Stop after the cap hit must not replace the cap reason.
func TestCapReasonSurvivesUserStop(t *testing.T) {
	e := newEnv(t, nil)
	if err := e.sup.Start(e.agentID, "go"); err != nil {
		t.Fatal(err)
	}
	s := e.be.session(t, 0)
	s.emit(initEv())
	e.waitStatus(e.agentID, StatusRunning)
	e.sup.mu.Lock()
	a := e.sup.agents[e.agentID]
	a.capped, a.userStopped = true, true
	e.sup.mu.Unlock()
	s.finish(false, "stopped")
	e.waitStatus(e.agentID, StatusCapped)
}

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "x"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	return dir
}

func (e *env) startWithWorktree() string {
	e.t.Helper()
	if err := e.sup.Start(e.agentID, "go"); err != nil {
		e.t.Fatal(err)
	}
	s := e.be.session(e.t, 0)
	wt := s.spec.WorkDir
	if wt == "" {
		e.t.Fatal("no workdir")
	}
	s.emit(initEv())
	e.waitStatus(e.agentID, StatusRunning)
	if err := e.sup.Stop(e.agentID); err != nil {
		e.t.Fatal(err)
	}
	e.waitStatus(e.agentID, StatusStopped)
	return wt
}

func TestArchiveRemovesWorktree(t *testing.T) {
	repo := gitRepo(t)
	e := newEnv(t, func(f *FleetConfig, a *AgentConfig) { f.WorkDir = repo; a.UseWorktree = true })
	wt := e.startWithWorktree()
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("worktree %s missing before archive: %v", wt, err)
	}
	if err := e.sup.ArchiveAgent(e.agentID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the worktree to be removed", func() bool { _, err := os.Stat(wt); return err != nil })
	waitFor(t, "the path to clear", func() bool { return e.view(e.agentID).Worktree == "" })
}

func TestArchiveKeepsDirtyWorktreeAndNotifies(t *testing.T) {
	repo := gitRepo(t)
	e := newEnv(t, func(f *FleetConfig, a *AgentConfig) { f.WorkDir = repo; a.UseWorktree = true })
	notes := make(chan Notice, 4)
	e.sup.SetNotifier(func(n Notice) { notes <- n })
	wt := e.startWithWorktree()
	if err := os.WriteFile(filepath.Join(wt, "new.txt"), []byte("work"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.sup.ArchiveAgent(e.agentID); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-notes:
		if !n.Error || n.AgentID != e.agentID {
			t.Errorf("notice = %+v, want an error for the agent", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no notice for the kept worktree")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("dirty worktree was removed: %v", err)
	}
}

func TestDeleteFleetRemovesWorktrees(t *testing.T) {
	repo := gitRepo(t)
	e := newEnv(t, func(f *FleetConfig, a *AgentConfig) { f.WorkDir = repo; a.UseWorktree = true })
	wt := e.startWithWorktree()
	if err := e.sup.DeleteFleet(e.fleetID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the worktree to be removed", func() bool { _, err := os.Stat(wt); return err != nil })
}

// Renaming a stopped agent keeps its worktree instead of orphaning it.
func TestRenameKeepsWorktree(t *testing.T) {
	repo := gitRepo(t)
	e := newEnv(t, func(f *FleetConfig, a *AgentConfig) { f.WorkDir = repo; a.UseWorktree = true })
	wt := e.startWithWorktree()
	cfg, _ := e.sup.AgentConfig(e.agentID)
	cfg.Name = "renamed"
	if err := e.sup.UpdateAgent(e.agentID, cfg); err != nil {
		t.Fatal(err)
	}
	if err := e.sup.Start(e.agentID, "again"); err != nil {
		t.Fatal(err)
	}
	if got := e.be.session(t, 1).spec.WorkDir; got != wt {
		t.Errorf("restart used %s, want the old worktree %s", got, wt)
	}
}
