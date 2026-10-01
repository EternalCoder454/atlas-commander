package fleet

import (
	"time"

	"atlas-commander/internal/agent"
)

// Status is an agent's state as the board shows it. The supervisor owns the
// transitions; the UI only reads them.
type Status string

const (
	StatusIdle     Status = "idle"     // registered, no session running
	StatusStarting Status = "starting" // process launched, no init yet
	StatusRunning  Status = "running"  // a turn is in progress
	StatusWaiting  Status = "waiting"  // session alive, turn finished, waiting for input
	StatusApproval Status = "approval" // blocked on a gate decision
	StatusHeld     Status = "held"     // will stop at (or is stopped at) the next tool call
	StatusCapped   Status = "capped"   // stopped by its own or its fleet's cost cap
	StatusError    Status = "error"    // the last session ended badly
	StatusStopped  Status = "stopped"  // stopped by the user
)

// Tone is the colour family a status is drawn in: one accent per state.
type Tone int

const (
	ToneIdle Tone = iota
	ToneOK
	ToneWarn
	ToneError
)

func (s Status) Tone() Tone {
	switch s {
	case StatusRunning, StatusWaiting, StatusStarting:
		return ToneOK
	case StatusApproval, StatusHeld, StatusCapped:
		return ToneWarn
	case StatusError:
		return ToneError
	}
	return ToneIdle
}

// Live reports whether the agent has a session process or stream open.
func (s Status) Live() bool {
	switch s {
	case StatusStarting, StatusRunning, StatusWaiting, StatusApproval, StatusHeld:
		return true
	}
	return false
}

// Snapshot is an immutable copy of everything the UI shows. The supervisor
// builds a new one whenever state changes and the UI timer picks up the
// latest; nothing in it is shared with the supervisor afterwards.
type Snapshot struct {
	Seq       uint64 // increases on every change; the UI skips equal Seq
	Fleets    []FleetView
	Agents    []AgentView
	Approvals []ApprovalView
	Tasks     []TaskView
	// Burn is fleet-wide tokens per second, one sample per second, oldest
	// first; CostRate is USD per minute on the same clock.
	Burn     []float64
	CostRate []float64
	Spent    float64 // all fleets, since Commander started
}

type FleetView struct {
	ID, Name, WorkDir string
	BudgetUSD         float64 // 0 = none
	SpentUSD          float64 // from the audit log, all time
	Agents, Live      int
}

type AgentView struct {
	ID, Name             string
	FleetID, FleetName   string
	Backend, Model       string
	WorkDir              string
	Worktree, Branch     string // empty when the agent works in WorkDir directly
	PinnedPrompt         string
	UseWorktree          bool
	Status               Status
	Task                 string // current task title, or the last prompt's first line
	TaskID               string
	LastTool             string // e.g. "Bash: go test ./..."
	LastToolAt           time.Time
	Latency              time.Duration // the last turn's duration
	Session              agent.Usage   // this session
	Total                agent.Usage   // all sessions, from the audit log
	SessionCost, CostUSD float64       // CostUSD is all-time
	CapUSD               float64       // 0 = none
	Error                string
	StartedAt            time.Time
	SessionID            string
	Burn                 []float64 // tokens per second, last 60 s
	Pending              int       // approvals waiting on this agent
	Archived             bool
}

type ApprovalView struct {
	ID        string
	AgentID   string
	AgentName string
	Tool      string
	Input     string // pretty-printed tool input
	Summary   string // one line: the command, path or pattern
	At        time.Time
}

type TaskView struct {
	ID, FleetID, Title, Prompt string
	Priority                   int
	Status                     string // queued, running, done, failed, cancelled
	Ready                      bool   // queued and every dependency is done
	AgentID, AgentName         string
	DependsOn                  []string
	Created                    time.Time
}

// Entry is one line of an agent's transcript as the detail view shows it.
type Entry struct {
	At      time.Time
	Kind    agent.EventKind
	Text    string
	Tool    string
	IsError bool
	User    bool // text the user sent (prompt, redirect)
}

// FleetConfig is what the user edits about a fleet.
type FleetConfig struct {
	Name      string
	WorkDir   string
	BudgetUSD float64 // 0 = none; all-time spend across the fleet's agents
}

// AgentConfig is the registration form. Approve lists the tools that need a
// human decision before they run ("*" for every tool); an empty list lets
// every tool run.
type AgentConfig struct {
	FleetID      string
	Name         string
	Backend      string // one of the agent.Backend* names
	Model        string
	WorkDir      string // "" uses the fleet's
	PinnedPrompt string // sent at the start of every session
	CostCapUSD   float64
	UseWorktree  bool
	Approve      []string
}

// DefaultApprove is the gate policy a new agent starts with: anything that
// changes files or runs commands asks first.
var DefaultApprove = []string{"Bash", "Write", "Edit", "MultiEdit", "NotebookEdit"}

// TaskConfig is a task as the user enters it.
type TaskConfig struct {
	FleetID   string
	Title     string
	Prompt    string
	Priority  int // higher runs first
	DependsOn []string
}

// Notice is something worth a desktop notification.
type Notice struct {
	Title, Body string
	AgentID     string
	Error       bool
}

// SetupInfo describes what is installed, for the setup screen.
type SetupInfo struct {
	ClaudePath    string
	ClaudeVersion string
	Problems      []string // plain sentences; empty means Claude Code is usable

	// Chat providers. The key itself is never exposed, only whether the
	// variable is set.
	OpenAIKeyEnv string
	OpenAIKeySet bool
	GeminiKeyEnv string
	GeminiKeySet bool
	// OllamaURL is where Local looks for Ollama. OllamaChecked is false until
	// the first probe has finished; OllamaModels counts what it offers.
	OllamaURL       string
	OllamaChecked   bool
	OllamaReachable bool
	OllamaModels    int
}

// LegacyClaudeAPI is the backend name of the removed "Claude API" provider.
// Agents stored with it are kept, but cannot start.
const LegacyClaudeAPI = "claude-api"

const claudeAPIRemoved = "Claude API was removed. Edit this agent to pick another provider."
