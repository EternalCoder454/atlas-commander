// Package chat is the agent backend for providers that speak the
// OpenAI-compatible Chat Completions API: OpenAI itself, Gemini through its
// compatibility endpoint, and Ollama (the Local provider). One implementation
// serves all three; they differ only in name, address, key and default model.
// The agent is purely conversational and runs no tools.
//
// Goroutine model: each Session runs one goroutine that owns the history and
// the event channel. It sleeps until a message is queued, runs one streamed
// turn, and repeats. Send only appends to a mutex-guarded queue and wakes the
// goroutine, so it never blocks on a running turn. Stop and Kill cancel the
// session context, which aborts the in-flight request; the goroutine then
// emits EventExit and closes the events channel. Once the session is stopped,
// events are dropped rather than blocking on a consumer that is not reading,
// except the final EventExit, which always gets through. Models is a plain
// blocking call. No platform-specific code.
package chat

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"atlas-commander/internal/agent"
)

// Options configures one provider.
type Options struct {
	// Name is the backend name: agent.BackendOpenAI, BackendGemini or
	// BackendLocal.
	Name string
	// BaseURL is the API root; requests go to BaseURL + "/chat/completions"
	// and BaseURL + "/models".
	BaseURL string
	// APIKey is sent as a bearer token. "" is allowed only for Local.
	APIKey string
	// KeyEnv names the environment variable the key came from, so the
	// "no key" message can say where to put it. Optional.
	KeyEnv string
	// DefaultModel is used when a Spec has no model.
	DefaultModel string
}

const eventBufferSize = 256

// stopWait bounds how long Stop and Kill wait for the run goroutine; a
// variable so tests need not wait the full time.
var stopWait = 5 * time.Second

// modelsTimeout bounds a model listing, so a dead address does not hang the
// picker for the OS connect timeout.
const modelsTimeout = 8 * time.Second

type backend struct {
	o      Options
	client *http.Client
}

// New returns the backend for one provider.
func New(o Options) agent.Backend {
	o.BaseURL = strings.TrimRight(o.BaseURL, "/")
	// No overall timeout: a streamed reply can legitimately take minutes.
	// The session context bounds the request instead.
	return &backend{o: o, client: &http.Client{}}
}

func (b *backend) Name() string { return b.o.Name }

func (b *backend) Start(ctx context.Context, spec agent.Spec) (agent.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.o.Name != agent.BackendLocal && b.o.APIKey == "" {
		msg := "No " + providerName(b.o.Name) + " key was found."
		if b.o.KeyEnv != "" {
			msg += " Set $" + b.o.KeyEnv + " and restart Atlas Commander."
		}
		return nil, errors.New(msg)
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	model := spec.Model
	if model == "" {
		model = b.o.DefaultModel
	}
	sctx, cancel := context.WithCancel(context.Background())
	s := &session{
		b:      b,
		model:  model,
		id:     "chat-" + hex.EncodeToString(id[:]),
		ctx:    sctx,
		cancel: cancel,
		events: make(chan agent.Event, eventBufferSize),
		wake:   make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
	if spec.SystemPrompt != "" {
		s.history = append(s.history, message{Role: "system", Content: spec.SystemPrompt})
	}
	if spec.Prompt != "" {
		s.queue = append(s.queue, spec.Prompt)
	}
	go s.run()
	return s, nil
}

// message is one chat message on the wire.
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type session struct {
	b     *backend
	model string
	id    string

	ctx    context.Context
	cancel context.CancelFunc
	events chan agent.Event
	wake   chan struct{} // capacity 1: "the queue changed"
	done   chan struct{} // closed after the events channel is closed

	mu    sync.Mutex
	queue []string // messages waiting for the next turn

	// history is owned by the run goroutine.
	history []message
}

func (s *session) Events() <-chan agent.Event { return s.events }

var errEnded = errors.New("the session has ended")

// Send queues text. If a turn is running it is picked up by the next turn.
func (s *session) Send(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return errEnded
	}
	s.queue = append(s.queue, text)
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

func (s *session) Stop() error {
	s.cancel()
	// The run goroutine ends promptly once the context is cancelled, since
	// emit no longer blocks; the limit only guards against a stuck request.
	select {
	case <-s.done:
	case <-time.After(stopWait):
	}
	return nil
}

func (s *session) Kill() error { return s.Stop() }

// emit sends one event. It blocks while the consumer is slow, but not once the
// session is stopped: then the event is dropped, so Stop works even when
// nobody drains Events.
func (s *session) emit(e agent.Event) {
	e.Time = time.Now()
	select {
	case s.events <- e:
	case <-s.ctx.Done():
	}
}

// emitFinal sends the last event, EventExit, and never blocks. Nothing is
// sent after it, so if the buffer is full the oldest queued event is dropped
// to make room; only this goroutine sends, so the loop ends after at most
// one pass per concurrent receive.
func (s *session) emitFinal(e agent.Event) {
	e.Time = time.Now()
	for {
		select {
		case s.events <- e:
			return
		default:
		}
		select {
		case <-s.events:
		default:
		}
	}
}

func (s *session) run() {
	defer close(s.done)
	defer close(s.events)
	s.emit(agent.Event{Kind: agent.EventInit, SessionID: s.id, Model: s.model})
	for {
		if batch := s.takeQueue(); len(batch) > 0 {
			s.turn(batch)
			continue
		}
		select {
		case <-s.wake:
		case <-s.ctx.Done():
			s.emitFinal(agent.Event{Kind: agent.EventExit, Text: "stopped"})
			return
		}
	}
}

func (s *session) takeQueue() []string {
	if s.ctx.Err() != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.queue
	s.queue = nil
	return b
}

// streamChunk is the part of one SSE data line this backend reads.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// turn sends the queued messages as one user message and streams the reply.
// The text is emitted once, when the stream ends, like a finished block.
func (s *session) turn(batch []string) {
	start := time.Now()
	name := providerName(s.b.o.Name)
	s.history = append(s.history, message{Role: "user", Content: strings.Join(batch, "\n\n")})

	body, _ := json.Marshal(map[string]any{
		"model":          s.model,
		"messages":       s.history,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	})
	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, s.b.o.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		s.fail(start, "The address for "+name+" isn't valid.")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if k := s.b.o.APIKey; k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := s.b.client.Do(req)
	if err != nil {
		if s.ctx.Err() != nil {
			return // stopped; run emits the exit
		}
		s.fail(start, s.b.reachText(err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		s.fail(start, s.b.statusText(resp))
		return
	}

	var text strings.Builder
	var usage agent.Usage
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue // comments, event names, blank separators
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var ch streamChunk
		if json.Unmarshal([]byte(data), &ch) != nil {
			continue
		}
		if ch.Error != nil && ch.Error.Message != "" {
			s.fail(start, name+" stopped the reply: "+truncate(ch.Error.Message, 200))
			return
		}
		for _, c := range ch.Choices {
			text.WriteString(c.Delta.Content)
		}
		if u := ch.Usage; u != nil {
			usage = agent.Usage{Input: u.PromptTokens, Output: u.CompletionTokens}
			if d := u.PromptTokensDetails; d != nil && d.CachedTokens > 0 {
				// Cached tokens are part of prompt_tokens; split them out so
				// they are not priced at the full input rate.
				c := min(d.CachedTokens, usage.Input)
				usage.CacheRead, usage.Input = c, usage.Input-c
			}
		}
	}
	if err := sc.Err(); err != nil {
		if s.ctx.Err() != nil {
			return
		}
		s.fail(start, "The reply from "+name+" was cut off.")
		return
	}
	if s.ctx.Err() != nil {
		return
	}

	reply := text.String()
	if reply != "" {
		s.history = append(s.history, message{Role: "assistant", Content: reply})
		s.emit(agent.Event{Kind: agent.EventText, Text: reply})
	}
	if !usage.IsZero() {
		s.emit(agent.Event{Kind: agent.EventUsage, Usage: usage})
	}
	s.emit(agent.Event{Kind: agent.EventResult, Text: reply, Usage: usage, Duration: time.Since(start), Turns: 1})
}

// fail reports a turn that ended in an error with a single error EventResult
// (no EventError besides it, so the supervisor does not log it twice). The
// user message stays in history, so the next Send continues the conversation
// with it included.
func (s *session) fail(start time.Time, text string) {
	s.emit(agent.Event{Kind: agent.EventResult, IsError: true, Text: text, Duration: time.Since(start), Turns: 1})
}

// providerName is how a backend is named in messages to the user.
func providerName(backend string) string {
	switch backend {
	case agent.BackendOpenAI:
		return "OpenAI"
	case agent.BackendGemini:
		return "Gemini"
	case agent.BackendLocal:
		return "Ollama"
	}
	return "The provider"
}

// reachText says why a request never got an answer, in one plain sentence.
func (b *backend) reachText(err error) string {
	if b.o.Name == agent.BackendLocal {
		return "Ollama isn't running at " + strings.TrimSuffix(b.o.BaseURL, "/v1") + "."
	}
	var ue *url.Error
	if errors.As(err, &ue) && ue.Timeout() {
		return providerName(b.o.Name) + " took too long to answer."
	}
	return "Couldn't reach " + providerName(b.o.Name) + ". Check the internet connection."
}

// statusText turns a non-2xx response into one short sentence, without the
// raw body.
func (b *backend) statusText(resp *http.Response) string {
	name := providerName(b.o.Name)
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	msg := errorMessage(raw)
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		if b.o.Name == agent.BackendLocal {
			return "Ollama refused the request."
		}
		return name + " turned the key down."
	case http.StatusTooManyRequests:
		return name + " says there have been too many requests or the quota is used up. Try again later."
	case http.StatusNotFound:
		if b.o.Name == agent.BackendLocal {
			return "Ollama doesn't have that model. Pick one from the list, or pull it with ollama pull."
		}
		if msg != "" {
			return fmt.Sprintf("%s doesn't know that model (%s).", name, truncate(msg, 160))
		}
		return name + " doesn't know that model."
	}
	if msg != "" {
		return fmt.Sprintf("%s returned an error (%d): %s", name, resp.StatusCode, truncate(msg, 200))
	}
	return fmt.Sprintf("%s returned an error (%d).", name, resp.StatusCode)
}

// errorMessage pulls the message out of {"error":{"message":...}} (OpenAI,
// Ollama) or [{"error":{...}}] (Gemini sometimes wraps it in a list).
func errorMessage(raw []byte) string {
	type body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	var one body
	if json.Unmarshal(raw, &one) == nil && one.Error.Message != "" {
		return one.Error.Message
	}
	var many []body
	if json.Unmarshal(raw, &many) == nil && len(many) > 0 {
		return many[0].Error.Message
	}
	return ""
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Models lists the model ids the provider offers, sorted, from GET
// {BaseURL}/models. Ollama and Gemini serve the same list shape as OpenAI.
// Errors are short plain sentences, fit to show next to the picker.
func Models(ctx context.Context, o Options) ([]string, error) {
	b := &backend{o: o, client: &http.Client{}}
	b.o.BaseURL = strings.TrimRight(o.BaseURL, "/")
	if o.Name != agent.BackendLocal && o.APIKey == "" {
		return nil, errors.New("No " + providerName(o.Name) + " key was found, so its models can't be listed.")
	}
	ctx, cancel := context.WithTimeout(ctx, modelsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.o.BaseURL+"/models", nil)
	if err != nil {
		return nil, errors.New("The address for " + providerName(o.Name) + " isn't valid.")
	}
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, errors.New(b.reachText(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, errors.New(b.statusText(resp))
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&list); err != nil {
		return nil, errors.New(providerName(o.Name) + " sent a model list that couldn't be read.")
	}
	var out []string
	for _, m := range list.Data {
		// Gemini lists "models/gemini-…"; the chat endpoint takes the bare id.
		if id := strings.TrimPrefix(m.ID, "models/"); id != "" {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}
