package chat

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

// fakeAPI serves /chat/completions and /models from httptest, one handler per
// chat request, and keeps the decoded request bodies and headers.
type fakeAPI struct {
	srv *httptest.Server

	mu       sync.Mutex
	bodies   []map[string]any
	auth     []string
	handlers []func(w http.ResponseWriter, r *http.Request)
	models   func(w http.ResponseWriter, r *http.Request)
}

func newFakeAPI(t *testing.T, handlers ...func(w http.ResponseWriter, r *http.Request)) *fakeAPI {
	t.Helper()
	f := &fakeAPI{handlers: handlers}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		f.mu.Lock()
		i := len(f.bodies)
		f.bodies = append(f.bodies, body)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		if i >= len(f.handlers) {
			http.Error(w, `{"error":{"message":"unexpected request"}}`, 400)
			return
		}
		f.handlers[i](w, r)
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if f.models == nil {
			http.NotFound(w, r)
			return
		}
		f.models(w, r)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) body(i int) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[i]
}

func data(w http.ResponseWriter, s string) {
	fmt.Fprintf(w, "data: %s\n\n", s)
	w.(http.Flusher).Flush()
}

func delta(w http.ResponseWriter, text string) {
	data(w, fmt.Sprintf(`{"choices":[{"index":0,"delta":{"content":%q}}]}`, text))
}

// reply streams text in two pieces, then a usage-only chunk, then [DONE].
func reply(text string, prompt, completion, cached int64) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		delta(w, text[:1])
		delta(w, text[1:])
		data(w, `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		data(w, fmt.Sprintf(`{"choices":[],"usage":{"prompt_tokens":%d,"completion_tokens":%d,"prompt_tokens_details":{"cached_tokens":%d}}}`, prompt, completion, cached))
		data(w, "[DONE]")
	}
}

func opts(f *fakeAPI) Options {
	return Options{Name: agent.BackendOpenAI, BaseURL: f.srv.URL + "/v1", APIKey: "test-key", DefaultModel: "gpt-test"}
}

func start(t *testing.T, o Options, spec agent.Spec) agent.Session {
	t.Helper()
	b := New(o)
	if b.Name() != o.Name {
		t.Fatalf("got name %q, want %q", b.Name(), o.Name)
	}
	s, err := b.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Kill() })
	return s
}

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

// Billing and the transcript depend on one text event per turn, usage counted
// once with cached tokens split out of the input, and the request carrying
// the model, the system prompt and the streaming options.
func TestTurnStreamsTextAndReportsUsage(t *testing.T) {
	f := newFakeAPI(t, reply("Hello there", 25, 40, 10))
	s := start(t, opts(f), agent.Spec{Prompt: "hi", SystemPrompt: "be brief"})

	evs := until(t, s, agent.EventResult)
	if evs[0].Kind != agent.EventInit || !strings.HasPrefix(evs[0].SessionID, "chat-") || evs[0].Model != "gpt-test" {
		t.Errorf("init: got %+v", evs[0])
	}
	var texts []string
	var usage agent.Usage
	for _, e := range evs {
		switch e.Kind {
		case agent.EventText:
			texts = append(texts, e.Text)
		case agent.EventUsage:
			usage = usage.Add(e.Usage)
		}
	}
	if len(texts) != 1 || texts[0] != "Hello there" {
		t.Errorf("got text events %q, want one \"Hello there\"", texts)
	}
	if want := (agent.Usage{Input: 15, Output: 40, CacheRead: 10}); usage != want {
		t.Errorf("got usage %+v, want %+v", usage, want)
	}
	res := evs[len(evs)-1]
	if res.IsError || res.Text != "Hello there" || res.Turns != 1 || res.CostUSD != 0 {
		t.Errorf("result: got %+v", res)
	}

	b := f.body(0)
	if b["model"] != "gpt-test" || b["stream"] != true {
		t.Errorf("request: got model %v stream %v", b["model"], b["stream"])
	}
	if so, _ := json.Marshal(b["stream_options"]); string(so) != `{"include_usage":true}` {
		t.Errorf("stream_options: got %s", so)
	}
	if msgs, _ := json.Marshal(b["messages"]); !strings.Contains(string(msgs), `"system"`) || !strings.Contains(string(msgs), "be brief") {
		t.Errorf("messages: got %s", msgs)
	}
	if f.auth[0] != "Bearer test-key" {
		t.Errorf("got auth %q, want the bearer key", f.auth[0])
	}
}

// A message sent while a turn runs must not be lost: it waits for the turn,
// then goes out with the whole conversation so far.
func TestSendDuringTurnRunsAfterwardsWithHistory(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	first := func(w http.ResponseWriter, r *http.Request) {
		delta(w, "one")
		close(started)
		<-release
		data(w, "[DONE]")
	}
	f := newFakeAPI(t, first, reply("two!", 30, 6, 0))
	s := start(t, opts(f), agent.Spec{Prompt: "first", Model: "gpt-other"})

	<-started
	if err := s.Send("second"); err != nil {
		t.Fatal(err)
	}
	close(release)
	until(t, s, agent.EventResult)
	evs := until(t, s, agent.EventResult)
	if last := evs[len(evs)-1]; last.Text != "two!" {
		t.Errorf("got second result %q, want two!", last.Text)
	}
	if f.body(1)["model"] != "gpt-other" {
		t.Errorf("got model %v", f.body(1)["model"])
	}
	msgs := f.body(1)["messages"].([]any)
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	if strings.Join(roles, ",") != "user,assistant,user" {
		t.Errorf("got roles %v, want user, assistant, user", roles)
	}
}

// A refused key is the commonest failure; the user must get one plain
// sentence, the session must survive, and the next turn must work.
func TestHTTPErrorBecomesPlainResultThenRecovers(t *testing.T) {
	bad := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"message":"Incorrect API key provided: sk-abc"}}`)
	}
	f := newFakeAPI(t, bad, reply("ok", 5, 2, 0))
	s := start(t, opts(f), agent.Spec{Prompt: "x"})
	evs := until(t, s, agent.EventResult)
	if len(evs) != 2 || !evs[1].IsError || evs[1].Text != "OpenAI turned the key down." {
		t.Fatalf("got %+v, want init, then the key error", evs)
	}
	s.Send("again")
	evs = until(t, s, agent.EventResult)
	if last := evs[len(evs)-1]; last.IsError || last.Text != "ok" {
		t.Errorf("after recovery: got %+v", last)
	}
}

// Local with nothing listening must say so with the address, not show a Go
// network error.
func TestLocalNotRunningSaysSo(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()
	o := Options{Name: agent.BackendLocal, BaseURL: addr + "/v1", DefaultModel: "m"}
	s := start(t, o, agent.Spec{Prompt: "x"})
	evs := until(t, s, agent.EventResult)
	if got, want := evs[len(evs)-1].Text, "Ollama isn't running at "+addr+"."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A cloud provider without a key must refuse to start, naming the variable.
func TestStartWithoutKeyFailsPlainly(t *testing.T) {
	_, err := New(Options{Name: agent.BackendGemini, BaseURL: "http://x", KeyEnv: "GEMINI_API_KEY"}).Start(context.Background(), agent.Spec{})
	if err == nil || !strings.Contains(err.Error(), "$GEMINI_API_KEY") {
		t.Errorf("got %v, want an error naming $GEMINI_API_KEY", err)
	}
}

// Stop has to interrupt a request that is still streaming, end with a
// "stopped" exit and close the channel.
func TestStopCancelsInFlightStream(t *testing.T) {
	started := make(chan struct{})
	hang := func(w http.ResponseWriter, r *http.Request) {
		delta(w, "par")
		close(started)
		<-r.Context().Done()
	}
	f := newFakeAPI(t, hang)
	s := start(t, opts(f), agent.Spec{Prompt: "x"})
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
		if e.Kind == agent.EventResult || e.Kind == agent.EventText {
			t.Errorf("got %v for a stopped turn", e.Kind)
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
		replies = append(replies, reply("ok", 5, 2, 0))
	}
	f := newFakeAPI(t, replies...)
	s := start(t, opts(f), agent.Spec{Prompt: "x"})
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

// The model picker relies on a sorted list of bare ids; Gemini prefixes its
// ids with "models/".
func TestModelsListsSortedBareIDs(t *testing.T) {
	f := newFakeAPI(t)
	f.models = func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("got auth %q", r.Header.Get("Authorization"))
		}
		io.WriteString(w, `{"object":"list","data":[{"id":"models/b-model"},{"id":"a-model"}]}`)
	}
	got, err := Models(context.Background(), opts(f))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "a-model,b-model" {
		t.Errorf("got %v, want [a-model b-model]", got)
	}
}

// Listing errors are shown next to the picker, so they must be plain.
func TestModelsErrorsArePlain(t *testing.T) {
	f := newFakeAPI(t)
	f.models = func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", 401) }
	if _, err := Models(context.Background(), opts(f)); err == nil || err.Error() != "OpenAI turned the key down." {
		t.Errorf("got %v, want the key error", err)
	}
	o := opts(f)
	o.APIKey = ""
	if _, err := Models(context.Background(), o); err == nil || !strings.Contains(err.Error(), "No OpenAI key") {
		t.Errorf("got %v, want a missing-key error", err)
	}
	f.srv.Close()
	l := Options{Name: agent.BackendLocal, BaseURL: f.srv.URL + "/v1"}
	if _, err := Models(context.Background(), l); err == nil || !strings.Contains(err.Error(), "Ollama isn't running at "+f.srv.URL) {
		t.Errorf("got %v, want the not-running error", err)
	}
}
