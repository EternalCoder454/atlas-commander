package observed

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setup copies a testdata fixture into a temp root laid out like Claude's.
func setup(t *testing.T, fixture, proj, id string) (root, path string) {
	t.Helper()
	// Root() is <CLAUDE_CONFIG_DIR>/projects, so point the config dir at a temp dir.
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	root = Root()
	data, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, proj)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return root, path
}

// TestRootHonoursConfigDir matters because users relocate Claude's config.
func TestRootHonoursConfigDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/x/cfg")
	if got, want := Root(), filepath.Join("/x/cfg", "projects"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestListOrdersAndFillsSessions checks ordering, Title (wrappers skipped) and Live.
func TestListOrdersAndFillsSessions(t *testing.T) {
	root, old := setup(t, "basic.jsonl", "-tmp-a", "old")
	data, _ := os.ReadFile(old)
	other := filepath.Join(root, "-tmp-b")
	os.MkdirAll(other, 0o755)
	newer := filepath.Join(other, "new.jsonl")
	os.WriteFile(newer, data, 0o600)
	past := time.Now().Add(-time.Hour)
	os.Chtimes(old, past, past)

	got, err := List(root, 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v, %v, want 2 sessions", got, err)
	}
	if got[0].ID != "new" || !got[0].Live || got[1].Live {
		t.Errorf("got %q live=%v/%v, want new first and only it live", got[0].ID, got[0].Live, got[1].Live)
	}
	s := got[0]
	if s.Title != "Fix the login bug" || s.Project != "/tmp/proj" || s.GitBranch != "main" || s.Model != "claude-test-1" || s.Messages != 8 || s.Started.IsZero() {
		t.Errorf("got %+v, want title/project/branch/model set and 8 messages", s)
	}
	if one, _ := List(root, 1); len(one) != 1 {
		t.Errorf("got %d sessions, want 1 with limit", len(one))
	}
}

// TestListMissingRoot guards first-run machines without Claude Code.
func TestListMissingRoot(t *testing.T) {
	got, err := List(filepath.Join(t.TempDir(), "nope"), 5)
	if got != nil || err != nil {
		t.Errorf("got %v, %v, want nil, nil", got, err)
	}
}

// TestReadMapsEveryKind checks each entry kind, and that thinking and
// sidechain entries and corrupt lines are dropped.
func TestReadMapsEveryKind(t *testing.T) {
	_, p := setup(t, "basic.jsonl", "-tmp-a", "s")
	got, err := Read(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ role, tool string }{
		{"user", ""}, {"user", ""}, {"assistant", ""}, {"tool", "Bash"}, {"result", ""},
		{"tool", "Grep"}, {"result", ""}, {"tool", "Weird"}, {"assistant", ""}, {"system", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines %+v, want %d", len(got), got, len(want))
	}
	for i, w := range want {
		if got[i].Role != w.role || got[i].Tool != w.tool {
			t.Errorf("line %d: got %s/%s, want %s/%s", i, got[i].Role, got[i].Tool, w.role, w.tool)
		}
	}
	if got[3].Text != "ls -la" || got[5].Text != "TODO" {
		t.Errorf("got summaries %q, %q, want ls -la, TODO", got[3].Text, got[5].Text)
	}
	if !got[6].IsError || got[6].Text != "boom" {
		t.Errorf("got %+v, want error result boom", got[6])
	}
	if len(got[7].Text) > 203 || !strings.HasPrefix(got[7].Text, `{"a":`) {
		t.Errorf("got %d-char summary %q, want trimmed compact JSON", len(got[7].Text), got[7].Text)
	}
	if got[9].Text != "Conversation compacted" {
		t.Errorf("got %q, want system text", got[9].Text)
	}
}

// TestReadMaxLinesKeepsTail matters because the UI wants the newest lines.
func TestReadMaxLinesKeepsTail(t *testing.T) {
	_, p := setup(t, "basic.jsonl", "-tmp-a", "s")
	got, _ := Read(p, 2)
	if len(got) != 2 || got[1].Role != "system" || got[0].Role != "assistant" {
		t.Errorf("got %+v, want last two lines", got)
	}
}

// TestReadSkipsHugeLine ensures one giant line neither fails nor leaks in.
// The file is larger than the tail Read parses, so only what follows the
// giant line is seen.
func TestReadSkipsHugeLine(t *testing.T) {
	_, p := setup(t, "basic.jsonl", "-tmp-a", "s")
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"user","message":{"role":"user","content":"` + strings.Repeat("x", maxLineBytes+10) + `"}}` + "\n")
	f.WriteString(`{"type":"user","message":{"role":"user","content":"after"}}` + "\n")
	f.Close()
	got, err := Read(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("got no lines, want at least the one after the huge line")
	}
	for _, l := range got {
		if len(l.Text) > maxText+8 {
			t.Errorf("got a %d-byte line, want at most %d", len(l.Text), maxText)
		}
	}
	if last := got[len(got)-1]; last.Text != "after" {
		t.Errorf("got last %q, want after", last.Text)
	}
}

// TestReadRefusesOutsideRoot protects against UI-supplied paths escaping.
func TestReadRefusesOutsideRoot(t *testing.T) {
	root, _ := setup(t, "basic.jsonl", "-tmp-a", "s")
	outside := filepath.Join(t.TempDir(), "x.jsonl")
	os.WriteFile(outside, []byte("{}\n"), 0o600)
	link := filepath.Join(root, "-tmp-a", "link.jsonl")
	os.Symlink(outside, link)
	for _, p := range []string{outside, link, filepath.Join(root, "..", "x.jsonl"), root} {
		if _, err := Read(p, 0); err == nil {
			t.Errorf("Read(%q): got nil error, want refusal", p)
		}
	}
}

// TestListSkipsUnreadable ensures a directory named *.jsonl is not fatal.
func TestListSkipsUnreadable(t *testing.T) {
	root, _ := setup(t, "basic.jsonl", "-tmp-a", "s")
	os.MkdirAll(filepath.Join(root, "-tmp-a", "dir.jsonl"), 0o755)
	got, err := List(root, 10)
	if err != nil || len(got) != 1 {
		t.Errorf("got %d, %v, want 1, nil", len(got), err)
	}
}

// TestReadIgnoresPartialLastLine matters because Claude Code is mid-write
// when a live transcript is read; a cut-off line must not fail the read.
func TestReadIgnoresPartialLastLine(t *testing.T) {
	_, p := setup(t, "basic.jsonl", "-tmp-a", "s")
	whole, _ := Read(p, 0)
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"user","message":{"role":"user","content":"cut of`)
	f.Close()
	got, err := Read(p, 0)
	if err != nil || len(got) != len(whole) {
		t.Errorf("got %d lines, %v, want %d lines and no error", len(got), err, len(whole))
	}
}

// TestFillFailsOnVanishedFile covers a transcript deleted between List's
// Stat and fill's Open: it must be an error List can skip, not a panic.
func TestFillFailsOnVanishedFile(t *testing.T) {
	s := &Session{Path: filepath.Join(t.TempDir(), "gone.jsonl"), Size: 100}
	if err := fill(s); err == nil {
		t.Error("got nil error, want one for a missing file")
	}
	if _, err := Read(s.Path, 0); err == nil {
		t.Error("Read: got nil error, want one for a missing file")
	}
}

// TestFillFindsTitleBehindHugeFirstLine matters because a first line larger
// than the head window used to leave the session untitled.
func TestFillFindsTitleBehindHugeFirstLine(t *testing.T) {
	root, p := setup(t, "basic.jsonl", "-tmp-a", "s")
	big := `{"type":"system","cwd":"/tmp/big","content":"` + strings.Repeat("x", headBytes+10) + `"}` + "\n" +
		`{"type":"user","message":{"role":"user","content":"Real prompt"}}` + "\n"
	if err := os.WriteFile(p, []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := List(root, 10)
	if err != nil || len(got) != 1 || got[0].Title != "Real prompt" || got[0].Project != "/tmp/big" {
		t.Errorf("got %+v, %v, want title Real prompt and project /tmp/big", got, err)
	}
}

// TestReadParsesOnlyTheTail matters because a live transcript can be
// hundreds of MB and Read runs every two seconds: it must read the end, and
// start on a whole line.
func TestReadParsesOnlyTheTail(t *testing.T) {
	_, p := setup(t, "basic.jsonl", "-tmp-a", "s")
	var b strings.Builder
	const n = 60000
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `{"type":"user","message":{"role":"user","content":"line %d %s"}}`+"\n", i, strings.Repeat("p", 40))
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if b.Len() <= readBytes {
		t.Fatalf("test file is %d bytes, want more than %d", b.Len(), readBytes)
	}
	got, err := Read(p, 0)
	if err != nil || len(got) == 0 || len(got) >= n {
		t.Fatalf("got %d lines, %v, want some but fewer than %d", len(got), err, n)
	}
	if want := fmt.Sprintf("line %d ", n-1); !strings.HasPrefix(got[len(got)-1].Text, want) {
		t.Errorf("got last %q, want it to start with %q", got[len(got)-1].Text, want)
	}
	if !strings.HasPrefix(got[0].Text, "line ") {
		t.Errorf("got first %q, want a whole line", got[0].Text)
	}
}
