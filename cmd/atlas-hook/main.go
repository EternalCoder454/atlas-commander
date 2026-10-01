// Command atlas-hook is the PreToolUse hook helper Claude Code runs before
// each tool call. It forwards the call to Commander over the gate socket,
// waits for the decision and prints it in Claude Code's hook format.
//
// It fails closed: if anything is missing or Commander can't be reached, the
// tool call is denied with a plain-sentence reason. It always exits 0, since
// a non-zero exit would be treated differently by Claude Code.
//
// Single goroutine, no platform-specific code. Kept tiny on purpose: it
// starts once per tool call, so it imports no Qt and no sqlite.
package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"time"

	"atlas-commander/internal/gate"
)

type hookInput struct {
	SessionID string          `json:"session_id"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	ToolUseID string          `json:"tool_use_id"`
	Cwd       string          `json:"cwd"`
}

type hookOutput struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

// decide runs the whole helper against the given streams and environment so
// tests can call it too.
func decide(stdin io.Reader, getenv func(string) string, timeout time.Duration) gate.Decision {
	addr, token, agent := getenv(gate.EnvAddr), getenv(gate.EnvToken), getenv(gate.EnvAgentID)
	if addr == "" || token == "" || agent == "" {
		return gate.Decision{Reason: "Atlas Commander didn't start this agent, so this tool call was blocked."}
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, gate.MaxRequest+1))
	if err == nil && len(raw) > gate.MaxRequest {
		return gate.Decision{Reason: "This tool call is too large for Commander to review, so it was blocked."}
	}
	var in hookInput
	if err != nil || json.Unmarshal(raw, &in) != nil {
		return gate.Decision{Reason: "The tool call details were unreadable, so it was blocked."}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	d, err := gate.Ask(ctx, addr, token, gate.Request{
		AgentID: agent, SessionID: in.SessionID, Tool: in.ToolName,
		ToolUseID: in.ToolUseID, Cwd: in.Cwd, Input: in.ToolInput,
	})
	if err != nil {
		if ctx.Err() != nil {
			return gate.Decision{Reason: "Nobody answered in time, so this tool call was blocked."}
		}
		return gate.Decision{Reason: "Atlas Commander isn't running, so this tool call was blocked."}
	}
	return d
}

func write(w io.Writer, d gate.Decision) {
	var out hookOutput
	out.HookSpecificOutput.HookEventName = "PreToolUse"
	out.HookSpecificOutput.PermissionDecision = "deny"
	if d.Allow {
		out.HookSpecificOutput.PermissionDecision = "allow"
	}
	out.HookSpecificOutput.PermissionDecisionReason = d.Reason
	json.NewEncoder(w).Encode(out)
}

func main() {
	// One minute under Claude Code's own hook timeout, so we always answer
	// first; a timed-out hook would let the tool run.
	write(os.Stdout, decide(os.Stdin, os.Getenv, gate.HookTimeout-time.Minute))
}
