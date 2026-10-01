package fleet

import (
	"context"
	"encoding/json"
	"errors"

	"atlas-commander/internal/gate"
	"atlas-commander/internal/store"
)

const (
	capDenied     = "This agent's cost cap has been reached."
	defaultDenial = "Denied in Atlas Commander."
)

func deny(reason string) gate.Decision { return gate.Decision{Reason: reason} }

// parseApprove reads the gate policy JSON. A policy that can't be read falls
// back to the default rather than letting everything through.
func parseApprove(policy string) []string {
	var p struct {
		Approve []string `json:"approve"`
	}
	if err := json.Unmarshal([]byte(policy), &p); err != nil {
		_ = json.Unmarshal([]byte(store.DefaultGatePolicy), &p)
	}
	return p.Approve
}

func needsApproval(approve []string, tool string) bool {
	for _, t := range approve {
		if t == "*" || t == tool {
			return true
		}
	}
	return false
}

// Gate is the gate.Decider: main passes it to gate.Listen. (It is not named
// Decide because ui.Controller already uses that name for answering an
// approval.) It may block for as long as a hold or a human takes; ctx ends
// when the hook goes away.
func (s *Supervisor) Gate(ctx context.Context, r gate.Request) gate.Decision {
	s.mu.Lock()
	a := s.agents[r.AgentID]
	if a == nil {
		s.mu.Unlock()
		return deny("Unknown agent.")
	}
	// A request that waited through a hold is denied if its session ended or
	// was replaced meanwhile, which shows as a.token no longer being the one
	// it came with. After a Stop the exit can be handled before this wakes,
	// and a restarted agent must not run an old session's call. A request
	// that came while a start was in flight, before a.token was set, is from
	// the new process and is let through while that start lasts.
	waited, early := false, a.token == ""
	for {
		if s.overCap(a) {
			s.mu.Unlock()
			return deny(capDenied)
		}
		if (a.userStopped && a.sess != nil) || (waited && a.token != r.Token && !(early && a.starting)) {
			s.mu.Unlock()
			return deny("This agent was stopped.")
		}
		if !a.held {
			break
		}
		ch := a.heldCh
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return deny("No answer")
		}
		s.mu.Lock()
		waited = true
	}
	if !needsApproval(a.approve, r.Tool) {
		s.mu.Unlock()
		return gate.Decision{Allow: true}
	}
	agentID, agentName := a.cfg.ID, a.cfg.Name
	session := r.SessionID
	if session == "" {
		session = a.sessionID
	}
	s.mu.Unlock()

	row := store.Approval{AgentID: agentID, SessionID: session, Tool: r.Tool, Input: prettyJSON(r.Input)}
	if err := s.st.AddApproval(&row); err != nil {
		return deny("Atlas Commander couldn't record the approval request.")
	}
	p := &pendingApproval{
		id: row.ID, agentID: agentID, tool: r.Tool, input: row.Input,
		summary: summarize(r.Tool, r.Input), at: row.RequestedAt, ch: make(chan gate.Decision, 1),
	}
	s.mu.Lock()
	a = s.agents[agentID]
	if a == nil || s.closing {
		s.mu.Unlock()
		_ = s.st.DecideApproval(row.ID, store.DecisionDeny, "Commander closed")
		return deny("Atlas Commander is closing.")
	}
	s.pending[p.id] = p
	a.pending++
	s.refresh(a)
	notifier := s.notifier
	s.mu.Unlock()
	if notifier != nil {
		notifier(Notice{Title: "Approval needed", Body: agentName + " wants to run " + r.Tool, AgentID: agentID})
	}

	select {
	case d := <-p.ch:
		return d
	case <-ctx.Done():
		// The hook is gone. Record the denial, unless the user answered in
		// the same instant, in which case that answer is already waiting.
		_ = s.decide(p.id, false, "No answer", true)
		return <-p.ch
	}
}

// Decide answers a pending approval from the UI.
func (s *Supervisor) Decide(approvalID string, allow bool, reason string) error {
	if !allow && reason == "" {
		reason = defaultDenial
	}
	return s.decide(approvalID, allow, reason, false)
}

// decide records the answer and wakes the waiting Gate call, exactly once. With
// force set, a failed database write still releases the waiter (as a denial),
// because a stuck hook is worse than a missing row.
func (s *Supervisor) decide(id string, allow bool, reason string, force bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pending[id]
	if p == nil {
		return errors.New("That request was already answered.")
	}
	decision := store.DecisionDeny
	if allow {
		decision = store.DecisionAllow
	}
	if err := s.st.DecideApproval(id, decision, reason); err != nil {
		if !force {
			return errors.New("Couldn't save the decision. Try again.")
		}
		allow = false
	}
	delete(s.pending, id)
	if a := s.agents[p.agentID]; a != nil {
		if a.pending > 0 {
			a.pending--
		}
		s.refresh(a)
	}
	s.touch()
	p.ch <- gate.Decision{Allow: allow, Reason: reason}
	return nil
}
