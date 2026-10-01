package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"atlas-commander/internal/gate"
)

func build(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "atlas-hook")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func run(t *testing.T, bin, stdin string, env ...string) (decision, reason string) {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helper exit: %v, want 0", err)
	}
	var o hookOutput
	if err := json.Unmarshal(out, &o); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	if o.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Errorf("hookEventName: got %q, want PreToolUse", o.HookSpecificOutput.HookEventName)
	}
	return o.HookSpecificOutput.PermissionDecision, o.HookSpecificOutput.PermissionDecisionReason
}

const hookJSON = `{"session_id":"s1","tool_name":"Bash","tool_input":{"command":"ls"},"tool_use_id":"t1","cwd":"/w","permission_mode":"default"}`

// The helper is the only thing standing between the model and a tool call, so
// both answers and every failure path must produce valid hook JSON.
func TestHelperAllowsDeniesAndFailsClosed(t *testing.T) {
	bin := build(t)
	dir, _ := os.MkdirTemp("", "ah")
	defer os.RemoveAll(dir)
	var seen gate.Request
	allow := true
	s, err := gate.Listen(filepath.Join(dir, "g"), func(_ context.Context, r gate.Request) gate.Decision {
		seen = r
		return gate.Decision{Allow: allow, Reason: "policy says so"}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tok := s.Register("agent-7")
	env := []string{gate.EnvAddr + "=" + s.Addr(), gate.EnvToken + "=" + tok, gate.EnvAgentID + "=agent-7"}

	d, r := run(t, bin, hookJSON, env...)
	if d != "allow" || r != "policy says so" {
		t.Errorf("allow: got %q %q, want allow", d, r)
	}
	if seen.AgentID != "agent-7" || seen.Tool != "Bash" || seen.ToolUseID != "t1" || seen.SessionID != "s1" ||
		seen.Cwd != "/w" || string(seen.Input) != `{"command":"ls"}` {
		t.Errorf("request: got %+v", seen)
	}
	allow = false
	if d, _ := run(t, bin, hookJSON, env...); d != "deny" {
		t.Errorf("deny: got %q, want deny", d)
	}
	if d, r := run(t, bin, "not json", env...); d != "deny" || r == "" {
		t.Errorf("bad stdin: got %q %q, want deny with reason", d, r)
	}
	if d, r := run(t, bin, hookJSON, gate.EnvAddr+"=", gate.EnvToken+"=", gate.EnvAgentID+"="); d != "deny" || r == "" {
		t.Errorf("missing env: got %q %q, want deny with reason", d, r)
	}
	s.Close()
	d, r = run(t, bin, hookJSON, env...)
	if d != "deny" || !strings.Contains(r, "isn't running") {
		t.Errorf("unreachable: got %q %q, want deny 'isn't running'", d, r)
	}
}
