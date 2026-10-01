package demo

import (
	"cmp"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"atlas-commander/internal/agent"
	"atlas-commander/internal/fleet"
	"atlas-commander/internal/observed"
	"atlas-commander/internal/store"
)

// The rest of ui.Controller. Everything stays in memory; analytics are
// computed from a synthetic audit log generated at start-up.

func (f *Fleet) id(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s%d", prefix, 100+f.nextID)
}

func (f *Fleet) fleetByID(id string) int {
	return slices.IndexFunc(f.fleets, func(x fleet.FleetView) bool { return x.ID == id })
}

func (f *Fleet) agentByID(id string) int {
	return slices.IndexFunc(f.agents, func(x fleet.AgentView) bool { return x.ID == id })
}

func (f *Fleet) taskByID(id string) int {
	return slices.IndexFunc(f.tasks, func(x fleet.TaskView) bool { return x.ID == id })
}

func (f *Fleet) depsDone(t fleet.TaskView) bool {
	for _, d := range t.DependsOn {
		if i := f.taskByID(d); i >= 0 && f.tasks[i].Status != "done" {
			return false
		}
	}
	return true
}

func (f *Fleet) CreateFleet(c fleet.FleetConfig) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.TrimSpace(c.Name) == "" {
		return "", errors.New("A fleet needs a name.")
	}
	id := f.id("f")
	f.fleets = append(f.fleets, fleet.FleetView{ID: id, Name: c.Name, WorkDir: c.WorkDir, BudgetUSD: c.BudgetUSD})
	f.seq++
	return id, nil
}

func (f *Fleet) UpdateFleet(id string, c fleet.FleetConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.fleetByID(id)
	if i < 0 {
		return errors.New("That fleet no longer exists.")
	}
	f.fleets[i].Name, f.fleets[i].WorkDir, f.fleets[i].BudgetUSD = c.Name, c.WorkDir, c.BudgetUSD
	for j := range f.agents {
		if f.agents[j].FleetID == id {
			f.agents[j].FleetName = c.Name
		}
	}
	f.seq++
	return nil
}

func (f *Fleet) DeleteFleet(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.agents {
		if a.FleetID == id && a.Status.Live() {
			return fmt.Errorf("%s is still running. Stop it before deleting its fleet.", a.Name)
		}
	}
	i := f.fleetByID(id)
	if i < 0 {
		return errors.New("That fleet no longer exists.")
	}
	f.fleets = slices.Delete(f.fleets, i, i+1)
	// Everything that belonged to the fleet goes with it, as in the real
	// store; leftovers would show up as tasks and approvals of nobody.
	gone := map[string]bool{}
	for _, a := range f.agents {
		if a.FleetID == id {
			gone[a.ID] = true
			delete(f.logs, a.ID)
			delete(f.cfgs, a.ID)
		}
	}
	f.agents = slices.DeleteFunc(f.agents, func(a fleet.AgentView) bool { return a.FleetID == id })
	f.appr = slices.DeleteFunc(f.appr, func(p fleet.ApprovalView) bool { return gone[p.AgentID] })
	f.tasks = slices.DeleteFunc(f.tasks, func(t fleet.TaskView) bool { return t.FleetID == id })
	f.seq++
	return nil
}

func (f *Fleet) RegisterAgent(c fleet.AgentConfig) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.TrimSpace(c.Name) == "" {
		return "", errors.New("An agent needs a name.")
	}
	fi := f.fleetByID(c.FleetID)
	if fi < 0 {
		return "", errors.New("Pick a fleet for the agent.")
	}
	for _, a := range f.agents {
		if a.FleetID == c.FleetID && a.Name == c.Name && !a.Archived {
			return "", fmt.Errorf("The %s fleet already has an agent called %s.", f.fleets[fi].Name, c.Name)
		}
	}
	id := f.id("a")
	wd := c.WorkDir
	if wd == "" {
		wd = f.fleets[fi].WorkDir
	}
	f.agents = append(f.agents, fleet.AgentView{ID: id, Name: c.Name, FleetID: c.FleetID, FleetName: f.fleets[fi].Name,
		Backend: c.Backend, Model: c.Model, WorkDir: wd, PinnedPrompt: c.PinnedPrompt, UseWorktree: c.UseWorktree,
		CapUSD: c.CostCapUSD, Status: fleet.StatusIdle})
	c.Approve = slices.Clone(c.Approve)
	f.cfgs[id] = c
	f.seq++
	return id, nil
}

func (f *Fleet) UpdateAgent(id string, c fleet.AgentConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.agentByID(id)
	if i < 0 {
		return errors.New("That agent no longer exists.")
	}
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return errors.New("An agent needs a name.")
	}
	for j, o := range f.agents {
		if j != i && o.FleetID == f.agents[i].FleetID && o.Name == c.Name && !o.Archived {
			return fmt.Errorf("This fleet already has an agent called %s.", c.Name)
		}
	}
	a := &f.agents[i]
	a.Name, a.Model, a.Backend, a.PinnedPrompt, a.CapUSD, a.UseWorktree = c.Name, c.Model, c.Backend, c.PinnedPrompt, c.CostCapUSD, c.UseWorktree
	if c.WorkDir != "" {
		a.WorkDir = c.WorkDir
	}
	c.Approve = slices.Clone(c.Approve)
	f.cfgs[id] = c
	f.seq++
	return nil
}

func (f *Fleet) AgentConfig(id string) (fleet.AgentConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.cfgs[id]
	if !ok {
		return c, errors.New("That agent no longer exists.")
	}
	c.Approve = slices.Clone(c.Approve)
	return c, nil
}

func (f *Fleet) ArchiveAgent(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.agentByID(id)
	if i < 0 {
		return errors.New("That agent no longer exists.")
	}
	if f.agents[i].Status.Live() {
		return fmt.Errorf("%s is running. Stop it before archiving.", f.agents[i].Name)
	}
	f.agents[i].Archived = true
	f.seq++
	return nil
}

func (f *Fleet) AddTask(c fleet.TaskConfig) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.TrimSpace(c.Title) == "" {
		return "", errors.New("A task needs a title.")
	}
	if f.fleetByID(c.FleetID) < 0 {
		return "", errors.New("That fleet no longer exists.")
	}
	id := f.id("t")
	if err := f.checkDeps(id, c.FleetID, c.DependsOn); err != nil {
		return "", err
	}
	f.tasks = append(f.tasks, fleet.TaskView{ID: id, FleetID: c.FleetID, Title: c.Title, Prompt: c.Prompt,
		Priority: c.Priority, Status: "queued", DependsOn: slices.Clone(c.DependsOn), Created: time.Now()})
	f.seq++
	return id, nil
}

func (f *Fleet) UpdateTask(id string, c fleet.TaskConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.taskByID(id)
	if i < 0 {
		return errors.New("That task no longer exists.")
	}
	t := &f.tasks[i]
	if t.Status != "queued" {
		return errors.New("Only queued tasks can be edited.")
	}
	if strings.TrimSpace(c.Title) == "" {
		return errors.New("A task needs a title.")
	}
	if err := f.checkDeps(id, t.FleetID, c.DependsOn); err != nil {
		return err
	}
	t.Title, t.Prompt, t.Priority, t.DependsOn = c.Title, c.Prompt, c.Priority, slices.Clone(c.DependsOn)
	f.seq++
	return nil
}

// checkDeps applies the store's dependency rules to task id (which may not
// exist yet): no self-dependency, only tasks that exist in the same fleet,
// and no cycle. The caller holds f.mu.
func (f *Fleet) checkDeps(id, fleetID string, deps []string) error {
	for _, d := range deps {
		if d == id {
			return errors.New("A task cannot depend on itself.")
		}
		i := f.taskByID(d)
		if i < 0 {
			return errors.New("A task it depends on no longer exists.")
		}
		if f.tasks[i].FleetID != fleetID {
			return errors.New("A task can only depend on a task in the same fleet.")
		}
	}
	// The new edges id -> d close a cycle when some d already reaches id.
	seen := map[string]bool{}
	var reaches func(from string) bool
	reaches = func(from string) bool {
		if from == id {
			return true
		}
		if seen[from] {
			return false
		}
		seen[from] = true
		if i := f.taskByID(from); i >= 0 {
			for _, n := range f.tasks[i].DependsOn {
				if reaches(n) {
					return true
				}
			}
		}
		return false
	}
	for _, d := range deps {
		if reaches(d) {
			return errors.New("That dependency would make a cycle.")
		}
	}
	return nil
}

func (f *Fleet) CancelTask(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.taskByID(id)
	if i < 0 {
		return errors.New("That task no longer exists.")
	}
	f.tasks[i].Status = "cancelled"
	f.seq++
	return nil
}

func (f *Fleet) Dispatch(taskID, agentID string) error {
	f.mu.Lock()
	ti, ai := f.taskByID(taskID), f.agentByID(agentID)
	if ti < 0 || ai < 0 {
		f.mu.Unlock()
		return errors.New("That task or agent no longer exists.")
	}
	t, a := &f.tasks[ti], &f.agents[ai]
	switch {
	case a.FleetID != t.FleetID:
		f.mu.Unlock()
		return errors.New("That agent belongs to a different fleet.")
	case a.Archived:
		f.mu.Unlock()
		return errors.New("This agent is archived.")
	case t.Status != "queued":
		f.mu.Unlock()
		return errors.New("Only queued tasks can be dispatched.")
	case !f.depsDone(*t):
		f.mu.Unlock()
		return errors.New("This task waits on other tasks that aren't done yet.")
	case a.Status.Live() && a.Status != fleet.StatusWaiting:
		f.mu.Unlock()
		return fmt.Errorf("%s is busy.", a.Name)
	}
	t.Status, t.AgentID, t.AgentName = "running", a.ID, a.Name
	a.TaskID = t.ID
	prompt := t.Prompt
	if prompt == "" {
		prompt = t.Title
	}
	f.mu.Unlock()
	return f.Start(agentID, prompt)
}

func (f *Fleet) Totals(flt store.Filter) (store.Totals, error) {
	var t store.Totals
	for _, e := range f.filter(flt) {
		addTotals(&t, e)
	}
	return t, nil
}

func (f *Fleet) AgentStats(flt store.Filter) ([]store.AgentStats, error) {
	by := map[string]*store.AgentStats{}
	var out []store.AgentStats
	for _, e := range f.filter(flt) {
		s := by[e.AgentID]
		if s == nil {
			s = &store.AgentStats{AgentID: e.AgentID}
			by[e.AgentID] = s
		}
		addTotals(&s.Totals, e)
	}
	for _, s := range by {
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b store.AgentStats) int { return cmp.Compare(b.CostUSD, a.CostUSD) })
	return out, nil
}

func (f *Fleet) SessionStats(flt store.Filter) ([]store.SessionStats, error) {
	by := map[string]*store.SessionStats{}
	var out []store.SessionStats
	for _, e := range f.filter(flt) {
		s := by[e.SessionID]
		if s == nil {
			s = &store.SessionStats{SessionID: e.SessionID, AgentID: e.AgentID, FleetID: e.FleetID, First: e.At}
			by[e.SessionID] = s
		}
		s.Last = e.At
		addTotals(&s.Totals, e)
	}
	for _, s := range by {
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b store.SessionStats) int { return b.Last.Compare(a.Last) })
	return out, nil
}

func (f *Fleet) CostSeries(flt store.Filter, bucket time.Duration) ([]store.CostPoint, error) {
	if bucket < time.Millisecond {
		bucket = time.Hour
	}
	var out []store.CostPoint
	for _, e := range f.filter(flt) {
		start := e.At.Truncate(bucket)
		if n := len(out); n == 0 || !out[n-1].Start.Equal(start) {
			out = append(out, store.CostPoint{Start: start})
		}
		p := &out[len(out)-1]
		p.CostUSD += e.CostUSD
		p.OutputTokens += e.Usage.Output
	}
	return out, nil
}

// Audit returns the newest entries first, like the store.
func (f *Fleet) Audit(flt store.Filter, limit int) ([]store.AuditEntry, error) {
	m := f.filter(flt)
	slices.Reverse(m)
	if limit > 0 && len(m) > limit {
		m = m[:limit]
	}
	return m, nil
}

func (f *Fleet) filter(flt store.Filter) []store.AuditEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.AuditEntry
	for _, e := range f.audit {
		if (flt.FleetID != "" && e.FleetID != flt.FleetID) || (flt.AgentID != "" && e.AgentID != flt.AgentID) ||
			(flt.SessionID != "" && e.SessionID != flt.SessionID) || (flt.Kind != "" && e.Kind != flt.Kind) ||
			(!flt.From.IsZero() && e.At.Before(flt.From)) || (!flt.To.IsZero() && !e.At.Before(flt.To)) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func addTotals(t *store.Totals, e store.AuditEntry) {
	t.Usage.Input += e.Usage.Input
	t.Usage.Output += e.Usage.Output
	t.Usage.CacheRead += e.Usage.CacheRead
	t.Usage.CacheWrite5m += e.Usage.CacheWrite5m
	t.Usage.CacheWrite1h += e.Usage.CacheWrite1h
	t.CostUSD += e.CostUSD
	switch e.Kind {
	case "result":
		t.Results++
		if !e.IsError {
			t.Successes++
		}
	case "tool_use":
		t.ToolCalls++
	}
	if e.IsError {
		t.Errors++
	}
}

// fakeAudit makes a week of turns for each agent, oldest first: per turn a
// usage row, a few tool calls and a result row carrying the cost.
func fakeAudit(agents []fleet.AgentView, now time.Time) []store.AuditEntry {
	r := rand.New(rand.NewPCG(7, 11))
	tools := []struct{ tool, detail string }{
		{"Read", `{"file_path":"internal/auth/session.go"}`},
		{"Grep", `{"pattern":"ValidateSession"}`},
		{"Bash", `{"command":"go test ./..."}`},
		{"Edit", `{"file_path":"internal/auth/session.go"}`},
		{"Write", `{"file_path":"docs/api/fleets.md"}`},
	}
	var out []store.AuditEntry
	for t := now.Add(-7 * 24 * time.Hour); t.Before(now); t = t.Add(time.Duration(20+r.IntN(70)) * time.Minute) {
		// Quieter at night.
		if h := t.Hour(); (h < 8 || h > 22) && r.IntN(4) != 0 {
			continue
		}
		a := agents[r.IntN(len(agents))]
		sess := fmt.Sprintf("%s-%s", a.ID, t.Format("0102"))
		base := store.AuditEntry{FleetID: a.FleetID, AgentID: a.ID, SessionID: sess}
		at := t
		row := func(kind, tool, detail string, isErr bool) store.AuditEntry {
			at = at.Add(time.Duration(2+r.IntN(20)) * time.Second)
			e := base
			e.At, e.Kind, e.Tool, e.Detail, e.IsError = at, kind, tool, detail, isErr
			return e
		}
		out = append(out, row("user_send", "", `{"text":"Continue with the next step."}`, false))
		for range 1 + r.IntN(5) {
			x := tools[r.IntN(len(tools))]
			out = append(out, row("tool_use", x.tool, x.detail, false))
			out = append(out, row("tool_result", x.tool, `{"text":"ok"}`, r.IntN(12) == 0))
		}
		u := row("usage", "", "", false)
		u.Usage = agent.Usage{Input: int64(800 + r.IntN(4000)), Output: int64(500 + r.IntN(9000)),
			CacheRead: int64(20000 + r.IntN(200000)), CacheWrite5m: int64(r.IntN(30000))}
		out = append(out, u)
		res := row("result", "", `{"text":"Turn finished"}`, r.IntN(9) == 0)
		mult := 1.0
		if strings.Contains(a.Model, "opus") {
			mult = 1.7
		} else if strings.Contains(a.Model, "haiku") {
			mult = 0.3
		}
		res.CostUSD = mult * (float64(u.Usage.Input)*3 + float64(u.Usage.Output)*15 +
			float64(u.Usage.CacheRead)*0.3 + float64(u.Usage.CacheWrite5m)*3.75) / 1e6
		out = append(out, res)
	}
	for i := range out {
		out[i].Seq = int64(i + 1) // the store numbers rows in insertion order
	}
	return out
}

func (f *Fleet) ObservedSessions() ([]observed.Session, error) {
	now := time.Now()
	return []observed.Session{
		{Path: "demo/1.jsonl", ID: "5f1c2a90", Project: "~/src/atlas-notes", Title: "Fix the export dialog losing focus on Wayland",
			Model: "claude-opus-5-5", GitBranch: "beta", Started: now.Add(-25 * time.Minute), Modified: now.Add(-20 * time.Second), Size: 412_000, Messages: 86, Live: true},
		{Path: "demo/2.jsonl", ID: "9ab03e11", Project: "~/src/atlas-monitor", Title: "Add a GPU temperature sensor for amdgpu",
			Model: "claude-sonnet-5-5", GitBranch: "gpu-temp", Started: now.Add(-3 * time.Hour), Modified: now.Add(-2 * time.Hour), Size: 1_830_000, Messages: 240},
		{Path: "demo/3.jsonl", ID: "c77d0b42", Project: "~/src/dotfiles", Title: "Why does tmux lose true colour over ssh?",
			Model: "claude-haiku-4-5", Started: now.Add(-50 * time.Hour), Modified: now.Add(-49 * time.Hour), Size: 64_000, Messages: 14},
	}, nil
}

func (f *Fleet) ObservedTranscript(path string) ([]observed.Line, error) {
	now := time.Now().Add(-5 * time.Minute)
	return []observed.Line{
		{At: now, Role: "user", Text: "The export dialog loses focus on Wayland after the file picker closes. Can you find out why?"},
		{At: now.Add(8 * time.Second), Role: "assistant", Text: "I'll look at how the dialog is parented and how the portal picker returns."},
		{At: now.Add(9 * time.Second), Role: "tool", Tool: "Grep", Text: "FileChooserNative"},
		{At: now.Add(10 * time.Second), Role: "result", Text: "src/export.c:212, src/export.c:288"},
		{At: now.Add(30 * time.Second), Role: "tool", Tool: "Bash", Text: "meson test -C build export"},
		{At: now.Add(41 * time.Second), Role: "result", IsError: true, Text: "1/1 export FAIL"},
		{At: now.Add(70 * time.Second), Role: "assistant", Text: "The picker isn't given a transient parent, so the compositor hands focus back to the main window."},
	}, nil
}

func (f *Fleet) Setup() fleet.SetupInfo {
	return fleet.SetupInfo{ClaudePath: "/usr/local/bin/claude", ClaudeVersion: "2.1.274", APIKeyEnv: "ANTHROPIC_API_KEY", APIKeySet: true}
}

func (f *Fleet) SetNotifier(fn func(fleet.Notice)) {
	f.mu.Lock()
	f.notify = fn
	f.mu.Unlock()
}
