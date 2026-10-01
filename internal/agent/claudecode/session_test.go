package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"atlas-commander/internal/agent"
	"atlas-commander/internal/procgroup"
)

// fakeGroup is the smallest Group that satisfies the contract without the
// real implementation: it starts the command and kills the one process.
type fakeGroup struct{ cmd *exec.Cmd }

func (g *fakeGroup) Start(cmd *exec.Cmd) error { g.cmd = cmd; return cmd.Start() }
func (g *fakeGroup) Terminate() error          { return g.Kill() }
func (g *fakeGroup) Kill() error {
	if g.cmd == nil || g.cmd.Process == nil {
		return nil
	}
	return g.cmd.Process.Kill()
}
func (g *fakeGroup) Close() error { return nil }

func newFakeGroup() (procgroup.Group, error) { return &fakeGroup{}, nil }

// fakeClaude writes a shell script standing in for claude: it records its
// arguments, replays the fixture, then records every stdin line until stdin
// closes. The real claude is never run in tests.
func fakeClaude(t *testing.T, body string) (bin, argsFile, stdinFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake claude is a shell script")
	}
	dir := t.TempDir()
	fixture, err := filepath.Abs("testdata/deny-bash.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	argsFile = filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\n" +
		strings.ReplaceAll(body, "FIXTURE", "'"+fixture+"'")
	bin = filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsFile, bin + ".stdin"
}

const replayAndRecord = `cat FIXTURE
while IFS= read -r line; do printf '%s\n' "$line" >> "$0.stdin"; done
`

func collect(t *testing.T, s agent.Session) []agent.Event {
	t.Helper()
	var evs []agent.Event
	timeout := time.After(10 * time.Second)
	for {
		select {
		case e, ok := <-s.Events():
			if !ok {
				return evs
			}
			evs = append(evs, e)
		case <-timeout:
			t.Fatalf("events did not close; got %v so far", kinds(evs))
		}
	}
}

// End to end through a real process: arguments, the first prompt and a later
// Send reach stdin as stream-json lines, the fixture comes back as events,
// and Stop ends it with a final "stopped" exit followed by channel close.
func TestSessionRunsFakeClaudeEndToEnd(t *testing.T) {
	bin, argsFile, stdinFile := fakeClaude(t, replayAndRecord)
	b := New(Options{Binary: bin, HookCommand: "/opt/Atlas Commander/atlas-hook", HookTimeout: 90 * time.Second, NewGroup: newFakeGroup, ExtraEnv: []string{"A_EXTRA=1"}})
	if b.Name() != agent.BackendClaudeCode {
		t.Fatalf("got name %q", b.Name())
	}
	s, err := b.Start(context.Background(), agent.Spec{
		Model: "opus", WorkDir: t.TempDir(), SystemPrompt: "be brief", ResumeID: "sess-1",
		Prompt: "first", Env: []string{"B_EXTRA=2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(`second "quoted"`); err != nil {
		t.Fatal(err)
	}

	// Wait for the result event, then stop.
	var evs []agent.Event
	for e := range s.Events() {
		evs = append(evs, e)
		if e.Kind == agent.EventResult {
			break
		}
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	evs = append(evs, collect(t, s)...)

	got := kinds(evs)
	want := []agent.EventKind{
		agent.EventInit, agent.EventUsage, agent.EventToolUse, agent.EventToolResult,
		agent.EventUsage, agent.EventText, agent.EventUsage, agent.EventResult, agent.EventExit,
	}
	if len(got) != len(want) {
		t.Fatalf("got kinds %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got kinds %v, want %v", got, want)
		}
	}
	if last := evs[len(evs)-1]; last.Text != "stopped" || last.IsError {
		t.Errorf("exit: got %+v, want stopped without error", last)
	}

	args, _ := os.ReadFile(argsFile)
	for _, w := range []string{"-p", "stream-json", "--verbose", "--model\nopus", "--append-system-prompt\nbe brief", "--resume\nsess-1", "--settings\n{"} {
		if !strings.Contains(string(args), w) {
			t.Errorf("args missing %q:\n%s", w, args)
		}
	}
	if !strings.Contains(string(args), `'/opt/Atlas Commander/atlas-hook'`) {
		t.Errorf("hook command not quoted in args:\n%s", args)
	}

	in, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(in)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d stdin lines, want 2: %q", len(lines), in)
	}
	for i, wantText := range []string{"first", `second "quoted"`} {
		var m struct {
			Type    string
			Message struct{ Role, Content string }
		}
		if err := json.Unmarshal([]byte(lines[i]), &m); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if m.Type != "user" || m.Message.Role != "user" || m.Message.Content != wantText {
			t.Errorf("stdin line %d: got %+v, want user message %q", i, m, wantText)
		}
	}

	if err := s.Send("late"); err == nil {
		t.Error("Send after exit: got nil error, want one")
	}
	if err := s.Stop(); err != nil {
		t.Errorf("second Stop: got %v, want nil (idempotent)", err)
	}
}

// A process that ignores end of input must still be ended: Stop falls through
// to Terminate and Kill, and returns only after the process is gone.
func TestStopEscalatesWhenProcessIgnoresStdinClose(t *testing.T) {
	oldS, oldT := stopGrace, terminateGrace
	stopGrace, terminateGrace = 100*time.Millisecond, 100*time.Millisecond
	defer func() { stopGrace, terminateGrace = oldS, oldT }()

	bin, _, _ := fakeClaude(t, "exec sleep 60\n")
	s, err := New(Options{Binary: bin, NewGroup: newFakeGroup}).Start(context.Background(), agent.Spec{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	s.Stop()
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Stop took %v, want a fraction of the sleep", d)
	}
	evs := collect(t, s)
	if last := evs[len(evs)-1]; last.Kind != agent.EventExit || last.Text != "stopped" {
		t.Errorf("got last event %+v, want stopped exit", last)
	}
}

// Kill must work when the process is mid-task and never reads stdin.
func TestKillEndsRunningProcess(t *testing.T) {
	bin, _, _ := fakeClaude(t, "exec sleep 60\n")
	s, err := New(Options{Binary: bin, NewGroup: newFakeGroup}).Start(context.Background(), agent.Spec{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Kill(); err != nil {
		t.Fatal(err)
	}
	s.Kill()
	evs := collect(t, s)
	if last := evs[len(evs)-1]; last.Kind != agent.EventExit || last.Text != "stopped" {
		t.Errorf("got last event %+v, want stopped exit", last)
	}
}

// A crash is not "stopped": the supervisor needs the exit code and stderr to
// tell the user what happened.
func TestExitEventReportsCrashWithStderr(t *testing.T) {
	bin, _, _ := fakeClaude(t, "echo 'boom: bad flag' >&2\nexit 3\n")
	s, err := New(Options{Binary: bin, NewGroup: newFakeGroup}).Start(context.Background(), agent.Spec{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, s)
	last := evs[len(evs)-1]
	if last.Kind != agent.EventExit || !last.IsError || !strings.Contains(last.Text, "code 3") || !strings.Contains(last.Text, "boom: bad flag") {
		t.Errorf("got %+v, want error exit with code 3 and stderr", last)
	}
}

// Tool output can be several MB on one line; a default Scanner would stop at
// 64 KB and the session would hang.
func TestSessionReadsVeryLongLines(t *testing.T) {
	body := `printf '{"type":"assistant","message":{"id":"m","content":[{"type":"text","text":"'
head -c 5000000 /dev/zero | tr '\0' 'x'
printf '"}]}}\n'
`
	bin, _, _ := fakeClaude(t, body)
	s, err := New(Options{Binary: bin, NewGroup: newFakeGroup}).Start(context.Background(), agent.Spec{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, s)
	if len(evs) < 1 || evs[0].Kind != agent.EventText || len(evs[0].Text) != 5000000 {
		t.Errorf("got %d events, first %v, want the 5 MB text event", len(evs), kinds(evs))
	}
}

func TestStartFailsWithoutGroupFactoryOrBinary(t *testing.T) {
	if _, err := New(Options{Binary: "/nonexistent/claude", NewGroup: newFakeGroup}).Start(context.Background(), agent.Spec{WorkDir: t.TempDir()}); err == nil {
		t.Error("missing binary: got nil error")
	}
	if _, err := New(Options{Binary: "x"}).Start(context.Background(), agent.Spec{}); err == nil {
		t.Error("no NewGroup: got nil error")
	}
}

func TestDetectReportsMissingBinaryAndVersion(t *testing.T) {
	s := Detect("/nonexistent/claude")
	if len(s.Problems) == 0 || !strings.Contains(s.Problems[0], "npm install -g @anthropic-ai/claude-code") {
		t.Errorf("got %+v, want an install hint", s)
	}
	bin, _, _ := fakeClaude(t, "echo '2.1.274 (Claude Code)'\n")
	s = Detect(bin)
	if len(s.Problems) != 0 || s.Version != "2.1.274 (Claude Code)" || s.Path != bin {
		t.Errorf("got %+v, want version and no problems", s)
	}
	bad, _, _ := fakeClaude(t, "exit 1\n")
	if s = Detect(bad); len(s.Problems) != 1 {
		t.Errorf("failing --version: got %+v, want one problem", s)
	}
}

// Leftover children (a dev server the Bash tool started) hold stdout open
// after claude exits. Events must still close and the exit must still be
// reported, and a clean exit must stay clean.
func TestEventsCloseWhenGrandchildHoldsStdout(t *testing.T) {
	bin, _, _ := fakeClaude(t, "sleep 8 &\nexit 0\n")
	s, err := New(Options{Binary: bin, NewGroup: newFakeGroup}).Start(context.Background(), agent.Spec{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	evs := collect(t, s)
	if d := time.Since(start); d > 7*time.Second {
		t.Errorf("events took %v to close, want a few seconds", d)
	}
	if last := evs[len(evs)-1]; last.Kind != agent.EventExit || last.IsError {
		t.Errorf("got last event %+v, want a clean exit", last)
	}
}

// Stop and Kill after the process is gone must be harmless no-ops, and an
// earlier crash must not be relabelled "stopped" by a later Stop.
func TestStopAfterCrashKeepsErrorExit(t *testing.T) {
	bin, _, _ := fakeClaude(t, "exit 4\n")
	s, err := New(Options{Binary: bin, NewGroup: newFakeGroup}).Start(context.Background(), agent.Spec{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, s)
	if err := s.Stop(); err != nil {
		t.Errorf("Stop after exit: got %v, want nil", err)
	}
	if err := s.Kill(); err != nil {
		t.Errorf("Kill after exit: got %v, want nil", err)
	}
	if last := evs[len(evs)-1]; !last.IsError || !strings.Contains(last.Text, "code 4") {
		t.Errorf("got %+v, want error exit with code 4", last)
	}
}

// The gate address and token reach the hook helper only through the agent
// process environment.
func TestSpecEnvReachesProcess(t *testing.T) {
	bin, _, _ := fakeClaude(t, "printf '%s' \"$ATLAS_GATE_ADDR|$A_EXTRA\" > \"$0.env\"\n")
	s, err := New(Options{Binary: bin, NewGroup: newFakeGroup, ExtraEnv: []string{"A_EXTRA=1"}}).Start(context.Background(),
		agent.Spec{WorkDir: t.TempDir(), Env: []string{"ATLAS_GATE_ADDR=sock"}})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, s)
	got, _ := os.ReadFile(bin + ".env")
	if string(got) != "sock|1" {
		t.Errorf("got env %q, want %q", got, "sock|1")
	}
}
