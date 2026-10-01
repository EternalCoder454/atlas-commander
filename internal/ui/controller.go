package ui

import (
	"time"

	"atlas-commander/internal/fleet"
	"atlas-commander/internal/observed"
	"atlas-commander/internal/store"
)

// Controller is what the UI needs from the fleet supervisor. The UI never
// touches processes, the gate or the database itself: it reads snapshots on
// its timer and calls these methods from button handlers. Every method must
// return quickly when called on the Qt thread; anything slow (stopping a
// process, waiting on a session) happens on the supervisor's goroutines.
// Errors are plain sentences the UI shows as they are.
type Controller interface {
	Snapshot() *fleet.Snapshot
	// Transcript returns the agent's entries from index from on, and the
	// index to pass next time. The detail view polls it while open.
	Transcript(agentID string, from int) ([]fleet.Entry, int)

	// Manage.
	Start(agentID, prompt string) error
	// Send re-prompts an agent waiting for input, or, while a turn runs,
	// queues the text so it arrives before the agent's next step (redirect).
	Send(agentID, text string) error
	Hold(agentID string) error
	Resume(agentID string) error
	Stop(agentID string) error
	Kill(agentID string) error
	KillAll() int

	// Govern.
	Decide(approvalID string, allow bool, reason string) error

	// Fleets and agents.
	CreateFleet(c fleet.FleetConfig) (string, error)
	UpdateFleet(id string, c fleet.FleetConfig) error
	DeleteFleet(id string) error
	RegisterAgent(c fleet.AgentConfig) (string, error)
	UpdateAgent(id string, c fleet.AgentConfig) error
	AgentConfig(id string) (fleet.AgentConfig, error)
	ArchiveAgent(id string) error

	// Orchestrate (manual dispatch only).
	AddTask(c fleet.TaskConfig) (string, error)
	UpdateTask(id string, c fleet.TaskConfig) error
	CancelTask(id string) error
	Dispatch(taskID, agentID string) error

	// Analyze: queries on the audit log.
	Totals(f store.Filter) (store.Totals, error)
	AgentStats(f store.Filter) ([]store.AgentStats, error)
	SessionStats(f store.Filter) ([]store.SessionStats, error)
	CostSeries(f store.Filter, bucket time.Duration) ([]store.CostPoint, error)
	Audit(f store.Filter, limit int) ([]store.AuditEntry, error)

	// Observed mode: read-only views of sessions started outside Commander.
	ObservedSessions() ([]observed.Session, error)
	ObservedTranscript(path string) ([]observed.Line, error)

	Setup() fleet.SetupInfo
	SetNotifier(func(fleet.Notice))
}
