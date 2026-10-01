// Package agent defines the contract between Commander and the things that
// actually run a model: a Backend starts Sessions, and a Session reports what
// happens as a stream of Events.
//
// The fleet supervisor and the UI only ever see these types. Claude Code and
// the raw Claude API are the v1 backends (packages claudecode and claudeapi);
// another provider can be added later by implementing Backend, without the UI
// changing. The interface stays internal until the v2 plugin API.
package agent

import (
	"context"
	"encoding/json"
	"time"
)

// Backend names, stored in the database and shown in the registration form.
// Never rename one: existing agents would lose their backend.
const (
	BackendClaudeCode = "claude-code"
	BackendClaudeAPI  = "claude-api"
)

// Backend starts sessions. Implementations must be safe for concurrent use;
// the supervisor starts many agents from many goroutines.
type Backend interface {
	// Name is one of the Backend* constants.
	Name() string
	// Start launches a session. It returns once the session is running (the
	// process has started, or the API client is ready); the first Event may
	// arrive later. ctx bounds only the start itself, not the session.
	Start(ctx context.Context, spec Spec) (Session, error)
}

// Spec is everything a backend needs to start one session.
type Spec struct {
	AgentID string // Commander's agent id, passed to the gate hook
	Model   string // full id or alias ("opus", "claude-sonnet-5-5"); "" = backend default
	WorkDir string // absolute; the agent's own directory or git worktree

	// SystemPrompt is the agent's pinned prompt. Claude Code appends it to
	// its own system prompt; the API backend sends it as the system prompt.
	SystemPrompt string

	// Prompt is the first user message. Empty starts the session idle,
	// waiting for Send.
	Prompt string

	// ResumeID continues an earlier backend session (Claude Code's session
	// id) so a restart of Commander does not lose the conversation.
	ResumeID string

	// Env is extra environment for the agent process, in "KEY=value" form.
	// The supervisor puts the gate address and per-session token here.
	Env []string
}

// Session is one running conversation with one agent.
//
// Send, Stop and Kill may be called from any goroutine. Events is closed
// after the final EventExit, and only then; a consumer can range over it.
type Session interface {
	// Send delivers a user message: a new task, or a redirect mid-task.
	// Claude Code queues it until the current turn reaches a safe point.
	Send(text string) error
	// Stop ends the session politely (close input, give the process a
	// moment), then forcefully. It returns once the session has exited.
	Stop() error
	// Kill ends the session and every process it started, at once.
	Kill() error
	// Events is the session's event stream. It is buffered; a slow reader
	// delays the session but never loses events.
	Events() <-chan Event
}

// EventKind says which fields of an Event are set.
type EventKind int

const (
	// EventInit: the session is up. SessionID and Model are set.
	EventInit EventKind = iota
	// EventText: assistant text. Text is set.
	EventText
	// EventToolUse: the model asked for a tool. Tool, ToolInput, ToolUseID.
	EventToolUse
	// EventToolResult: a tool finished. ToolUseID, IsError, Text (truncated).
	EventToolResult
	// EventUsage: tokens were spent. Usage holds the increment for this
	// event only, never a running total, so the supervisor can sum them.
	EventUsage
	// EventResult: a turn ended. IsError, Text (the final answer or the
	// error), CostUSD (as the backend reports it, 0 if it doesn't),
	// Duration, Turns.
	EventResult
	// EventError: something went wrong that did not end the session
	// (rate limit, transient API error). Text is set.
	EventError
	// EventExit: the session is over. IsError and Text describe why
	// (exit code, signal, or "stopped").
	EventExit
)

// String is the name written to the audit log. Never rename one.
func (k EventKind) String() string {
	switch k {
	case EventInit:
		return "init"
	case EventText:
		return "text"
	case EventToolUse:
		return "tool_use"
	case EventToolResult:
		return "tool_result"
	case EventUsage:
		return "usage"
	case EventResult:
		return "result"
	case EventError:
		return "error"
	case EventExit:
		return "exit"
	}
	return "unknown"
}

// Event is one thing that happened in a session.
type Event struct {
	Kind EventKind
	Time time.Time

	SessionID string
	Model     string

	Text      string
	Tool      string
	ToolInput json.RawMessage
	ToolUseID string
	IsError   bool

	Usage    Usage
	CostUSD  float64
	Duration time.Duration
	Turns    int
}

// Usage counts tokens. Cache writes are split by TTL because they are priced
// differently (1.25x and 2x input).
type Usage struct {
	Input        int64 `json:"input"`
	Output       int64 `json:"output"`
	CacheRead    int64 `json:"cache_read"`
	CacheWrite5m int64 `json:"cache_write_5m"`
	CacheWrite1h int64 `json:"cache_write_1h"`
}

// Add returns u + v.
func (u Usage) Add(v Usage) Usage {
	return Usage{
		Input:        u.Input + v.Input,
		Output:       u.Output + v.Output,
		CacheRead:    u.CacheRead + v.CacheRead,
		CacheWrite5m: u.CacheWrite5m + v.CacheWrite5m,
		CacheWrite1h: u.CacheWrite1h + v.CacheWrite1h,
	}
}

// Total is every token the model read or wrote.
func (u Usage) Total() int64 {
	return u.Input + u.Output + u.CacheRead + u.CacheWrite5m + u.CacheWrite1h
}

// IsZero reports whether no tokens were counted.
func (u Usage) IsZero() bool { return u == Usage{} }
