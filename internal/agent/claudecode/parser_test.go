package claudecode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"atlas-commander/internal/agent"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func kinds(evs []agent.Event) []agent.EventKind {
	var k []agent.EventKind
	for _, e := range evs {
		k = append(k, e.Kind)
	}
	return k
}

func fixtureEvents(t *testing.T) []agent.Event {
	t.Helper()
	f, err := os.Open("testdata/deny-bash.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var p parser
	var evs []agent.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 8<<20)
	for sc.Scan() {
		evs = append(evs, p.parse(sc.Bytes(), t0)...)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return evs
}

// The captured run is the ground truth for the stream format; the sequence of
// events from it must not change silently when the parser is edited.
func TestParserMapsFixtureSequence(t *testing.T) {
	evs := fixtureEvents(t)
	want := []agent.EventKind{
		agent.EventInit, agent.EventUsage, agent.EventToolUse, agent.EventToolResult,
		agent.EventUsage, agent.EventText, agent.EventUsage, agent.EventResult,
	}
	got := kinds(evs)
	if len(got) != len(want) {
		t.Fatalf("got kinds %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got kinds %v, want %v", got, want)
		}
	}
	if evs[0].SessionID != "50a794d6-7100-4a70-882e-c8e01812e057" || evs[0].Model != "claude-haiku-4-5-20251001" {
		t.Errorf("init: got %q/%q", evs[0].SessionID, evs[0].Model)
	}
	tu := evs[2]
	if tu.Tool != "Bash" || tu.ToolUseID != "toolu_01C1MS2TEas5aSZv2oeTQD5r" || !strings.Contains(string(tu.ToolInput), "echo hi") {
		t.Errorf("tool_use: got %+v", tu)
	}
	tr := evs[3]
	if !tr.IsError || tr.Text != "blocked by test gate" || tr.ToolUseID != tu.ToolUseID {
		t.Errorf("tool_result: got %+v", tr)
	}
	res := evs[7]
	if res.IsError || res.CostUSD != 0.06979859999999999 || res.Turns != 2 || res.Duration != 4377*time.Millisecond {
		t.Errorf("result: got %+v", res)
	}
}

// Billing depends on this: the per-turn sum of usage events must equal the
// result's usage, even though the assistant events undercount output tokens
// (9 on the events, 222 in the result) and repeat usage on every block.
func TestParserUsageSumsToResultUsage(t *testing.T) {
	var sum agent.Usage
	for _, e := range fixtureEvents(t) {
		if e.Kind == agent.EventUsage {
			sum = sum.Add(e.Usage)
		}
	}
	want := agent.Usage{Input: 18, Output: 222, CacheRead: 32526, CacheWrite1h: 32709}
	if sum != want {
		t.Errorf("got usage sum %+v, want %+v", sum, want)
	}
}

// A second turn must start from zero, otherwise the first turn's tokens would
// be subtracted from the second turn's correction.
func TestParserResetsUsageBetweenTurns(t *testing.T) {
	var p parser
	asst := `{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"a"}],"usage":{"input_tokens":5,"output_tokens":1}}}`
	res := `{"type":"result","subtype":"success","usage":{"input_tokens":5,"output_tokens":4}}`
	var sum agent.Usage
	for i := 0; i < 2; i++ {
		for _, line := range []string{asst, res} {
			for _, e := range p.parse([]byte(line), t0) {
				if e.Kind == agent.EventUsage {
					sum = sum.Add(e.Usage)
				}
			}
		}
	}
	if want := (agent.Usage{Input: 10, Output: 8}); sum != want {
		t.Errorf("got %+v, want %+v", sum, want)
	}
}

func TestParserUsageNeverNegative(t *testing.T) {
	var p parser
	p.parse([]byte(`{"type":"assistant","message":{"id":"m1","content":[],"usage":{"input_tokens":50,"output_tokens":9}}}`), t0)
	evs := p.parse([]byte(`{"type":"result","subtype":"success","usage":{"input_tokens":10,"output_tokens":20}}`), t0)
	if len(evs) != 2 || evs[0].Usage != (agent.Usage{Output: 11}) {
		t.Errorf("got %+v, want one usage event with Output 11 then the result", evs)
	}
}

func TestParserTable(t *testing.T) {
	long := strings.Repeat("é", 600)
	tests := []struct {
		name string
		line string
		want func(t *testing.T, evs []agent.Event)
	}{
		{"not json", `hello`, expectNone},
		{"empty object", `{}`, expectNone},
		{"other system subtype", `{"type":"system","subtype":"task_summary"}`, expectNone},
		{"allowed rate limit", `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`, expectNone},
		{"rejected rate limit", `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","resetsAt":1790836200}}`,
			func(t *testing.T, evs []agent.Event) {
				if len(evs) != 1 || evs[0].Kind != agent.EventError || !strings.Contains(evs[0].Text, "rejected") {
					t.Errorf("got %+v", evs)
				}
			}},
		{"thinking only without usage", `{"type":"assistant","message":{"id":"x","content":[{"type":"thinking","thinking":"hm"}]}}`, expectNone},
		{"cache creation without breakdown", `{"type":"assistant","message":{"id":"x","content":[],"usage":{"cache_creation_input_tokens":7}}}`,
			func(t *testing.T, evs []agent.Event) {
				if len(evs) != 1 || evs[0].Usage != (agent.Usage{CacheWrite5m: 7}) {
					t.Errorf("got %+v", evs)
				}
			}},
		{"tool result with text parts", `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"a"},{"type":"image"},{"type":"text","text":"b"}]}]}}`,
			func(t *testing.T, evs []agent.Event) {
				if len(evs) != 1 || evs[0].Text != "a\nb" || evs[0].IsError || evs[0].ToolUseID != "t1" {
					t.Errorf("got %+v", evs)
				}
			}},
		{"tool result truncated by runes", `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"` + long + `"}]}}`,
			func(t *testing.T, evs []agent.Event) {
				if len(evs) != 1 || len([]rune(evs[0].Text)) != 500 {
					t.Errorf("got %d runes, want 500", len([]rune(evs[0].Text)))
				}
			}},
		{"user echo with string content", `{"type":"user","message":{"role":"user","content":"hi"}}`, expectNone},
		{"error subtype without text", `{"type":"result","subtype":"error_max_turns","is_error":false}`,
			func(t *testing.T, evs []agent.Event) {
				if len(evs) != 1 || !evs[0].IsError || !strings.Contains(evs[0].Text, "turn limit") {
					t.Errorf("got %+v", evs)
				}
			}},
		{"success with is_error", `{"type":"result","subtype":"success","is_error":true,"result":"API error"}`,
			func(t *testing.T, evs []agent.Event) {
				if len(evs) != 1 || !evs[0].IsError || evs[0].Text != "API error" {
					t.Errorf("got %+v", evs)
				}
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var p parser
			tc.want(t, p.parse([]byte(tc.line), t0))
		})
	}
}

func expectNone(t *testing.T, evs []agent.Event) {
	t.Helper()
	if len(evs) != 0 {
		t.Errorf("got %d events %+v, want none", len(evs), evs)
	}
}

// The parser runs on untrusted-shaped output from a CLI that changes every
// release; it must never panic.
func FuzzParse(f *testing.F) {
	data, err := os.ReadFile("testdata/deny-bash.jsonl")
	if err != nil {
		f.Fatal(err)
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		f.Add(line)
	}
	f.Add([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","content":[1,2]}]}}`))
	f.Add([]byte(`{"type":"assistant","message":{"content":"x"}}`))
	f.Add([]byte(`{"type":"result","subtype":"success","total_cost_usd":0.1,"permission_denials":"x"}`))
	f.Fuzz(func(t *testing.T, line []byte) {
		var p parser
		p.parse(line, t0)
		p.parse(line, t0)
	})
}

func TestHookSettingsQuotesPathAndUsesJSON(t *testing.T) {
	// Paths contain spaces; an unquoted command would be split by the shell.
	got, err := hookSettings(`C:\Program Files\Atlas Commander\atlas-hook.exe`, 90*time.Second, "windows")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Hooks struct {
			PreToolUse []struct {
				Matcher string
				Hooks   []struct {
					Type, Command string
					Timeout       int
				}
			}
		}
	}
	if err := json.Unmarshal([]byte(got), &cfg); err != nil {
		t.Fatal(err)
	}
	h := cfg.Hooks.PreToolUse[0]
	if h.Matcher != "*" || h.Hooks[0].Type != "command" || h.Hooks[0].Timeout != 90 ||
		h.Hooks[0].Command != `"C:/Program Files/Atlas Commander/atlas-hook.exe"` {
		t.Errorf("got %s", got)
	}
}

// total_cost_usd is cumulative per process; the supervisor sums CostUSD, so
// each result must carry only its own turn or cost would be counted twice.
func TestParserResultCostIsPerTurnDelta(t *testing.T) {
	var p parser
	costs := []float64{0.03, 0.034, 0.01}
	want := []float64{0.03, 0.004, 0.01} // the last total drops: a restart
	for i, c := range costs {
		line := fmt.Sprintf(`{"type":"result","subtype":"success","total_cost_usd":%v}`, c)
		evs := p.parse([]byte(line), t0)
		got := evs[len(evs)-1].CostUSD
		if math.Abs(got-want[i]) > 1e-9 {
			t.Errorf("result %d: got cost %v, want %v", i, got, want[i])
		}
	}
}

// Subagent messages are not in result.usage, so counting them could push the
// emitted total above it.
func TestParserSkipsSubagentUsage(t *testing.T) {
	var p parser
	evs := p.parse([]byte(`{"type":"assistant","parent_tool_use_id":"toolu_1","message":{"id":"m1","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":50,"output_tokens":9}}}`), t0)
	if len(evs) != 1 || evs[0].Kind != agent.EventText {
		t.Errorf("got %+v, want only the text event", evs)
	}
}

// One field of an unexpected type, or one bad block, must cost only itself.
func TestParserToleratesOddTypes(t *testing.T) {
	var p parser
	evs := p.parse([]byte(`{"type":"result","subtype":"success","result":"ok","total_cost_usd":0.5,"permission_denials":"x","modelUsage":7,"num_turns":"two"}`), t0)
	if len(evs) != 1 || evs[0].Kind != agent.EventResult || evs[0].CostUSD != 0.5 || evs[0].Text != "ok" {
		t.Errorf("odd result field: got %+v, want a result with cost 0.5", evs)
	}
	evs = p.parse([]byte(`{"type":"assistant","message":{"id":"m","content":[{"type":"text","text":5},"junk",{"type":"tool_use","name":"Bash","id":"t1","input":{}}]}}`), t0)
	if len(evs) != 2 || evs[1].Kind != agent.EventToolUse || evs[1].Tool != "Bash" {
		t.Errorf("bad block: got %+v, want the good block still parsed", evs)
	}
}

func TestHookSettingsShellQuoting(t *testing.T) {
	// A path with a space, $ and ' must reach sh as one literal word.
	got, err := hookSettings(`/opt/a b/$HOME/it's/atlas-hook`, 0, "linux")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Hooks struct {
			PreToolUse []struct {
				Hooks []struct {
					Command string
					Timeout int
				}
			}
		}
	}
	if err := json.Unmarshal([]byte(got), &cfg); err != nil {
		t.Fatal(err)
	}
	h := cfg.Hooks.PreToolUse[0].Hooks[0]
	if want := `'/opt/a b/$HOME/it'\''s/atlas-hook'`; h.Command != want {
		t.Errorf("got command %s, want %s", h.Command, want)
	}
	if h.Timeout != 0 || strings.Contains(got, "timeout") {
		t.Errorf("zero timeout: got %s, want no timeout field", got)
	}
}
