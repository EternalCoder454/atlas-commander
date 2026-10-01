package fleet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"atlas-commander/internal/agent"
	"atlas-commander/internal/gate"
	"atlas-commander/internal/procgroup"
	"atlas-commander/internal/store"
	"atlas-commander/internal/worktree"
)

// effects collects what must happen after the mutex is released: desktop
// notices and sessions to stop. Doing them under the lock could block the UI
// thread behind a slow process.
type effects struct {
	notes []Notice
	stops []agent.Session
}

func (s *Supervisor) run(fx *effects) {
	s.emit(fx.notes)
	for _, sess := range fx.stops {
		sess := sess
		if !s.spawn(func() { _ = sess.Stop() }) {
			_ = sess.Stop() // closing: close() is waiting, so don't add to wg
		}
	}
}

// refresh recomputes the displayed status from the base status, pending
// approvals and the hold flag, and persists a change. Call with mu held.
func (s *Supervisor) refresh(a *agentState) {
	st := a.base
	if st.Live() {
		if a.pending > 0 {
			st = StatusApproval
		} else if a.held {
			st = StatusHeld
		}
	}
	s.touch()
	if st != a.status {
		a.status = st
		a.cfg.Status = string(st)
		_ = s.st.SetAgentStatus(a.cfg.ID, string(st))
	}
}

// releaseHeld ends a hold and wakes gate requests blocked on it.
func (s *Supervisor) releaseHeld(a *agentState) {
	if a.held {
		a.held = false
		close(a.heldCh)
		a.heldCh = nil
	}
	s.refresh(a)
}

func (a *agentState) modelName() string {
	if a.model != "" {
		return a.model
	}
	return a.cfg.Model
}

func (s *Supervisor) fleetSpent(fleetID string) float64 {
	var sum float64
	for _, a := range s.list {
		if a.cfg.FleetID == fleetID {
			sum += a.cost + a.turnEst
		}
	}
	return sum
}

// overCap reports whether the agent's own cap or its fleet's budget is used
// up, counting the turn in progress.
func (s *Supervisor) overCap(a *agentState) bool {
	if a.capped {
		return true
	}
	if c := a.cfg.CostCapUSD; c > 0 && a.cost+a.turnEst >= c {
		return true
	}
	if f := s.fleetByID(a.cfg.FleetID); f != nil && f.BudgetUSD > 0 && s.fleetSpent(f.ID) >= f.BudgetUSD {
		return true
	}
	return false
}

// ---- starting ----

// Start launches a session for the agent with prompt as its first message.
func (s *Supervisor) Start(agentID, prompt string) error {
	return s.startAgent(agentID, prompt, nil)
}

// startAgent validates under the lock, flips the agent to Starting and hands
// the slow part (history, worktree, process launch) to a goroutine, so the
// Qt thread never waits on git or a process. A failure there shows up as the
// agent's error text and a Notice, not as a return value.
func (s *Supervisor) startAgent(agentID, prompt string, task *store.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.agents[agentID]
	if a == nil {
		return errors.New("That agent no longer exists.")
	}
	if err := s.canStart(a); err != nil {
		return err
	}
	l := launch{
		a: a, backend: s.o.Backends[a.cfg.Backend], srv: s.srv,
		prev: a.base, cfg: a.cfg, workdir: s.effectiveDir(a), prompt: prompt, task: task,
	}
	if !s.spawnLocked(func() { s.launch(l) }) {
		return errors.New("Atlas Commander is closing.")
	}
	a.base = StatusStarting
	a.starting, a.cancelStart = true, false
	a.userStopped, a.capped, a.errMsg = false, false, ""
	s.refresh(a)
	return nil
}

// launch is what startAgent hands to its goroutine.
type launch struct {
	a       *agentState
	backend agent.Backend
	srv     *gate.Server
	prev    Status
	cfg     store.Agent
	workdir string
	prompt  string
	task    *store.Task
}

// failStart ends a start that didn't happen: the agent goes back to its
// earlier status, with the reason as its error text and in a Notice.
func (s *Supervisor) failStart(l launch, err error) {
	var fx effects
	s.mu.Lock()
	a := l.a
	a.starting, a.cancelStart = false, false
	a.base = l.prev
	a.errMsg = truncate(err.Error(), 300)
	s.releaseHeld(a)
	if !s.closing {
		s.addEntry(a, Entry{Kind: agent.EventError, Text: err.Error(), IsError: true})
		fx.notes = append(fx.notes, Notice{Title: "Couldn't start " + l.cfg.Name, Body: truncate(err.Error(), 200), AgentID: a.cfg.ID, Error: true})
	}
	s.mu.Unlock()
	s.run(&fx)
}

// endCancelled finishes a start the user stopped before the session existed.
func (s *Supervisor) endCancelled(l launch) {
	s.mu.Lock()
	a := l.a
	a.starting, a.cancelStart = false, false
	a.base = StatusStopped
	a.errMsg = ""
	s.releaseHeld(a)
	s.addEntry(a, Entry{Kind: agent.EventExit, Text: "stopped before it started"})
	s.mu.Unlock()
}

func (s *Supervisor) cancelled(a *agentState) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return a.cancelStart
}

func (s *Supervisor) launch(l launch) {
	a, cfg, srv := l.a, l.cfg, l.srv
	s.loadHistory(a)
	if s.cancelled(a) {
		s.endCancelled(l)
		return
	}
	dir := l.workdir
	var wtPath, wtBranch string
	if cfg.UseWorktree {
		if !worktree.IsRepo(l.workdir) {
			s.failStart(l, fmt.Errorf("Worktrees need a git repository; %s isn't one.", l.workdir))
			return
		}
		if worktree.Existing(l.workdir, cfg.WorktreePath, cfg.Branch) {
			// Keep the agent's old worktree, e.g. after a rename.
			wtPath, wtBranch = cfg.WorktreePath, cfg.Branch
		} else {
			var err error
			wtPath, wtBranch, err = worktree.Create(l.workdir, s.o.WorktreeRoot, cfg.ID, cfg.Name)
			if err != nil {
				s.failStart(l, err)
				return
			}
		}
		dir = wtPath
	}
	if s.cancelled(a) {
		s.endCancelled(l)
		return
	}
	text := l.prompt
	if cfg.PinnedPrompt != "" {
		text = cfg.PinnedPrompt + "\n\n" + l.prompt
	}
	token := srv.Register(cfg.ID)
	env := []string{gate.EnvAddr + "=" + srv.Addr(), gate.EnvToken + "=" + token, gate.EnvAgentID + "=" + cfg.ID}
	if s.o.Instance != "" {
		env = append(env, procgroup.InstanceEnv+"="+s.o.Instance)
	}
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	sess, err := l.backend.Start(ctx, agent.Spec{
		AgentID: cfg.ID, Model: cfg.Model, WorkDir: dir, Prompt: text, Env: env,
	})
	cancel()
	if err != nil {
		srv.Unregister(token)
		s.failStart(l, fmt.Errorf("Couldn't start %s: %v", cfg.Name, err))
		return
	}

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		srv.Unregister(token)
		_ = sess.Kill()
		s.failStart(l, errors.New("Atlas Commander is closing."))
		return
	}
	if a.cancelStart {
		s.mu.Unlock()
		srv.Unregister(token)
		_ = sess.Kill()
		s.endCancelled(l)
		return
	}
	if l.task != nil {
		// The task is marked only now, so a launch that fails leaves it queued.
		if err := s.claimTask(l.task, a); err != nil {
			s.mu.Unlock()
			srv.Unregister(token)
			_ = sess.Kill()
			s.failStart(l, err)
			return
		}
	}
	if wtPath != "" && (a.cfg.WorktreePath != wtPath || a.cfg.Branch != wtBranch) {
		a.cfg.WorktreePath, a.cfg.Branch = wtPath, wtBranch
		_ = s.st.UpdateAgent(a.cfg)
	}
	a.gen++
	gen := a.gen
	a.sess, a.token = sess, token
	a.starting = false
	a.outbox = make(chan string, 64)
	a.session, a.turn, a.sessionCost, a.turnEst = agent.Usage{}, agent.Usage{}, 0, 0
	// A hold requested while starting stays: a.held and its channel are kept.
	a.startedAt, a.sessionID, a.model = time.Now(), "", ""
	a.lastTool, a.lastToolAt = "", time.Time{}
	if l.task == nil {
		a.task, a.taskID = firstLine(l.prompt), ""
	} else {
		s.pushAudit(s.row(a, "task_dispatch", "", map[string]any{"task_id": l.task.ID, "title": l.task.Title}))
	}
	a.base = StatusStarting
	s.refresh(a)
	s.addEntry(a, Entry{Kind: agent.EventText, Text: truncate(l.prompt, maxDetailText), User: true})
	s.userAudit(a, "user_start", map[string]any{"text": truncate(l.prompt, maxDetailText), "workdir": dir})
	outbox := a.outbox
	s.wg.Add(2) // legal: this goroutine is itself counted, so wg is not at zero
	s.mu.Unlock()
	go s.drain(a, gen, sess, token, srv)
	go s.sender(a, sess, outbox)
}

// canStart checks the refusals that need no I/O. Call with mu held.
func (s *Supervisor) canStart(a *agentState) error {
	switch {
	case s.closing:
		return errors.New("Atlas Commander is closing.")
	case a.cfg.Archived:
		return errors.New("This agent is archived.")
	case a.status.Live() || a.sess != nil:
		return errors.New("This agent is already running.")
	case a.cfg.Backend == LegacyClaudeAPI:
		return errors.New(claudeAPIRemoved)
	case s.o.Backends[a.cfg.Backend] == nil:
		return fmt.Errorf("The %s backend isn't available. Check the setup screen.", a.cfg.Backend)
	case s.srv == nil:
		return errors.New("The approval gate isn't running, so agents can't start.")
	}
	f := s.fleetByID(a.cfg.FleetID)
	if c := a.cfg.CostCapUSD; c > 0 && a.cost >= c {
		return errors.New("This agent has reached its cost cap. Raise the cap to run it again.")
	}
	if f != nil && f.BudgetUSD > 0 && s.fleetSpent(f.ID) >= f.BudgetUSD {
		return errors.New("This fleet has reached its budget. Raise the budget to run its agents again.")
	}
	capSet := a.cfg.CostCapUSD > 0 || (f != nil && f.BudgetUSD > 0)
	// Local runs on this machine and costs nothing, so it never needs a price.
	if _, ok := s.o.Prices.Lookup(a.cfg.Model); capSet && !ok && a.cfg.Backend != agent.BackendLocal {
		return fmt.Errorf("No price is known for %s, so its cost cap can't be enforced. Add it to prices.json.", a.cfg.Model)
	}
	return nil
}

// sender delivers queued Send texts in order, so two quick redirects arrive
// as typed. It ends when the outbox is closed at session exit.
func (s *Supervisor) sender(a *agentState, sess agent.Session, outbox chan string) {
	defer s.wg.Done()
	for text := range outbox {
		if err := sess.Send(text); err != nil {
			s.mu.Lock()
			s.addEntry(a, Entry{Kind: agent.EventError, Text: "Couldn't deliver a message: " + err.Error(), IsError: true})
			s.mu.Unlock()
		}
	}
}

func (s *Supervisor) drain(a *agentState, gen int, sess agent.Session, token string, srv *gate.Server) {
	defer s.wg.Done()
	exited := false
	for ev := range sess.Events() {
		if ev.Kind == agent.EventExit {
			if !exited {
				s.onExit(a, gen, ev, token, srv)
			}
			exited = true
			continue
		}
		s.onEvent(a, ev)
	}
	if !exited {
		s.onExit(a, gen, agent.Event{Kind: agent.EventExit, Time: time.Now(), Text: "The session ended."}, token, srv)
	}
}

// ---- events ----

func (s *Supervisor) onEvent(a *agentState, ev agent.Event) {
	var fx effects
	s.mu.Lock()
	s.touch()
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	det := map[string]any{}
	var usage agent.Usage
	var cost float64
	switch ev.Kind {
	case agent.EventInit:
		if ev.SessionID != "" {
			a.sessionID = ev.SessionID
			a.cfg.SessionID = ev.SessionID
			_ = s.st.SetAgentSession(a.cfg.ID, ev.SessionID)
		}
		if ev.Model != "" {
			a.model = ev.Model
		}
		det["model"] = ev.Model
		s.addEntry(a, Entry{At: ev.Time, Kind: ev.Kind, Text: fmt.Sprintf("Session started (%s)", a.modelName())})
	case agent.EventText:
		det["text"] = truncate(ev.Text, maxDetailText)
		s.addEntry(a, Entry{At: ev.Time, Kind: ev.Kind, Text: truncate(ev.Text, maxEntryText)})
	case agent.EventToolUse:
		sum := summarize(ev.Tool, ev.ToolInput)
		det["input"] = truncate(string(ev.ToolInput), maxDetailText)
		det["tool_use_id"] = ev.ToolUseID
		a.lastTool, a.lastToolAt = ev.Tool, ev.Time
		if sum != "" {
			a.lastTool = ev.Tool + ": " + truncate(sum, 120)
		}
		s.addEntry(a, Entry{At: ev.Time, Kind: ev.Kind, Tool: ev.Tool, Text: sum})
	case agent.EventToolResult:
		det["text"] = truncate(ev.Text, maxDetailText)
		det["tool_use_id"] = ev.ToolUseID
		s.addEntry(a, Entry{At: ev.Time, Kind: ev.Kind, Tool: ev.Tool, Text: truncate(ev.Text, 2<<10), IsError: ev.IsError})
	case agent.EventUsage:
		usage = ev.Usage
		a.session = a.session.Add(ev.Usage)
		a.turn = a.turn.Add(ev.Usage)
		a.total = a.total.Add(ev.Usage)
		a.burnTotal += ev.Usage.Input + ev.Usage.Output + ev.Usage.CacheWrite5m + ev.Usage.CacheWrite1h
		est, _ := s.o.Prices.Cost(a.modelName(), a.turn)
		s.costFlow += est - a.turnEst
		s.spent += est - a.turnEst
		a.turnEst = est
		s.checkCaps(a, &fx)
	case agent.EventResult:
		cost = s.settleTurn(a, ev.CostUSD)
		a.latency = ev.Duration
		det["text"] = truncate(ev.Text, maxDetailText)
		det["duration_ms"] = ev.Duration.Milliseconds()
		det["turns"] = ev.Turns
		text := resultText(cost, ev.Duration, ev.IsError, ev.Text)
		s.addEntry(a, Entry{At: ev.Time, Kind: ev.Kind, Text: text, IsError: ev.IsError})
		if ev.IsError {
			a.errMsg = truncate(ev.Text, 300)
		} else {
			a.errMsg = ""
		}
		s.finishTask(a, ev)
		s.checkCaps(a, &fx)
	case agent.EventError:
		det["text"] = truncate(ev.Text, maxDetailText)
		s.addEntry(a, Entry{At: ev.Time, Kind: ev.Kind, Text: ev.Text, IsError: true})
	}
	// Status: the first event means the process is up; new work after a
	// finished turn means it is running again. Capped stays capped.
	switch ev.Kind {
	case agent.EventResult:
		if a.base.Live() {
			a.base = StatusWaiting
		}
	case agent.EventError:
	default:
		if a.base == StatusStarting || a.base == StatusWaiting {
			a.base = StatusRunning
		}
	}
	s.refresh(a)
	row := s.row(a, ev.Kind.String(), ev.Tool, det)
	row.At, row.IsError, row.Usage, row.CostUSD = ev.Time, ev.IsError, usage, cost
	s.pushAudit(row)
	s.mu.Unlock()
	s.run(&fx)
}

// settleTurn closes the turn: its cost is what the backend reported, or our
// estimate from the tokens. The cost lands in the session and all-time sums
// exactly once, and the returned value goes on exactly one audit row.
func (s *Supervisor) settleTurn(a *agentState, reported float64) float64 {
	cost := reported
	if cost <= 0 {
		cost, _ = s.o.Prices.Cost(a.modelName(), a.turn)
	}
	a.sessionCost += cost
	a.cost += cost
	s.costFlow += cost - a.turnEst
	s.spent += cost - a.turnEst
	a.turn, a.turnEst = agent.Usage{}, 0
	return cost
}

func resultText(cost float64, d time.Duration, isErr bool, text string) string {
	if isErr {
		if text == "" {
			text = "The turn failed."
		}
		return "Turn failed · " + truncate(text, 500)
	}
	out := "Turn finished"
	if cost > 0 {
		out += fmt.Sprintf(" · $%.4f", cost)
	}
	if d > 0 {
		out += fmt.Sprintf(" · %.1fs", d.Seconds())
	}
	return out
}

// checkCaps stops the agent, or every live agent of its fleet, when a cap or
// the fleet budget is used up. Call with mu held.
func (s *Supervisor) checkCaps(a *agentState, fx *effects) {
	if c := a.cfg.CostCapUSD; c > 0 && a.cost+a.turnEst >= c {
		s.capAgent(a, fx, fmt.Sprintf("%s reached its cost cap of $%.2f and was stopped.", a.cfg.Name, c))
	}
	f := s.fleetByID(a.cfg.FleetID)
	if f == nil || f.BudgetUSD <= 0 || s.fleetSpent(f.ID) < f.BudgetUSD {
		return
	}
	for _, b := range s.list {
		if b.cfg.FleetID == f.ID {
			s.capAgent(b, fx, fmt.Sprintf("Fleet %s reached its budget of $%.2f; %s was stopped.", f.Name, f.BudgetUSD, b.cfg.Name))
		}
	}
}

func (s *Supervisor) capAgent(a *agentState, fx *effects, msg string) {
	if a.capped || a.sess == nil || !a.base.Live() {
		return
	}
	a.capped = true
	a.base = StatusCapped
	a.errMsg = "Stopped at its cost cap."
	s.releaseHeld(a)
	s.pushAudit(s.row(a, "cap_reached", "", map[string]any{"text": msg, "cost_usd": a.cost + a.turnEst}))
	s.addEntry(a, Entry{Kind: agent.EventError, Text: msg, IsError: true})
	fx.notes = append(fx.notes, Notice{Title: "Cost cap reached", Body: msg, AgentID: a.cfg.ID, Error: true})
	fx.stops = append(fx.stops, a.sess)
}

func (s *Supervisor) onExit(a *agentState, gen int, ev agent.Event, token string, srv *gate.Server) {
	var fx effects
	s.mu.Lock()
	s.touch()
	if a.gen != gen {
		s.mu.Unlock()
		return
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	var cost float64
	if !a.turn.IsZero() {
		cost = s.settleTurn(a, 0) // killed mid-turn: no Result will carry it
	}
	final := StatusStopped
	switch {
	case a.capped:
		// Checked before userStopped: a Stop after a cap hit keeps the reason.
		final = StatusCapped
	case a.userStopped:
	case ev.IsError:
		final = StatusError
	}
	reason := ev.Text
	if reason == "" {
		reason = "stopped"
	}
	if final == StatusError {
		a.errMsg = reason
		fx.notes = append(fx.notes, Notice{Title: a.cfg.Name + " stopped with an error", Body: truncate(reason, 200), AgentID: a.cfg.ID, Error: true})
	} else if final == StatusStopped {
		a.errMsg = ""
	}
	if a.taskID != "" {
		// A conflict means the user cancelled it meanwhile; that stands.
		_ = s.st.SetTaskStatus(a.taskID, store.TaskFailed, "The agent stopped before finishing: "+truncate(reason, 500))
		a.taskID = ""
		s.touchTasks()
	}
	s.addEntry(a, Entry{At: ev.Time, Kind: agent.EventExit, Text: reason, IsError: ev.IsError})
	row := s.row(a, agent.EventExit.String(), "", map[string]any{"text": truncate(ev.Text, maxDetailText)})
	row.At, row.IsError, row.CostUSD = ev.Time, ev.IsError, cost
	s.pushAudit(row)
	a.sess, a.token = nil, ""
	if a.outbox != nil {
		close(a.outbox)
		a.outbox = nil
	}
	a.base = final
	s.releaseHeld(a)
	s.mu.Unlock()
	srv.Unregister(token)
	s.run(&fx)
}

// ---- controls ----

// Send queues text for a running session; Claude Code delivers it at the next
// safe point of the current turn, which is how a redirect works.
func (s *Supervisor) Send(agentID, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.agents[agentID]
	if a == nil || a.sess == nil || !a.status.Live() || a.outbox == nil {
		return errors.New("This agent isn't running. Start it first.")
	}
	return s.sendLocked(a, text)
}

func (s *Supervisor) sendLocked(a *agentState, text string) error {
	select {
	case a.outbox <- text:
	default:
		return errors.New("Too many messages are waiting for this agent. Try again in a moment.")
	}
	s.addEntry(a, Entry{Kind: agent.EventText, Text: truncate(text, maxDetailText), User: true})
	s.userAudit(a, "user_send", map[string]any{"text": truncate(text, maxDetailText)})
	if a.base == StatusWaiting {
		a.base = StatusRunning
	}
	s.refresh(a)
	return nil
}

func (s *Supervisor) liveAgent(id string) (*agentState, error) {
	a := s.agents[id]
	if a == nil {
		return nil, errors.New("That agent no longer exists.")
	}
	if !a.status.Live() {
		return nil, errors.New("This agent isn't running.")
	}
	return a, nil
}

// Hold makes the agent's next tool request wait until Resume.
func (s *Supervisor) Hold(agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.liveAgent(agentID)
	if err != nil {
		return err
	}
	if a.held {
		return nil
	}
	a.held, a.heldCh = true, make(chan struct{})
	s.userAudit(a, "user_hold", nil)
	s.refresh(a)
	return nil
}

// Resume lets a held agent's waiting requests go on.
func (s *Supervisor) Resume(agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.liveAgent(agentID)
	if err != nil {
		return err
	}
	if a.held {
		s.releaseHeld(a)
		s.userAudit(a, "user_resume", nil)
	}
	return nil
}

func (s *Supervisor) endSession(agentID string, kill bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("Atlas Commander is closing.")
	}
	a := s.agents[agentID]
	if a == nil || (a.sess == nil && !a.starting) {
		return errors.New("This agent isn't running.")
	}
	kind := "user_stop"
	if kill {
		kind = "user_kill"
	}
	if a.sess == nil {
		// Still starting: launch notices the flag once the process exists and
		// ends the session itself.
		a.cancelStart = true
		s.releaseHeld(a)
		s.userAudit(a, kind, nil)
		return nil
	}
	a.userStopped = true
	s.releaseHeld(a)
	sess := a.sess
	s.userAudit(a, kind, nil)
	s.spawnLocked(func() {
		if kill {
			_ = sess.Kill()
		} else {
			_ = sess.Stop()
		}
	})
	return nil
}

// Stop ends the session politely, without waiting for it here.
func (s *Supervisor) Stop(agentID string) error { return s.endSession(agentID, false) }

// Kill ends the session and everything it started, at once.
func (s *Supervisor) Kill(agentID string) error { return s.endSession(agentID, true) }

// KillAll kills every live session, and cancels every start in flight, and
// returns how many agents that was.
func (s *Supervisor) KillAll() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return 0
	}
	n := 0
	for _, a := range s.list {
		switch {
		case a.sess != nil:
			sess := a.sess
			a.userStopped = true
			s.releaseHeld(a)
			s.userAudit(a, "user_kill", map[string]any{"all": true})
			s.spawnLocked(func() { _ = sess.Kill() })
			n++
		case a.starting:
			a.cancelStart = true
			s.releaseHeld(a)
			s.userAudit(a, "user_kill", map[string]any{"all": true})
			n++
		}
	}
	return n
}
