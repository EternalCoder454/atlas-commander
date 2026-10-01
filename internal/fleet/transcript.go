package fleet

import (
	"encoding/json"
	"time"

	"atlas-commander/internal/agent"
	"atlas-commander/internal/observed"
	"atlas-commander/internal/store"
)

const (
	maxEntryText   = 32 << 10 // one transcript entry's text, to bound memory
	historyEntries = 500      // audit rows read to rebuild a transcript
)

// addEntry appends to the agent's transcript, dropping the oldest quarter once
// it is a quarter over the cap. trOff counts the dropped ones so indices stay stable for pollers.
// Call with mu held.
func (s *Supervisor) addEntry(a *agentState, e Entry) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	a.loaded = true
	a.tr = append(a.tr, e)
	if len(a.tr) > maxTranscript+maxTranscript/4 {
		// Drop a chunk at once, so the copy happens once per quarter-cap of
		// entries instead of on every event. The copy (rather than a
		// reslice) lets the dropped entries be freed.
		over := maxTranscript / 4
		a.tr = append([]Entry(nil), a.tr[over:]...)
		a.trOff += over
	}
}

// Transcript returns the entries from index from on and the index to ask for
// next. An index older than what is kept starts at the oldest kept entry. An
// agent whose history isn't loaded yet returns what is there (possibly
// nothing) and fills in from the audit log in the background; the next poll
// sees it.
func (s *Supervisor) Transcript(agentID string, from int) ([]Entry, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.agents[agentID]
	if a == nil {
		return nil, from
	}
	if !a.loaded && !a.loading {
		s.spawnLocked(func() { s.loadHistory(a) })
	}
	start := from - a.trOff
	if start < 0 {
		start = 0
	}
	if start > len(a.tr) {
		start = len(a.tr)
	}
	return append([]Entry(nil), a.tr[start:]...), a.trOff + len(a.tr)
}

// loadHistory fills an empty transcript, once, from the agent's last audit
// rows, so a restart doesn't blank the detail view. It queries without the
// lock and must not run on the Qt thread.
func (s *Supervisor) loadHistory(a *agentState) {
	s.mu.Lock()
	if a.loaded || a.loading || len(a.tr) != 0 {
		s.mu.Unlock()
		return
	}
	a.loading = true
	id := a.cfg.ID
	s.mu.Unlock()
	rows, _ := s.st.AuditEntries(store.Filter{AgentID: id}, historyEntries)
	var entries []Entry
	for i := len(rows) - 1; i >= 0; i-- { // newest first from the store
		if e, ok := entryFromAudit(rows[i]); ok {
			entries = append(entries, e)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a.loading = false
	if !a.loaded && len(a.tr) == 0 {
		a.tr = entries
	}
	a.loaded = true
	s.touch()
}

func entryFromAudit(r store.AuditEntry) (Entry, bool) {
	var d struct {
		Text       string `json:"text"`
		Input      string `json:"input"`
		Model      string `json:"model"`
		DurationMS int64  `json:"duration_ms"`
	}
	_ = json.Unmarshal([]byte(r.Detail), &d)
	e := Entry{At: r.At, Tool: r.Tool, IsError: r.IsError}
	switch r.Kind {
	case "init":
		e.Kind, e.Text = agent.EventInit, "Session started ("+d.Model+")"
	case "text":
		e.Kind, e.Text = agent.EventText, d.Text
	case "user_start", "user_send":
		e.Kind, e.Text, e.User = agent.EventText, d.Text, true
	case "tool_use":
		e.Kind = agent.EventToolUse
		var in json.RawMessage
		if json.Valid([]byte(d.Input)) {
			in = json.RawMessage(d.Input)
		}
		e.Text = summarize(r.Tool, in)
	case "tool_result":
		e.Kind, e.Text = agent.EventToolResult, truncate(d.Text, 2<<10)
	case "result":
		e.Kind = agent.EventResult
		e.Text = resultText(r.CostUSD, time.Duration(d.DurationMS)*time.Millisecond, r.IsError, d.Text)
	case "error":
		e.Kind, e.Text = agent.EventError, d.Text
	case "exit":
		e.Kind, e.Text = agent.EventExit, d.Text
		if e.Text == "" {
			e.Text = "stopped"
		}
	default:
		return Entry{}, false
	}
	return e, true
}

// ObservedSessions lists Claude Code sessions started outside Commander.
func (s *Supervisor) ObservedSessions() ([]observed.Session, error) {
	return observed.List(observed.Root(), 200)
}

// ObservedTranscript reads one such session's transcript.
func (s *Supervisor) ObservedTranscript(path string) ([]observed.Line, error) {
	return observed.Read(path, 2000)
}
