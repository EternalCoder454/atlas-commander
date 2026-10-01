package observed

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	headBytes    = 64 << 10
	tailBytes    = 256 << 10
	maxLineBytes = 32 << 20
	maxResult    = 2 << 10
	maxText      = 16 << 10 // one Line.Text; a pasted file must not bloat the view
	readBytes    = 4 << 20  // Read parses only this much of the file's end
	headMax      = 1 << 20  // head window when no prompt shows up in headBytes
	liveWindow   = 2 * time.Minute
)

// Root returns the directory Claude Code keeps transcripts in:
// $CLAUDE_CONFIG_DIR/projects, else ~/.claude/projects.
func Root() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "projects")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// entry is the subset of a transcript line this package looks at.
type entry struct {
	Type        string          `json:"type"`
	Timestamp   string          `json:"timestamp"`
	Cwd         string          `json:"cwd"`
	GitBranch   string          `json:"gitBranch"`
	IsSidechain bool            `json:"isSidechain"`
	IsMeta      bool            `json:"isMeta"`
	Subtype     string          `json:"subtype"`
	Content     json.RawMessage `json:"content"` // system entries carry text here
	Message     struct {
		ID      string          `json:"id"`
		Role    string          `json:"role"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type block struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
	IsError bool            `json:"is_error"`
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// blocks normalises content (a string or an array of blocks) to blocks.
func blocks(raw json.RawMessage) []block {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil
		}
		return []block{{Type: "text", Text: s}}
	}
	var bs []block
	if json.Unmarshal(raw, &bs) != nil {
		return nil
	}
	return bs
}

// forEachLine streams JSONL lines, skipping any longer than maxLineBytes.
// fn returns false to stop.
func forEachLine(r io.Reader, fn func([]byte) bool) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var buf []byte
	skipping := false
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 && !skipping {
			buf = append(buf, chunk...)
			if len(buf) > maxLineBytes {
				skipping = true
				buf = nil
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if !skipping && len(buf) > 0 {
			if !fn(bytes.TrimSpace(buf)) {
				return nil
			}
		}
		buf, skipping = buf[:0], false
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = strings.ToValidUTF8(s[:max], "") + "…"
	}
	return s
}

// List scans root/*/*.jsonl and returns up to limit sessions, newest first.
// A missing root is not an error. Unreadable files are skipped.
func List(root string, limit int) ([]Session, error) {
	paths, err := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		out = append(out, Session{
			Path:     p,
			ID:       strings.TrimSuffix(filepath.Base(p), ".jsonl"),
			Modified: fi.ModTime(),
			Size:     fi.Size(),
		})
	}
	if len(out) == 0 {
		return nil, nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	kept := out[:0]
	now := time.Now()
	for _, s := range out {
		if err := fill(&s); err != nil {
			continue
		}
		s.Live = now.Sub(s.Modified) < liveWindow
		kept = append(kept, s)
	}
	return kept, nil
}

// fillHead scans the first size bytes of f for project, branch, start and title.
func fillHead(f *os.File, s *Session, size int) error {
	head := make([]byte, size)
	n, err := f.ReadAt(head, 0)
	if err != nil && err != io.EOF {
		return err
	}
	head = head[:n]
	if n == size { // the last line is probably cut off
		if i := bytes.LastIndexByte(head, '\n'); i >= 0 {
			head = head[:i]
		} else {
			head = nil // the first line alone is bigger than the window
		}
	}
	_ = forEachLine(bytes.NewReader(head), func(l []byte) bool {
		var e entry
		if json.Unmarshal(l, &e) != nil {
			return true
		}
		if s.Project == "" && e.Cwd != "" {
			s.Project = e.Cwd
		}
		if s.GitBranch == "" && e.GitBranch != "" {
			s.GitBranch = e.GitBranch
		}
		if s.Started.IsZero() {
			s.Started = parseTime(e.Timestamp)
		}
		if s.Title == "" && e.Type == "user" && !e.IsMeta && !e.IsSidechain {
			for _, b := range blocks(e.Message.Content) {
				t := strings.TrimSpace(b.Text)
				if b.Type == "text" && t != "" && !strings.HasPrefix(t, "<") {
					s.Title = oneLine(t, 120)
					break
				}
			}
		}
		return true
	})
	return nil
}

// fill reads the head and tail of the file to complete s.
func fill(s *Session) error {
	f, err := os.Open(s.Path)
	if err != nil {
		return err
	}
	defer f.Close()

	// A session whose first prompt is buried under a huge wrapper entry
	// gets one wider look before it is shown untitled.
	for _, size := range []int{headBytes, headMax} {
		if err := fillHead(f, s, size); err != nil {
			return err
		}
		if s.Title != "" || s.Size <= int64(size) {
			break
		}
	}

	off := s.Size - tailBytes
	if off < 0 {
		off = 0
	}
	tail := make([]byte, s.Size-off)
	n, err := f.ReadAt(tail, off)
	if err != nil && err != io.EOF {
		return err
	}
	tail = tail[:n]
	if off > 0 { // drop the partial first line
		if i := bytes.IndexByte(tail, '\n'); i >= 0 {
			tail = tail[i+1:]
		} else {
			tail = nil
		}
	}
	// Claude Code writes one assistant entry per content block with the same
	// message id, so count each id once. The count covers the tail window
	// only, which is why the UI labels it as recent.
	seen := map[string]bool{}
	_ = forEachLine(bytes.NewReader(tail), func(l []byte) bool {
		var e entry
		if json.Unmarshal(l, &e) != nil || e.IsSidechain || e.IsMeta {
			return true
		}
		if e.Type == "user" || e.Type == "assistant" {
			if e.Message.ID == "" || !seen[e.Message.ID] {
				s.Messages++
			}
			if e.Message.ID != "" {
				seen[e.Message.ID] = true
			}
		}
		if e.Type == "assistant" && e.Message.Model != "" && e.Message.Model != "<synthetic>" {
			s.Model = e.Message.Model
		}
		return true
	})
	return nil
}

// checkPath makes sure path is a file inside Root().
func checkPath(path string) (string, error) {
	root, err := filepath.EvalSymlinks(Root())
	if err != nil {
		return "", errors.New("transcript folder not found")
	}
	p, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("cannot open transcript: %w", err)
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("path is outside the transcript folder")
	}
	return p, nil
}

// Read returns the last maxLines mapped lines of the transcript at path.
// maxLines <= 0 means no limit.
func Read(path string, maxLines int) ([]Line, error) {
	p, err := checkPath(path)
	if err != nil {
		return nil, err
	}
	// Opening a FIFO for reading blocks until a writer shows up, so only a
	// regular file may reach os.Open.
	if fi, err := os.Stat(p); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, errors.New("not a transcript file")
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat() // the path may have been swapped after the first check
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a transcript file")
	}
	// A live transcript can be hundreds of MB; only its end is ever shown.
	var r io.Reader = f
	partial := false
	if fi.Size() > readBytes {
		if _, err := f.Seek(fi.Size()-readBytes, io.SeekStart); err != nil {
			return nil, err
		}
		partial = true
	}

	var out []Line
	err = forEachLine(r, func(l []byte) bool {
		if partial { // the first line of a mid-file seek is cut off
			partial = false
			return true
		}
		var e entry
		if json.Unmarshal(l, &e) != nil || e.IsSidechain || e.IsMeta {
			return true
		}
		out = append(out, mapEntry(&e)...)
		return true
	})
	if err != nil {
		return nil, err
	}
	if maxLines > 0 && len(out) > maxLines {
		out = append([]Line(nil), out[len(out)-maxLines:]...)
	}
	return out, nil
}

func mapEntry(e *entry) []Line {
	at := parseTime(e.Timestamp)
	switch e.Type {
	case "system":
		t := ""
		var s string
		if json.Unmarshal(e.Content, &s) == nil {
			t = s
		}
		if t == "" {
			t = e.Subtype
		}
		if t == "" {
			return nil
		}
		return []Line{{At: at, Role: "system", Text: oneLine(t, 500)}}
	case "user", "assistant":
	default:
		return nil
	}
	var out []Line
	for _, b := range blocks(e.Message.Content) {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) == "" {
				continue
			}
			out = append(out, Line{At: at, Role: e.Type, Text: truncate(b.Text, maxText)})
		case "tool_use":
			out = append(out, Line{At: at, Role: "tool", Tool: b.Name, Text: toolSummary(b.Input)})
		case "tool_result":
			out = append(out, Line{At: at, Role: "result", Text: truncate(resultText(b.Content), maxResult), IsError: b.IsError})
		}
	}
	return out
}

func resultText(raw json.RawMessage) string {
	var parts []string
	for _, b := range blocks(raw) {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], "") + "…"
}

func toolSummary(input json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(input, &m) == nil {
		for _, k := range []string{"command", "file_path", "pattern"} {
			if s, ok := m[k].(string); ok && s != "" {
				return oneLine(s, 200)
			}
		}
	}
	var c bytes.Buffer
	if json.Compact(&c, input) != nil {
		return ""
	}
	return oneLine(c.String(), 200)
}
