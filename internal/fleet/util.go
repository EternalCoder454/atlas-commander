package fleet

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// truncate cuts s to at most n bytes (plus an ellipsis), never in the middle
// of a rune, so JSON and the UI never see broken UTF-8.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// oneLine collapses all whitespace runs so a command or path fits a single
// row, then trims it to n bytes.
func oneLine(s string, n int) string {
	return truncate(strings.Join(strings.Fields(s), " "), n)
}

// firstLine is the first non-empty line of s, trimmed.
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return truncate(l, 200)
		}
	}
	return ""
}

// summarize is the one-line description of a tool call: the command for
// Bash, the file for file tools, the pattern for Grep and Glob, otherwise the
// compact input.
func summarize(tool string, input json.RawMessage) string {
	var m map[string]any
	if len(input) > 0 && json.Unmarshal(input, &m) == nil {
		pick := func(keys ...string) string {
			for _, k := range keys {
				if v, ok := m[k].(string); ok && v != "" {
					return v
				}
			}
			return ""
		}
		var s string
		switch tool {
		case "Bash":
			s = pick("command")
		case "Read", "Write", "Edit", "MultiEdit", "NotebookEdit":
			s = pick("file_path", "notebook_path", "path")
		case "Grep", "Glob":
			s = pick("pattern")
		}
		if s != "" {
			return oneLine(s, 200)
		}
	}
	if len(input) == 0 {
		return ""
	}
	var b bytes.Buffer
	if json.Compact(&b, input) != nil {
		return oneLine(string(input), 200)
	}
	return oneLine(b.String(), 200)
}

// prettyJSON indents raw for the approval dialog; invalid JSON is shown as is.
func prettyJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var b bytes.Buffer
	if json.Indent(&b, raw, "", "  ") != nil {
		return string(raw)
	}
	return b.String()
}

// detail encodes an audit row's Detail column.
func detail(m map[string]any) string {
	if len(m) == 0 {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

const ringSize = 60

// ring is a fixed window of the last ringSize samples.
type ring struct {
	v [ringSize]float64
	n int // samples pushed, saturating at ringSize
	i int // next write position
}

func (r *ring) push(x float64) {
	r.v[r.i] = x
	r.i = (r.i + 1) % ringSize
	if r.n < ringSize {
		r.n++
	}
}

// slice returns a fresh copy, oldest first, always ringSize long (zeros fill
// the part of the window before the first sample) so charts keep their width.
func (r *ring) slice() []float64 {
	out := make([]float64, ringSize)
	for k := 0; k < r.n; k++ {
		out[ringSize-r.n+k] = r.v[(r.i-r.n+k+2*ringSize)%ringSize]
	}
	return out
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	return strings.ToUpper(string(r)) + s[n:]
}
