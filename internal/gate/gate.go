// Package gate is the local channel between the hook helper (cmd/atlas-hook)
// and Commander. The helper asks "may this tool call run?" and waits for the
// answer; a second Commander launch uses the same socket to ask the first to
// show its window.
//
// The transport is an AF_UNIX socket on both Linux and Windows 10 1803+, via
// Go's net package, so no named-pipe library is needed. Access is limited by
// the 0700 directory holding the socket and by a per-agent token.
//
// The socket directory must be private to the user. On Linux Listen sets it to
// mode 0700. On Windows chmod does nothing, so the directory must already be
// per-user: paths.Runtime sits under the user's own temp directory there, and
// that is what makes the socket unreachable by other users.
//
// Goroutine model: Listen starts one accept goroutine; each connection gets
// one goroutine, plus one watcher while a decision is pending so a dropped
// hook cancels the Decider's context. The Decider is called from those
// goroutines and must be safe for concurrent use.
package gate

import (
	"encoding/json"
	"errors"
	"time"
)

// Environment variables Commander sets in every agent so the hook helper,
// which inherits the agent's environment, can find its way back.
const (
	EnvAddr    = "ATLAS_GATE_ADDR"
	EnvToken   = "ATLAS_GATE_TOKEN"
	EnvAgentID = "ATLAS_AGENT_ID"
)

// HookTimeout is the timeout Commander writes into the Claude Code hook
// settings. Claude Code treats a timed-out hook as non-blocking and runs the
// tool anyway, so it must be longer than any wait the helper allows: the
// helper gives up and denies one minute earlier (see cmd/atlas-hook).
const HookTimeout = 24 * time.Hour

// ErrRunning means another Commander already answers on the socket.
var ErrRunning = errors.New("Atlas Commander is already running")

// MaxRequest bounds the hook input the helper forwards (16 MiB), so a bad
// client can't exhaust memory. The helper reads at most this much from
// Claude Code; the server accepts a little more to allow for the envelope.
const MaxRequest = 16 << 20

// maxLine is the longest request line the server reads: the hook input plus
// room for the JSON envelope around it.
const maxLine = MaxRequest + 64<<10

// maxConns caps concurrent connections; extras are closed at once.
const maxConns = 64

// closeWait bounds how long Close waits for handlers to finish.
const closeWait = 5 * time.Second

// readTimeout bounds only the time to receive the request line, not the
// decision wait, which can take as long as the user needs.
const readTimeout = 10 * time.Second

// Request describes one tool call awaiting approval.
type Request struct {
	AgentID   string          `json:"agent_id"`
	SessionID string          `json:"session_id"`
	Tool      string          `json:"tool"`
	ToolUseID string          `json:"tool_use_id"`
	Cwd       string          `json:"cwd"`
	Input     json.RawMessage `json:"input,omitempty"`
	// Token is the session token the request came with, set by the server
	// (never sent): a decider uses it to tell one session from the next.
	Token string `json:"-"`
}

// Decision is the answer to a Request.
type Decision struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason,omitempty"`
}

type message struct {
	Op      string   `json:"op"`
	Token   string   `json:"token,omitempty"`
	Request *Request `json:"request,omitempty"`
}

type reply struct {
	OK     bool   `json:"ok,omitempty"`
	Allow  bool   `json:"allow"`
	Reason string `json:"reason,omitempty"`
}
