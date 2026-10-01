// Package demo is a fake fleet for developing and screenshotting the UI
// without running agents: `atlas-commander --demo`. Numbers move on a
// clock so charts and live cells can be checked; commands change states.
package demo

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"atlas-commander/internal/agent"
	"atlas-commander/internal/fleet"
	"atlas-commander/internal/store"
)

type Fleet struct {
	mu     sync.Mutex
	seq    uint64
	start  time.Time
	agents []fleet.AgentView
	appr   []fleet.ApprovalView
	logs   map[string][]fleet.Entry
	burn   []float64
	cost   []float64
	spent  float64
	fleets []fleet.FleetView
	tasks  []fleet.TaskView
	cfgs   map[string]fleet.AgentConfig
	audit  []store.AuditEntry
	nextID int
	notify func(fleet.Notice)
	stop   chan struct{}
	once   sync.Once // Close may be called more than once
}

func New() *Fleet {
	now := time.Now()
	f := &Fleet{start: now, logs: map[string][]fleet.Entry{}, stop: make(chan struct{})}
	mk := func(id, name, fl, model, task, branch string, st fleet.Status, cost, cap float64) fleet.AgentView {
		return fleet.AgentView{
			ID: id, Name: name, FleetID: fl, FleetName: fl, Backend: agent.BackendClaudeCode, Model: model,
			WorkDir: "~/src/" + fl, Status: st, Task: task, Branch: branch, Worktree: "~/.local/share/atlas-commander/wt/" + branch,
			CostUSD: cost, SessionCost: cost * 0.6, CapUSD: cap, StartedAt: now.Add(-time.Duration(rand.IntN(3000)) * time.Second),
			Latency: time.Duration(800+rand.IntN(9000)) * time.Millisecond, SessionID: fmt.Sprintf("%08x-%04x", rand.Uint32(), rand.Uint32()&0xffff),
			Session: agent.Usage{Input: 1200, Output: int64(2000 + rand.IntN(40000)), CacheRead: int64(100000 + rand.IntN(900000)), CacheWrite5m: 24000},
		}
	}
	f.agents = []fleet.AgentView{
		mk("a1", "refactor-auth", "backend", "claude-opus-5-5", "Move session handling into internal/auth and add tests", "atlas/refactor-auth-a1", fleet.StatusRunning, 1.84, 10),
		mk("a2", "docs-writer", "backend", "claude-sonnet-5-5", "Write the API reference for /v2/fleets", "atlas/docs-writer-a2", fleet.StatusWaiting, 0.42, 0),
		mk("a3", "migrations", "backend", "claude-opus-5-5", "Add the audit_v3 migration", "atlas/migrations-a3", fleet.StatusApproval, 3.10, 4),
		mk("a4", "flaky-tests", "web", "claude-sonnet-5-5", "Find why TestCheckout flakes on CI", "atlas/flaky-tests-a4", fleet.StatusError, 0.77, 5),
		mk("a5", "lint-sweep", "web", "claude-haiku-4-5", "", "", fleet.StatusIdle, 0.05, 1),
		mk("a6", "perf-probe", "web", "claude-opus-5-5", "Profile the dashboard render path", "atlas/perf-probe-a6", fleet.StatusHeld, 2.25, 0),
	}
	f.agents[3].Error = "Process exited with code 1: API rate limit"
	f.agents[2].LastTool = "Bash: go run ./cmd/migrate -dry-run"
	f.agents[0].LastTool = "Edit: internal/auth/session.go"
	f.agents[1].LastTool = "Read: docs/api/fleets.md"
	f.agents[5].LastTool = "Bash: go test -bench . ./internal/dash"
	for i := range f.agents {
		f.agents[i].LastToolAt = now.Add(-time.Duration(rand.IntN(400)) * time.Second)
		f.agents[i].Total = f.agents[i].Session
	}
	f.appr = []fleet.ApprovalView{{ID: "p1", AgentID: "a3", AgentName: "migrations", Tool: "Bash",
		Summary: "go run ./cmd/migrate -apply", Input: `{"command": "go run ./cmd/migrate -apply"}`, At: now.Add(-40 * time.Second)}}
	f.logs["a1"] = []fleet.Entry{
		{At: now.Add(-90 * time.Second), Kind: agent.EventInit, Text: "Session started (claude-opus-5-5)"},
		{At: now.Add(-89 * time.Second), User: true, Text: "Move session handling into internal/auth and add tests."},
		{At: now.Add(-80 * time.Second), Kind: agent.EventText, Text: "I'll start by finding where sessions are created and validated."},
		{At: now.Add(-79 * time.Second), Kind: agent.EventToolUse, Tool: "Grep", Text: "NewSession|ValidateSession"},
		{At: now.Add(-78 * time.Second), Kind: agent.EventToolResult, Text: "server/session.go:14, server/middleware.go:52"},
		{At: now.Add(-60 * time.Second), Kind: agent.EventToolUse, Tool: "Edit", Text: "internal/auth/session.go"},
		{At: now.Add(-40 * time.Second), Kind: agent.EventToolUse, Tool: "Bash", Text: "go test ./internal/auth/..."},
		{At: now.Add(-38 * time.Second), Kind: agent.EventToolResult, IsError: true, Text: "--- FAIL: TestExpiry (0.00s)\n    session_test.go:41: got 0, want 3600"},
		{At: now.Add(-20 * time.Second), Kind: agent.EventText, Text: "The expiry is computed in milliseconds; fixing the unit."},
	}
	f.fleets = []fleet.FleetView{{ID: "backend", Name: "backend", WorkDir: "~/src/backend", BudgetUSD: 25}, {ID: "web", Name: "web", WorkDir: "~/src/web"}}
	f.tasks = []fleet.TaskView{
		{ID: "t1", FleetID: "web", Title: "Upgrade the chart library", Prompt: "Upgrade chart.js to v5 and fix the breaking changes.", Priority: 2, Status: "queued", Created: now.Add(-2 * time.Hour)},
		{ID: "t2", FleetID: "web", Title: "Visual regression pass", Prompt: "Re-run the screenshot suite and update the baselines that changed on purpose.", Priority: 1, Status: "queued", DependsOn: []string{"t1"}, Created: now.Add(-110 * time.Minute)},
		{ID: "t3", FleetID: "backend", Title: "Move session handling into internal/auth", Priority: 3, Status: "running", AgentID: "a1", AgentName: "refactor-auth", Created: now.Add(-3 * time.Hour)},
		{ID: "t4", FleetID: "backend", Title: "Write the /v2/fleets reference", Priority: 1, Status: "done", AgentID: "a2", AgentName: "docs-writer", Created: now.Add(-5 * time.Hour)},
		{ID: "t5", FleetID: "web", Title: "Find why TestCheckout flakes", Priority: 2, Status: "failed", AgentID: "a4", AgentName: "flaky-tests", Created: now.Add(-4 * time.Hour)},
	}
	f.cfgs = map[string]fleet.AgentConfig{}
	for _, a := range f.agents {
		f.cfgs[a.ID] = fleet.AgentConfig{FleetID: a.FleetID, Name: a.Name, Backend: a.Backend, Model: a.Model,
			CostCapUSD: a.CapUSD, UseWorktree: a.Branch != "", Approve: fleet.DefaultApprove}
	}
	f.audit = fakeAudit(f.agents, now)
	go f.run()
	return f
}

func (f *Fleet) Close() { f.once.Do(func() { close(f.stop) }) }

func (f *Fleet) run() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-f.stop:
			return
		case now := <-t.C:
			f.mu.Lock()
			s := now.Sub(f.start).Seconds()
			total, rate := 0.0, 0.0
			for i := range f.agents {
				a := &f.agents[i]
				v := 0.0
				if a.Status == fleet.StatusRunning || a.Status == fleet.StatusStarting {
					v = 60 + 50*math.Sin(s/7+float64(i)) + rand.Float64()*30
					a.Session.Output += int64(v)
					c := v * 25 / 1e6
					a.CostUSD += c
					a.SessionCost += c
					rate += c * 60
				}
				a.Burn = push(a.Burn, v)
				total += v
			}
			f.spent += rate / 60
			f.burn = push(f.burn, total)
			f.cost = push(f.cost, rate)
			f.seq++
			f.mu.Unlock()
		}
	}
}

func push(v []float64, x float64) []float64 {
	v = append(v, x)
	if len(v) > 60 {
		v = v[len(v)-60:]
	}
	return v
}

func (f *Fleet) Snapshot() *fleet.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := &fleet.Snapshot{Seq: f.seq, Spent: f.spent}
	for _, a := range f.agents {
		a.Burn = append([]float64(nil), a.Burn...)
		a.Pending = 0
		for _, p := range f.appr {
			if p.AgentID == a.ID {
				a.Pending++
			}
		}
		s.Agents = append(s.Agents, a)
	}
	s.Approvals = append(s.Approvals, f.appr...)
	s.Burn = append([]float64(nil), f.burn...)
	s.CostRate = append([]float64(nil), f.cost...)
	for _, fl := range f.fleets {
		for _, a := range f.agents {
			if a.FleetID == fl.ID && !a.Archived {
				fl.Agents++
				fl.SpentUSD += a.CostUSD
				if a.Status.Live() {
					fl.Live++
				}
			}
		}
		s.Fleets = append(s.Fleets, fl)
	}
	s.Agents = slices.DeleteFunc(s.Agents, func(a fleet.AgentView) bool { return a.Archived })
	for _, t := range f.tasks {
		if t.Status == "cancelled" {
			continue
		}
		t.DependsOn = slices.Clone(t.DependsOn)
		t.Ready = t.Status == "queued" && f.depsDone(t)
		s.Tasks = append(s.Tasks, t)
	}
	return s
}

func (f *Fleet) Transcript(id string, from int) ([]fleet.Entry, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := f.logs[id]
	if from >= len(l) {
		return nil, len(l)
	}
	return append([]fleet.Entry(nil), l[from:]...), len(l)
}

func (f *Fleet) set(id string, st fleet.Status) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.agents {
		if f.agents[i].ID == id {
			f.agents[i].Status = st
			f.seq++
			return nil
		}
	}
	return fmt.Errorf("no agent %s", id)
}

func (f *Fleet) Start(id, prompt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.agentByID(id)
	if i < 0 || f.agents[i].Archived {
		return errors.New("That agent no longer exists.")
	}
	f.logs[id] = append(f.logs[id], fleet.Entry{At: time.Now(), User: true, Text: prompt})
	f.agents[i].Task = prompt
	f.agents[i].Status = fleet.StatusRunning
	f.seq++
	return nil
}

func (f *Fleet) Send(id, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.agentByID(id)
	if i < 0 {
		return errors.New("That agent no longer exists.")
	}
	if !f.agents[i].Status.Live() {
		return errors.New("This agent isn't running. Start it first.")
	}
	f.logs[id] = append(f.logs[id], fleet.Entry{At: time.Now(), User: true, Text: text})
	f.seq++
	return nil
}

func (f *Fleet) Hold(id string) error   { return f.set(id, fleet.StatusHeld) }
func (f *Fleet) Resume(id string) error { return f.set(id, fleet.StatusRunning) }
func (f *Fleet) Stop(id string) error   { return f.set(id, fleet.StatusStopped) }
func (f *Fleet) Kill(id string) error   { return f.set(id, fleet.StatusStopped) }

func (f *Fleet) KillAll() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for i := range f.agents {
		if f.agents[i].Status.Live() {
			f.agents[i].Status = fleet.StatusStopped
			n++
		}
	}
	f.seq++
	return n
}

func (f *Fleet) Decide(id string, allow bool, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, p := range f.appr {
		if p.ID != id {
			continue
		}
		f.appr = slices.Delete(f.appr, i, i+1)
		e := store.AuditEntry{At: time.Now(), AgentID: p.AgentID, Kind: "approval_decision", Tool: p.Tool,
			Detail: fmt.Sprintf(`{"allow":%t,"reason":%q}`, allow, reason)}
		if j := f.agentByID(p.AgentID); j >= 0 {
			f.agents[j].Status = fleet.StatusRunning
			e.FleetID = f.agents[j].FleetID
		}
		f.addAudit(e)
		f.seq++
		return nil
	}
	return fmt.Errorf("no approval %s", id)
}

// addAudit appends a row with the next sequence number, like the store's
// autoincrement. The caller holds f.mu.
func (f *Fleet) addAudit(e store.AuditEntry) {
	e.Seq = 1
	if n := len(f.audit); n > 0 {
		e.Seq = f.audit[n-1].Seq + 1
	}
	f.audit = append(f.audit, e)
}
