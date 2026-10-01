package claudecode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"atlas-commander/internal/agent"
)

// maxToolResultRunes caps tool output carried in events. Tool results can be
// whole files; the audit log and activity feed only need the start.
const maxToolResultRunes = 500

// parser turns Claude Code stream-json lines into agent events. It is pure
// (no I/O, no goroutines) so it can be tested and fuzzed on its own, and it is
// not safe for concurrent use: one reader goroutine owns it.
//
// Usage accounting. Claude Code repeats the same message.id, with the same
// usage, on every content block of one API message, so usage is emitted once
// per id. The usage on those partial events can undercount output_tokens
// (thinking tokens in the fixture: 9 seen on assistant events against 222 on
// the result). The result event carries the turn total, so the parser keeps
// the sum it has emitted for the turn and, on result, emits one correcting
// EventUsage for the per-field difference (never negative) before EventResult.
// The sum of EventUsage per turn therefore equals result.usage whenever the
// result is at least as large field by field. Messages from subagents
// (parent_tool_use_id set) are not counted, as the result does not include
// them. Token counts are best effort; the cost comes from total_cost_usd.
type parser struct {
	seen     map[string]bool // message ids already counted this turn
	emitted  agent.Usage     // usage emitted since the last result
	lastCost float64         // total_cost_usd of the previous result (cumulative per process)
}

// fields is one JSON object with its values still raw. Every accessor ignores
// a value of the wrong type and returns the zero value, so one odd field in a
// line (a newer Claude Code changing a type) costs only that field.
type fields map[string]json.RawMessage

func objectOf(raw json.RawMessage) fields {
	var f fields
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	return f
}

func (f fields) str(k string) string {
	var v string
	_ = json.Unmarshal(f[k], &v)
	return v
}

func (f fields) num(k string) float64 {
	var v float64
	_ = json.Unmarshal(f[k], &v)
	return v
}

func (f fields) boolean(k string) bool {
	var v bool
	_ = json.Unmarshal(f[k], &v)
	return v
}

func (f fields) object(k string) fields { return objectOf(f[k]) }

// present reports whether k is set to something other than null.
func (f fields) present(k string) bool {
	raw := bytes.TrimSpace(f[k])
	return len(raw) > 0 && string(raw) != "null"
}

func usageOf(f fields) agent.Usage {
	if f == nil {
		return agent.Usage{}
	}
	out := agent.Usage{
		Input:     int64(f.num("input_tokens")),
		Output:    int64(f.num("output_tokens")),
		CacheRead: int64(f.num("cache_read_input_tokens")),
	}
	if cc := f.object("cache_creation"); cc != nil {
		out.CacheWrite5m = int64(cc.num("ephemeral_5m_input_tokens"))
		out.CacheWrite1h = int64(cc.num("ephemeral_1h_input_tokens"))
	} else {
		// Without the TTL breakdown, the cheaper 5 minute rate is the
		// assumption that never overstates cost.
		out.CacheWrite5m = int64(f.num("cache_creation_input_tokens"))
	}
	return out
}

// parse maps one stdout line to zero or more events. Lines that are not JSON
// or not interesting yield nothing: the stream format grows new event types
// with every Claude Code release and must not break a running agent. The
// envelope is decoded minimally (the type, the rest raw) and each part is
// decoded on its own, so a type mismatch drops only the part it is in.
func (p *parser) parse(line []byte, now time.Time) []agent.Event {
	var env struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(line, &env) != nil {
		return nil
	}
	l := objectOf(line)
	switch env.Type {
	case "system":
		if l.str("subtype") == "init" {
			return []agent.Event{{Kind: agent.EventInit, Time: now, SessionID: l.str("session_id"), Model: l.str("model")}}
		}
	case "assistant":
		return p.assistant(l, now)
	case "user":
		return userEvents(l, now)
	case "result":
		return p.result(l, now)
	case "rate_limit_event":
		if rl := l.object("rate_limit_info"); rl != nil {
			if status := rl.str("status"); status != "" && status != "allowed" {
				return []agent.Event{{Kind: agent.EventError, Time: now, Text: rateLimitText(status, rl.str("rateLimitType"), rl.num("resetsAt"))}}
			}
		}
	}
	return nil
}

// blocks decodes message.content one block at a time. A plain string content
// (an echoed prompt) or a bad block yields nothing for that part.
func blocks(msg fields) []fields {
	var raws []json.RawMessage
	if json.Unmarshal(msg["content"], &raws) != nil {
		return nil
	}
	var out []fields
	for _, r := range raws {
		if b := objectOf(r); b != nil {
			out = append(out, b)
		}
	}
	return out
}

func (p *parser) assistant(l fields, now time.Time) []agent.Event {
	msg := l.object("message")
	if msg == nil {
		return nil
	}
	var evs []agent.Event
	for _, b := range blocks(msg) {
		switch b.str("type") {
		case "text":
			evs = append(evs, agent.Event{Kind: agent.EventText, Time: now, Text: b.str("text")})
		case "tool_use":
			evs = append(evs, agent.Event{Kind: agent.EventToolUse, Time: now, Tool: b.str("name"), ToolInput: b["input"], ToolUseID: b.str("id")})
		}
	}
	// Subagent messages (parent_tool_use_id set) are not counted: the
	// result's usage does not include them, so counting them could push the
	// per-turn sum above the result.
	if id := msg.str("id"); id != "" && msg.present("usage") && !l.present("parent_tool_use_id") {
		if p.seen == nil {
			p.seen = make(map[string]bool)
		}
		if !p.seen[id] {
			p.seen[id] = true
			u := usageOf(msg.object("usage"))
			p.emitted = p.emitted.Add(u)
			if !u.IsZero() {
				evs = append(evs, agent.Event{Kind: agent.EventUsage, Time: now, Usage: u})
			}
		}
	}
	return evs
}

func userEvents(l fields, now time.Time) []agent.Event {
	msg := l.object("message")
	if msg == nil {
		return nil
	}
	var evs []agent.Event
	for _, b := range blocks(msg) {
		if b.str("type") != "tool_result" {
			continue
		}
		evs = append(evs, agent.Event{
			Kind: agent.EventToolResult, Time: now,
			ToolUseID: b.str("tool_use_id"), IsError: b.boolean("is_error"),
			Text: truncateRunes(contentText(b["content"]), maxToolResultRunes),
		})
	}
	return evs
}

// contentText flattens tool_result content, which is either a string or a
// list of parts of which only the text ones are shown.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var texts []string
	for _, r := range parts {
		if part := objectOf(r); part != nil && part.str("type") == "text" {
			texts = append(texts, part.str("text"))
		}
	}
	return strings.Join(texts, "\n")
}

func truncateRunes(s string, n int) string {
	if len(s) <= n { // fewer bytes than the limit means fewer runes too
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func (p *parser) result(l fields, now time.Time) []agent.Event {
	var evs []agent.Event
	if l.present("usage") {
		total := usageOf(l.object("usage"))
		diff := agent.Usage{
			Input:        positive(total.Input - p.emitted.Input),
			Output:       positive(total.Output - p.emitted.Output),
			CacheRead:    positive(total.CacheRead - p.emitted.CacheRead),
			CacheWrite5m: positive(total.CacheWrite5m - p.emitted.CacheWrite5m),
			CacheWrite1h: positive(total.CacheWrite1h - p.emitted.CacheWrite1h),
		}
		if !diff.IsZero() {
			evs = append(evs, agent.Event{Kind: agent.EventUsage, Time: now, Usage: diff})
		}
	}
	// A new turn starts counting from zero; ids never repeat across turns.
	p.emitted = agent.Usage{}
	p.seen = nil

	// total_cost_usd is cumulative for the process, so this turn's cost is
	// the step from the previous result. A drop means the counter restarted.
	total := l.num("total_cost_usd")
	cost := total - p.lastCost
	if cost < 0 {
		cost = total
	}
	p.lastCost = total

	subtype := l.str("subtype")
	isErr := l.boolean("is_error") || subtype != "success"
	text := l.str("result")
	if text == "" && isErr {
		text = resultErrorText(subtype)
	}
	evs = append(evs, agent.Event{
		Kind: agent.EventResult, Time: now, IsError: isErr, Text: text,
		CostUSD:  max(cost, 0),
		Duration: time.Duration(l.num("duration_ms") * float64(time.Millisecond)),
		Turns:    int(l.num("num_turns")),
	})
	return evs
}

func positive(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

func resultErrorText(subtype string) string {
	switch subtype {
	case "error_max_turns":
		return "The agent stopped because it reached its turn limit."
	case "error_during_execution":
		return "The agent hit an error while working."
	case "error_max_budget_usd":
		return "The agent stopped because it reached its budget."
	case "":
		return "The agent reported an error."
	}
	return fmt.Sprintf("The agent stopped with an error (%s).", subtype)
}

func rateLimitText(status, kind string, resetsAt float64) string {
	s := "Claude rate limit: " + strings.ReplaceAll(status, "_", " ")
	if kind != "" {
		s += " (" + strings.ReplaceAll(kind, "_", " ") + ")"
	}
	if resetsAt > 0 {
		s += ", resets at " + time.Unix(int64(resetsAt), 0).Format("15:04")
	}
	return s + "."
}
