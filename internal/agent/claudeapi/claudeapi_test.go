package claudeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"atlas-commander/internal/agent"
)

// fakeAPI serves /v1/messages from a list of handlers, one per request, and
// keeps the decoded request bodies for assertions.
type fakeAPI struct {
	srv *httptest.Server

	mu       sync.Mutex
	bodies   []map[string]any
	handlers []func(w http.ResponseWriter, r *http.Request)
}

func newFakeAPI(t *testing.T, handlers ...func(w http.ResponseWriter, r *http.Request)) *fakeAPI {
	t.Helper()
	f := &fakeAPI{handlers: handlers}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		f.mu.Lock()
		i := len(f.bodies)
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()
		if i >= len(f.handlers) {
			http.Error(w, `{"type":"error","error":{"type":"api_error","message":"unexpected request"}}`, 400)
			return
		}
		f.handlers[i](w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) body(i int) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[i]
}

func sse(w http.ResponseWriter, event string, data string) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	w.(http.Flusher).Flush()
}

func startSSE(w http.ResponseWriter, in, out int64) {
	w.Header().Set("Content-Type", "text/event-stream")
	sse(w, "message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":3,"cache_creation_input_tokens":0}}}`, in, out))
}

func textBlock(w http.ResponseWriter, idx int, parts ...string) {
	sse(w, "content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, idx))
	for _, p := range parts {
		sse(w, "content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":%q}}`, idx, p))
	}
	sse(w, "content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, idx))
}

func endSSE(w http.ResponseWriter, stop string, out int64) {
	sse(w, "message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q,"stop_sequence":null},"usage":{"output_tokens":%d}}`, stop, out))
	sse(w, "message_stop", `{"type":"message_stop"}`)
}

// simpleReply is a one-text-block reply with start usage (in, 1) and final output out.
func simpleReply(text string, in, out int64) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		startSSE(w, in, 1)
		textBlock(w, 0, text[:1], text[1:])
		endSSE(w, "end_turn", out)
	}
}

func start(t *testing.T, f *fakeAPI, spec agent.Spec) agent.Session {
	t.Helper()
	b := New(Options{APIKey: "test-key", BaseURL: f.srv.URL})
	if b.Name() != agent.BackendClaudeAPI {
		t.Fatalf("got name %q", b.Name())
	}
	s, err := b.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Kill() })
	return s
}

// next returns the next event or fails after a timeout.
func next(t *testing.T, s agent.Session) agent.Event {
	t.Helper()
	select {
	case e, ok := <-s.Events():
		if !ok {
			t.Fatal("events closed early")
		}
		return e
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for an event")
	}
	return agent.Event{}
}

// until collects events up to and including the first one of kind.
func until(t *testing.T, s agent.Session, kind agent.EventKind) []agent.Event {
	t.Helper()
	var evs []agent.Event
	for {
		e := next(t, s)
		evs = append(evs, e)
		if e.Kind == kind {
			return evs
		}
	}
}

func sumUsage(evs []agent.Event) agent.Usage {
	var u agent.Usage
	for _, e := range evs {
		if e.Kind == agent.EventUsage {
			u = u.Add(e.Usage)
		}
	}
	return u
}

func texts(evs []agent.Event) []string {
	var out []string
	for _, e := range evs {
		if e.Kind == agent.EventText {
			out = append(out, e.Text)
		}
	}
	return out
}

// Billing depends on usage increments summing to the message totals: input
// and cache come from message_start, output grows from 1 to the cumulative
// 40 in message_delta, and text is emitted once per block, not per delta.
func TestTurnEmitsTextOnceAndUsageIncrementsSum(t *testing.T) {
	f := newFakeAPI(t, simpleReply("Hello there", 25, 40))
	s := start(t, f, agent.Spec{Prompt: "hi", SystemPrompt: "be brief"})

	evs := until(t, s, agent.EventResult)
	if evs[0].Kind != agent.EventInit || !strings.HasPrefix(evs[0].SessionID, "api-") || evs[0].Model != "claude-opus-5-5" {
		t.Errorf("init: got %+v", evs[0])
	}
	if got := texts(evs); len(got) != 1 || got[0] != "Hello there" {
		t.Errorf("got text events %q, want one \"Hello there\"", got)
	}
	if got, want := sumUsage(evs), (agent.Usage{Input: 25, Output: 40, CacheRead: 3}); got != want {
		t.Errorf("got usage sum %+v, want %+v", got, want)
	}
	res := evs[len(evs)-1]
	if res.IsError || res.Text != "Hello there" || res.Turns != 1 {
		t.Errorf("result: got %+v", res)
	}

	b := f.body(0)
	if b["model"] != "claude-opus-5-5" || b["max_tokens"] != float64(64000) {
		t.Errorf("request: got model %v max_tokens %v", b["model"], b["max_tokens"])
	}
	if _, ok := b["thinking"]; ok {
		t.Error("request has a thinking field, want none")
	}
	if sys, _ := json.Marshal(b["system"]); !strings.Contains(string(sys), "be brief") {
		t.Errorf("system: got %s", sys)
	}
}

// A message sent while a turn runs must not be lost or interleaved: it waits
// for the turn, then goes out with the full history, thinking block intact.
func TestSendDuringTurnRunsAfterwardsWithHistory(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	first := func(w http.ResponseWriter, r *http.Request) {
		startSSE(w, 10, 1)
		sse(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`)
		sse(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`)
		sse(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"SIG123"}}`)
		sse(w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
		close(started)
		<-release
		textBlock(w, 1, "one")
		endSSE(w, "end_turn", 5)
	}
	f := newFakeAPI(t, first, simpleReply("two!", 30, 6))
	s := start(t, f, agent.Spec{Prompt: "first", Model: "claude-sonnet-5-5"})

	<-started
	if err := s.Send("second"); err != nil {
		t.Fatal(err)
	}
	close(release)
	evs := until(t, s, agent.EventResult)
	evs = append(evs, until(t, s, agent.EventResult)...)

	var results []string
	for _, e := range evs {
		if e.Kind == agent.EventResult {
			results = append(results, e.Text)
		}
	}
	if len(results) != 2 || results[0] != "one" || results[1] != "two!" {
		t.Fatalf("got results %q, want [one two!]", results)
	}
	if f.body(1)["model"] != "claude-sonnet-5-5" {
		t.Errorf("got model %v", f.body(1)["model"])
	}
	raw, _ := json.Marshal(f.body(1)["messages"])
	for _, want := range []string{`"first"`, `"second"`, `"SIG123"`, `"hmm"`, `"one"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("second request messages missing %s: %s", want, raw)
		}
	}
	msgs := f.body(1)["messages"].([]any)
	if len(msgs) != 3 {
		t.Errorf("got %d messages, want user, assistant, user", len(msgs))
	}
}

// A refusal is an error result carrying the explanation, and the refused
// reply stays out of history so the conversation can continue.
func TestRefusalEndsTurnAsErrorWithExplanation(t *testing.T) {
	refuse := func(w http.ResponseWriter, r *http.Request) {
		startSSE(w, 10, 1)
		sse(w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null,"stop_details":{"type":"refusal","category":"cyber","explanation":"not that"}},"usage":{"output_tokens":2}}`)
		sse(w, "message_stop", `{"type":"message_stop"}`)
	}
	f := newFakeAPI(t, refuse, simpleReply("ok", 5, 2))
	s := start(t, f, agent.Spec{Prompt: "bad"})
	evs := until(t, s, agent.EventResult)
	res := evs[len(evs)-1]
	if !res.IsError || !strings.Contains(res.Text, "not that") {
		t.Errorf("got %+v, want error result with the explanation", res)
	}
	s.Send("fine")
	until(t, s, agent.EventResult)
	for _, m := range f.body(1)["messages"].([]any) {
		if m.(map[string]any)["role"] == "assistant" {
			t.Error("refused reply was kept in history")
		}
	}
}

// An API error must surface as one error result and leave the
// session usable; the failed user message stays in history for the retry.
func TestAPIErrorEmitsOneErrorResultThenRecovers(t *testing.T) {
	bad := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long"}}`)
	}
	f := newFakeAPI(t, bad, simpleReply("ok", 5, 2))
	s := start(t, f, agent.Spec{Prompt: "x"})
	evs := until(t, s, agent.EventResult)
	if len(evs) != 2 || evs[1].Kind != agent.EventResult || !strings.Contains(evs[1].Text, "prompt is too long") || !strings.Contains(evs[1].Text, "400") {
		t.Fatalf("got %+v, want init, error result", evs)
	}
	if !evs[1].IsError {
		t.Error("result: got IsError false, want true")
	}
	s.Send("again")
	evs = until(t, s, agent.EventResult)
	if last := evs[len(evs)-1]; last.IsError || last.Text != "ok" {
		t.Errorf("after recovery: got %+v", last)
	}
}

// Stop has to interrupt a request that is still streaming, end with a
// "stopped" exit and close the channel.
func TestStopCancelsInFlightStream(t *testing.T) {
	started := make(chan struct{})
	hang := func(w http.ResponseWriter, r *http.Request) {
		startSSE(w, 10, 1)
		sse(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		close(started)
		<-r.Context().Done()
	}
	f := newFakeAPI(t, hang)
	s := start(t, f, agent.Spec{Prompt: "x"})
	<-started

	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return")
	}
	var last agent.Event
	for e := range s.Events() {
		if e.Kind == agent.EventResult {
			t.Error("got a result for a stopped turn")
		}
		last = e
	}
	if last.Kind != agent.EventExit || last.Text != "stopped" {
		t.Errorf("got last event %+v, want stopped exit", last)
	}
	if err := s.Send("late"); err == nil {
		t.Error("Send after Stop: got nil error, want one")
	}
	if err := s.Stop(); err != nil || s.Kill() != nil {
		t.Error("Stop and Kill must be idempotent")
	}
}

// A consumer that never reads must not be able to wedge Stop: the session
// fills the event buffer, Stop still returns, and the exit is still delivered.
func TestStopReturnsWhenNobodyDrainsEvents(t *testing.T) {
	var replies []func(http.ResponseWriter, *http.Request)
	for i := 0; i < 150; i++ {
		replies = append(replies, simpleReply("ok", 5, 2))
	}
	f := newFakeAPI(t, replies...)
	s := start(t, f, agent.Spec{Prompt: "x"})
	// Each turn is a few events, and Sends sent mid-turn are batched, so
	// pace them to run enough turns to fill the buffer.
	for i := 0; i < 100; i++ {
		s.Send("more")
		time.Sleep(30 * time.Millisecond)
	}

	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return with a full event buffer")
	}
	var last agent.Event
	for e := range s.Events() {
		last = e
	}
	if last.Kind != agent.EventExit {
		t.Errorf("got last event %+v, want the exit", last)
	}
}
