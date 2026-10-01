package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"atlas-commander/internal/agent"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sub", "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustFleet(t *testing.T, s *Store, name string) Fleet {
	t.Helper()
	f := Fleet{Name: name}
	if err := s.CreateFleet(&f); err != nil {
		t.Fatal(err)
	}
	return f
}

func mustAgent(t *testing.T, s *Store, fleet, name string) Agent {
	t.Helper()
	a := Agent{FleetID: fleet, Name: name, Backend: "claudecode"}
	if err := s.CreateAgent(&a); err != nil {
		t.Fatal(err)
	}
	return a
}

func mustTask(t *testing.T, s *Store, fleet, title string, prio int, created time.Time) Task {
	t.Helper()
	k := Task{FleetID: fleet, Title: title, Priority: prio, CreatedAt: created}
	if err := s.CreateTask(&k); err != nil {
		t.Fatal(err)
	}
	return k
}

func version(t *testing.T, s *Store) int {
	t.Helper()
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// A fresh database must come up at the current schema, with the folder made for it.
func TestOpenMigratesFromEmpty(t *testing.T) {
	s := open(t)
	if got := version(t, s); got != SchemaVersion() {
		t.Errorf("got version %d, want %d", got, SchemaVersion())
	}
	var fk int
	s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk)
	if fk != 1 {
		t.Error("foreign keys are off")
	}
}

// The board is the source of truth, so a restart must find everything.
func TestReopenKeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	f := mustFleet(t, s, "alpha")
	a := mustAgent(t, s, f.ID, "one")
	s.Append(&AuditEntry{Kind: "init", AgentID: a.ID})
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetAgent(a.ID)
	if err != nil || got.Name != "one" || got.GatePolicy != DefaultGatePolicy {
		t.Errorf("got %+v, %v, want agent one with the default policy", got, err)
	}
	if es, _ := s.AuditEntries(Filter{}, 0); len(es) != 1 {
		t.Errorf("got %d audit rows, want 1", len(es))
	}
}

// Opening a newer database and then "fixing" it would lose its data; it must be refused untouched.
func TestOpenRefusesNewerDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.db")
	s, _ := Open(path)
	f := mustFleet(t, s, "keep")
	if _, err := s.db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("got %v, want a newer-version error", err)
	}
	// The refused file must be left as it was, for the newer build.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var v int
	raw.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != 99 {
		t.Errorf("got version %d after refusal, want 99", v)
	}
	var name string
	if err := raw.QueryRow(`SELECT name FROM fleets WHERE id=?`, f.ID).Scan(&name); err != nil || name != "keep" {
		t.Errorf("got %q, %v, want fleet kept", name, err)
	}
}

// The log is evidence; neither an UPDATE nor a DELETE may change it, even by
// accident from a future query.
func TestAuditIsAppendOnly(t *testing.T) {
	s := open(t)
	e := AuditEntry{Kind: "text", AgentID: "a"}
	if err := s.Append(&e); err != nil {
		t.Fatal(err)
	}
	if e.Seq == 0 {
		t.Error("Seq not set")
	}
	_, err := s.db.Exec(`UPDATE audit SET cost_usd=0`)
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Errorf("update: got %v, want append-only error", err)
	}
	_, err = s.db.Exec(`DELETE FROM audit`)
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Errorf("delete: got %v, want append-only error", err)
	}
	if err := s.Append(&AuditEntry{}); err == nil {
		t.Error("entry without kind: got nil, want an error")
	}
}

// Deleting a fleet takes its agents and tasks; deleting an agent only
// unassigns tasks; the audit log is untouched by both.
func TestCascadeDeletes(t *testing.T) {
	s := open(t)
	f := mustFleet(t, s, "f")
	a := mustAgent(t, s, f.ID, "a")
	k := mustTask(t, s, f.ID, "t", 0, time.Time{})
	k.AgentID = a.ID
	if err := s.UpdateTask(k); err != nil {
		t.Fatal(err)
	}
	s.Append(&AuditEntry{Kind: "text", FleetID: f.ID, AgentID: a.ID})

	if err := s.DeleteAgent(a.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTask(k.ID)
	if err != nil || got.AgentID != "" {
		t.Errorf("after agent delete: got %+v, %v, want task kept and unassigned", got, err)
	}
	a2 := mustAgent(t, s, f.ID, "a2")
	if err := s.DeleteFleet(f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAgent(a2.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("agent after fleet delete: got %v, want ErrNotFound", err)
	}
	if _, err := s.GetTask(k.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("task after fleet delete: got %v, want ErrNotFound", err)
	}
	if es, _ := s.AuditEntries(Filter{}, 0); len(es) != 1 {
		t.Errorf("got %d audit rows, want 1 kept", len(es))
	}
}

// Names are how people refer to fleets and agents, so duplicates must fail clearly.
func TestUniqueNamesAndUpdates(t *testing.T) {
	s := open(t)
	f := mustFleet(t, s, "f")
	if err := s.CreateFleet(&Fleet{Name: "f"}); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate fleet: got %v, want ErrExists", err)
	}
	a := mustAgent(t, s, f.ID, "a")
	if err := s.CreateAgent(&Agent{FleetID: f.ID, Name: "a"}); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate agent: got %v, want ErrExists", err)
	}
	// Same name in another fleet is fine.
	g := mustFleet(t, s, "g")
	mustAgent(t, s, g.ID, "a")

	a.Archived, a.UseWorktree, a.CostCapUSD = true, true, 1.5
	if err := s.UpdateAgent(a); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentStatus(a.ID, "running"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentSession(a.ID, "sess-1"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetAgent(a.ID)
	if !got.Archived || !got.UseWorktree || got.CostCapUSD != 1.5 || got.Status != "running" || got.SessionID != "sess-1" {
		t.Errorf("got %+v, want updates stored", got)
	}
	if l, _ := s.ListAgents(f.ID, false); len(l) != 0 {
		t.Errorf("without archived: got %d, want 0", len(l))
	}
	if l, _ := s.ListAgents(f.ID, true); len(l) != 1 {
		t.Errorf("with archived: got %d, want 1", len(l))
	}
	if err := s.SetAgentStatus("nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing agent: got %v, want ErrNotFound", err)
	}
	f.BudgetUSD = 20
	if err := s.UpdateFleet(f); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FleetByName("f"); got.BudgetUSD != 20 {
		t.Errorf("budget: got %v, want 20", got.BudgetUSD)
	}
}

func ids(ts []Task) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Title)
	}
	return out
}

// The scheduler takes the first ready task, so order and dependency gating decide what runs next.
func TestReadyTasksOrderAndGating(t *testing.T) {
	s := open(t)
	f := mustFleet(t, s, "f")
	base := time.UnixMilli(1_700_000_000_000)
	low := mustTask(t, s, f.ID, "low", 0, base)
	hi2 := mustTask(t, s, f.ID, "hi-late", 5, base.Add(2*time.Second))
	hi1 := mustTask(t, s, f.ID, "hi-early", 5, base.Add(time.Second))
	blocked := mustTask(t, s, f.ID, "blocked", 9, base)
	if err := s.AddTaskDep(blocked.ID, low.ID); err != nil {
		t.Fatal(err)
	}
	other := mustFleet(t, s, "other")
	mustTask(t, s, other.ID, "elsewhere", 100, base)

	got, err := s.ReadyTasks(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := "hi-early hi-late low"; strings.Join(ids(got), " ") != want {
		t.Errorf("got %v, want %s", ids(got), want)
	}
	// Running is not ready; done unblocks the dependent.
	s.SetTaskStatus(hi1.ID, TaskRunning, "")
	s.SetTaskStatus(low.ID, TaskDone, "ok")
	got, _ = s.ReadyTasks(f.ID)
	if want := "blocked hi-late"; strings.Join(ids(got), " ") != want {
		t.Errorf("after done: got %v, want %s", ids(got), want)
	}
	if k, _ := s.GetTask(low.ID); k.FinishedAt.IsZero() || k.Result != "ok" {
		t.Errorf("done task: got %+v, want finished_at and result", k)
	}
	// A failed dependency keeps its dependent waiting.
	s.SetTaskStatus(hi2.ID, TaskDone, "")
	bad := mustTask(t, s, f.ID, "bad", 0, base)
	s.SetTaskStatus(bad.ID, TaskFailed, "boom")
	s.RemoveTaskDep(blocked.ID, low.ID)
	if err := s.AddTaskDep(blocked.ID, bad.ID); err != nil {
		t.Fatal(err)
	}
	s.SetTaskStatus(blocked.ID, TaskQueued, "") // refused; stays queued
	got, _ = s.ReadyTasks(f.ID)
	if len(got) != 0 {
		t.Errorf("after failure: got %v, want none", ids(got))
	}
	if err := s.SetTaskStatus(low.ID, "weird", ""); err == nil {
		t.Error("bad status: got nil, want an error")
	}
	if deps, _ := s.TaskDeps(blocked.ID); len(deps) != 1 || deps[0] != bad.ID {
		t.Errorf("deps: got %v", deps)
	}
	s.RemoveTaskDep(blocked.ID, bad.ID)
	if deps, _ := s.TaskDeps(blocked.ID); len(deps) != 0 {
		t.Errorf("deps after remove: got %v", deps)
	}
}

// A cycle would leave tasks waiting for each other forever.
func TestAddTaskDepRejectsCycles(t *testing.T) {
	s := open(t)
	f := mustFleet(t, s, "f")
	a := mustTask(t, s, f.ID, "a", 0, time.Time{})
	b := mustTask(t, s, f.ID, "b", 0, time.Time{})
	c := mustTask(t, s, f.ID, "c", 0, time.Time{})
	for _, p := range [][2]string{{b.ID, a.ID}, {c.ID, b.ID}} {
		if err := s.AddTaskDep(p[0], p[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddTaskDep(a.ID, c.ID); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("indirect cycle: got %v, want cycle error", err)
	}
	if err := s.AddTaskDep(a.ID, a.ID); err == nil {
		t.Error("self dependency: got nil, want an error")
	}
	if err := s.AddTaskDep(a.ID, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing task: got %v, want ErrNotFound", err)
	}
	// Diamonds are not cycles.
	d := mustTask(t, s, f.ID, "d", 0, time.Time{})
	if err := s.AddTaskDep(d.ID, a.ID); err != nil {
		t.Error(err)
	}
	if err := s.AddTaskDep(d.ID, c.ID); err != nil {
		t.Errorf("diamond: got %v, want nil", err)
	}
	// Adding the same edge twice is harmless.
	if err := s.AddTaskDep(d.ID, c.ID); err != nil {
		t.Errorf("repeat: got %v, want nil", err)
	}
}

// A pending request must survive a restart, and deciding it must leave a mark in the audit log.
func TestApprovalsPendingAndDecide(t *testing.T) {
	s := open(t)
	f := mustFleet(t, s, "f")
	a := mustAgent(t, s, f.ID, "a")
	ap := Approval{AgentID: a.ID, SessionID: "s1", Tool: "Bash", Input: `{"command":"ls"}`}
	if err := s.AddApproval(&ap); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.PendingApprovals(); len(p) != 1 || p[0].Decision != "" {
		t.Fatalf("pending: got %+v, want one undecided", p)
	}
	if err := s.DecideApproval(ap.ID, "maybe", ""); err == nil {
		t.Error("bad decision: got nil, want an error")
	}
	if err := s.DecideApproval(ap.ID, DecisionDeny, "too risky"); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.PendingApprovals(); len(p) != 0 {
		t.Errorf("pending after decide: got %d, want 0", len(p))
	}
	if err := s.DecideApproval(ap.ID, DecisionAllow, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("second decision: got %v, want ErrNotFound", err)
	}
	got, _ := s.GetApproval(ap.ID)
	if got.Decision != DecisionDeny || got.Reason != "too risky" || got.DecidedAt.IsZero() {
		t.Errorf("got %+v, want denied with reason", got)
	}
	es, _ := s.AuditEntries(Filter{Kind: "approval_decision"}, 0)
	if len(es) != 1 || es[0].FleetID != f.ID || es[0].Tool != "Bash" || !strings.Contains(es[0].Detail, "too risky") {
		t.Errorf("audit: got %+v, want one decision row for the fleet", es)
	}
}

// Fixed data set for the analytics: two agents in one fleet, known numbers.
func seedAnalytics(t *testing.T, s *Store) (t0 time.Time) {
	t.Helper()
	t0 = time.UnixMilli(1_700_000_000_000 / 3_600_000 * 3_600_000) // on the hour
	at := func(d time.Duration) time.Time { return t0.Add(d) }
	err := s.AppendBatch([]AuditEntry{
		// agent A, session s1
		{At: at(1 * time.Minute), FleetID: "F", AgentID: "A", SessionID: "s1", Kind: "init"},
		{At: at(2 * time.Minute), FleetID: "F", AgentID: "A", SessionID: "s1", Kind: "tool_use", Tool: "Bash"},
		{At: at(3 * time.Minute), FleetID: "F", AgentID: "A", SessionID: "s1", Kind: "tool_use", Tool: "Edit"},
		{At: at(4 * time.Minute), FleetID: "F", AgentID: "A", SessionID: "s1", Kind: "tool_result", IsError: true},
		{At: at(5 * time.Minute), FleetID: "F", AgentID: "A", SessionID: "s1", Kind: "usage",
			Usage: agent.Usage{Input: 100, Output: 1000, CacheRead: 5000, CacheWrite5m: 10, CacheWrite1h: 20}, CostUSD: 0.50},
		{At: at(6 * time.Minute), FleetID: "F", AgentID: "A", SessionID: "s1", Kind: "result"},
		// agent A, session s2, an hour later
		{At: at(61 * time.Minute), FleetID: "F", AgentID: "A", SessionID: "s2", Kind: "usage",
			Usage: agent.Usage{Input: 50, Output: 500}, CostUSD: 0.25},
		{At: at(62 * time.Minute), FleetID: "F", AgentID: "A", SessionID: "s2", Kind: "result", IsError: true},
		// agent B
		{At: at(10 * time.Minute), FleetID: "F", AgentID: "B", SessionID: "s3", Kind: "usage",
			Usage: agent.Usage{Output: 300}, CostUSD: 0.10},
		{At: at(11 * time.Minute), FleetID: "F", AgentID: "B", SessionID: "s3", Kind: "result"},
		// another fleet, and a row with no agent
		{At: at(12 * time.Minute), FleetID: "G", AgentID: "C", SessionID: "s4", Kind: "usage", CostUSD: 9},
		{At: at(13 * time.Minute), FleetID: "F", Kind: "error", IsError: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return t0
}

func near(a, b float64) bool { d := a - b; return d < 1e-9 && d > -1e-9 }

// These numbers drive budget caps and the Analyze charts; each is worked out by hand from the seed data.
func TestAnalyticsOnKnownData(t *testing.T) {
	s := open(t)
	t0 := seedAnalytics(t, s)
	f := Filter{FleetID: "F"}

	as, err := s.AgentStats(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 2 || as[0].AgentID != "A" || as[1].AgentID != "B" {
		t.Fatalf("agents: got %+v, want A then B", as)
	}
	a := as[0]
	if !near(a.CostUSD, 0.75) || a.Usage.Input != 150 || a.Usage.Output != 1500 || a.Usage.CacheRead != 5000 ||
		a.Usage.CacheWrite5m != 10 || a.Usage.CacheWrite1h != 20 {
		t.Errorf("A usage: got %+v cost %v", a.Usage, a.CostUSD)
	}
	if a.Results != 2 || a.Successes != 1 || a.ToolCalls != 2 || a.Errors != 2 {
		t.Errorf("A counts: got results %d ok %d tools %d errors %d, want 2 1 2 2", a.Results, a.Successes, a.ToolCalls, a.Errors)
	}
	if !near(a.SuccessRate(), 0.5) {
		t.Errorf("A success rate: got %v, want 0.5", a.SuccessRate())
	}
	if !near(a.OutputPerUSD(), 2000) {
		t.Errorf("A output per USD: got %v, want 2000", a.OutputPerUSD())
	}
	if !near(a.CostPerSuccess(), 0.75) {
		t.Errorf("A cost per success: got %v, want 0.75", a.CostPerSuccess())
	}
	if b := as[1]; !near(b.CostUSD, 0.10) || b.Successes != 1 || !near(b.SuccessRate(), 1) || !near(b.OutputPerUSD(), 3000) {
		t.Errorf("B: got %+v", b)
	}

	// Time range: only the first hour keeps agent A's session s1.
	first, _ := s.AgentStats(Filter{FleetID: "F", From: t0, To: t0.Add(time.Hour)})
	if len(first) != 2 || !near(first[0].CostUSD, 0.50) {
		t.Errorf("first hour: got %+v, want A at 0.50", first)
	}

	ss, _ := s.SessionStats(f)
	if len(ss) != 3 {
		t.Fatalf("sessions: got %d, want 3", len(ss))
	}
	if ss[0].SessionID != "s2" || !near(ss[0].CostUSD, 0.25) || ss[0].Errors != 1 || ss[0].AgentID != "A" {
		t.Errorf("newest session: got %+v", ss[0])
	}
	var s1 SessionStats
	for _, x := range ss {
		if x.SessionID == "s1" {
			s1 = x
		}
	}
	if !near(s1.CostUSD, 0.50) || s1.ToolCalls != 2 || !s1.First.Equal(t0.Add(time.Minute)) || !s1.Last.Equal(t0.Add(6*time.Minute)) {
		t.Errorf("s1: got %+v", s1)
	}

	tot, _ := s.FleetTotals(f)
	if !near(tot.CostUSD, 0.85) || tot.Errors != 3 {
		t.Errorf("fleet totals: got cost %v errors %d, want 0.85 3", tot.CostUSD, tot.Errors)
	}
	if c, _ := s.FleetCostSince("F", time.Time{}); !near(c, 0.85) {
		t.Errorf("fleet cost: got %v, want 0.85", c)
	}
	if c, _ := s.FleetCostSince("F", t0.Add(time.Hour)); !near(c, 0.25) {
		t.Errorf("fleet cost since +1h: got %v, want 0.25", c)
	}
	if c, _ := s.FleetCostSince("none", time.Time{}); c != 0 {
		t.Errorf("unknown fleet: got %v, want 0", c)
	}

	pts, err := s.CostSeries(f, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 2 || !pts[0].Start.Equal(t0) || !near(pts[0].CostUSD, 0.60) || pts[0].OutputTokens != 1300 ||
		!pts[1].Start.Equal(t0.Add(time.Hour)) || !near(pts[1].CostUSD, 0.25) {
		t.Errorf("series: got %+v, want 0.60 then 0.25", pts)
	}
	empty, _ := s.FleetTotals(Filter{FleetID: "none"})
	if empty.SuccessRate() != 0 || empty.OutputPerUSD() != 0 || empty.CostPerSuccess() != 0 {
		t.Errorf("empty totals: got %+v, want zero ratios", empty)
	}
}

// The supervisor appends from many goroutines; nothing may be lost or deadlock, and Seq must be unique.
func TestConcurrentAppend(t *testing.T) {
	s := open(t)
	const workers, each = 16, 50
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if i%10 == 0 {
					batch := []AuditEntry{{Kind: "usage", CostUSD: 1}, {Kind: "usage", CostUSD: 1}}
					if err := s.AppendBatch(batch); err != nil {
						t.Error(err)
					}
					continue
				}
				if err := s.Append(&AuditEntry{Kind: "usage", CostUSD: 1}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	want := workers * (each + each/10)
	tot, err := s.FleetTotals(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if int(tot.CostUSD) != want {
		t.Errorf("got %v rows, want %d", tot.CostUSD, want)
	}
	var distinct int
	s.db.QueryRow(`SELECT COUNT(DISTINCT seq) FROM audit`).Scan(&distinct)
	if distinct != want {
		t.Errorf("got %d distinct seq, want %d", distinct, want)
	}
}

// IDs are used as keys and in file names, so they must not collide or be empty.
func TestNewIDIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewID()
		if len(id) != 16 || seen[id] {
			t.Fatalf("got %q (seen %v), want unique 16 hex chars", id, seen[id])
		}
		seen[id] = true
	}
}

// Every allowed and refused move, since a late "done" must never undo a cancel.
func TestSetTaskStatusTransitions(t *testing.T) {
	s := open(t)
	f := mustFleet(t, s, "f")
	statuses := []string{TaskQueued, TaskRunning, TaskDone, TaskFailed, TaskCancelled}
	allowed := map[string][]string{
		TaskQueued:  {TaskRunning, TaskDone, TaskFailed, TaskCancelled},
		TaskRunning: {TaskDone, TaskFailed, TaskCancelled},
	}
	for _, from := range statuses {
		for _, to := range statuses {
			k := mustTask(t, s, f.ID, "t", 0, time.Time{})
			switch from {
			case TaskRunning:
				s.SetTaskStatus(k.ID, TaskRunning, "")
			case TaskDone, TaskFailed, TaskCancelled:
				if err := s.SetTaskStatus(k.ID, from, "r"); err != nil {
					t.Fatal(err)
				}
			}
			want := false
			for _, a := range allowed[from] {
				want = want || a == to
			}
			err := s.SetTaskStatus(k.ID, to, "late")
			if want && err != nil {
				t.Errorf("%s -> %s: got %v, want ok", from, to, err)
			}
			if !want && !errors.Is(err, ErrConflict) {
				t.Errorf("%s -> %s: got %v, want ErrConflict", from, to, err)
			}
			got, _ := s.GetTask(k.ID)
			if !want && got.Status != from {
				t.Errorf("%s -> %s refused but status became %s", from, to, got.Status)
			}
			if want && to == TaskRunning && got.StartedAt.IsZero() {
				t.Error("running did not set started_at")
			}
			if want && to != TaskRunning && got.FinishedAt.IsZero() {
				t.Errorf("%s did not set finished_at", to)
			}
		}
	}
	if err := s.SetTaskStatus("nope", TaskDone, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing task: got %v, want ErrNotFound", err)
	}
}

// UpdateTask must not be able to change status, or it would bypass the
// transition rules.
func TestUpdateTaskLeavesStatusAlone(t *testing.T) {
	s := open(t)
	f := mustFleet(t, s, "f")
	k := mustTask(t, s, f.ID, "t", 0, time.Time{})
	s.SetTaskStatus(k.ID, TaskCancelled, "x")
	k.Status, k.Title = TaskQueued, "new"
	if err := s.UpdateTask(k); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTask(k.ID)
	if got.Status != TaskCancelled || got.Title != "new" {
		t.Errorf("got %s %q, want cancelled and the new title", got.Status, got.Title)
	}
	k.AgentID = "ghost"
	if err := s.UpdateTask(k); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown agent: got %v, want ErrNotFound", err)
	}
}

// A dependency across fleets could never be scheduled sensibly and would leak
// one fleet's board into another's.
func TestAddTaskDepRejectsCrossFleet(t *testing.T) {
	s := open(t)
	f1, f2 := mustFleet(t, s, "f1"), mustFleet(t, s, "f2")
	a := mustTask(t, s, f1.ID, "a", 0, time.Time{})
	b := mustTask(t, s, f2.ID, "b", 0, time.Time{})
	if err := s.AddTaskDep(a.ID, b.ID); err == nil {
		t.Fatal("got nil, want a cross-fleet error")
	}
	if d, _ := s.TaskDeps(a.ID); len(d) != 0 {
		t.Errorf("got deps %v, want none", d)
	}
}

// '?' and '#' in a database path would otherwise cut the DSN short and open a
// different file. Windows forbids '?' in names, so there only '#' and '%' are
// tried; they cut a DSN short just the same.
func TestOpenPathWithQueryCharacters(t *testing.T) {
	name := "we?ird#dir%41"
	if runtime.GOOS == "windows" {
		name = "weird#dir%41"
	}
	p := filepath.Join(t.TempDir(), name, "c.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	mustFleet(t, s, "f")
	s.Close()
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("database not at the requested path: %v", err)
	}
	s, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if fl, _ := s.ListFleets(); len(fl) != 1 {
		t.Errorf("got %d fleets, want 1", len(fl))
	}
}

// A request left pending by a removed agent has no hook to answer it, and must
// not clutter the board; the decision is audited.
func TestDeleteAgentDeniesPendingApprovals(t *testing.T) {
	s := open(t)
	f := mustFleet(t, s, "f")
	a := mustAgent(t, s, f.ID, "a")
	b := mustAgent(t, s, f.ID, "b")
	pa := Approval{AgentID: a.ID, Tool: "Bash"}
	pb := Approval{AgentID: b.ID, Tool: "Bash"}
	s.AddApproval(&pa)
	s.AddApproval(&pb)
	if err := s.DeleteAgent(a.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetApproval(pa.ID)
	if err != nil || got.Decision != DecisionDeny || got.Reason != "agent removed" {
		t.Errorf("got %+v %v, want denied with reason agent removed", got, err)
	}
	if p, _ := s.PendingApprovals(); len(p) != 1 || p[0].ID != pb.ID {
		t.Errorf("pending: got %v, want only b's", p)
	}
	if err := s.DeleteFleet(f.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetApproval(pb.ID); got.Decision != DecisionDeny {
		t.Errorf("fleet delete: got decision %q, want deny", got.Decision)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM audit WHERE kind='approval_decision'`).Scan(&n)
	if n != 2 {
		t.Errorf("got %d audit decisions, want 2", n)
	}
}

// Unknown parents must surface as ErrNotFound, not a driver message.
func TestForeignKeyFailuresAreNotFound(t *testing.T) {
	s := open(t)
	if err := s.CreateAgent(&Agent{FleetID: "ghost", Name: "a"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("CreateAgent: got %v, want ErrNotFound", err)
	}
	if err := s.CreateTask(&Task{FleetID: "ghost"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("CreateTask: got %v, want ErrNotFound", err)
	}
}
