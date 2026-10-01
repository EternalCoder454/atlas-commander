// Package claudeapi is the agent backend that talks to the Claude API
// directly: a plain conversational agent with no tools in v1.
//
// Goroutine model: each Session runs one goroutine that owns the history and
// the event channel. It sleeps until a message is queued, runs one streamed
// turn, and repeats. Send only appends to a mutex-guarded queue and wakes the
// goroutine, so it never blocks on a running turn. Stop and Kill cancel the
// session context, which aborts the in-flight request; the goroutine then
// emits EventExit and closes the events channel. Once the session is stopped,
// events are dropped rather than blocking on a consumer that is not reading,
// except the final EventExit, which always gets through. No platform-specific code.
package claudeapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"atlas-commander/internal/agent"
)

// Options configures the backend.
type Options struct {
	// APIKey is the Anthropic API key; "" lets the SDK read ANTHROPIC_API_KEY.
	APIKey string
	// BaseURL overrides the API address; tests point it at a local server.
	BaseURL string
	// MaxTokens bounds one response; 0 means 64000.
	MaxTokens int64
}

const (
	defaultModel     = "claude-opus-5-5"
	defaultMaxTokens = 64000
	eventBufferSize  = 256
)

// stopWait bounds how long Stop and Kill wait for the run goroutine; a
// variable so tests need not wait the full time.
var stopWait = 5 * time.Second

type backend struct {
	client    anthropic.Client
	maxTokens int64
}

// New returns the Claude API backend.
func New(o Options) agent.Backend {
	var opts []option.RequestOption
	if o.APIKey != "" {
		opts = append(opts, option.WithAPIKey(o.APIKey))
	}
	if o.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(o.BaseURL))
	}
	max := o.MaxTokens
	if max <= 0 {
		max = defaultMaxTokens
	}
	return &backend{client: anthropic.NewClient(opts...), maxTokens: max}
}

func (b *backend) Name() string { return agent.BackendClaudeAPI }

func (b *backend) Start(ctx context.Context, spec agent.Spec) (agent.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	model := spec.Model
	if model == "" {
		model = defaultModel
	}
	sctx, cancel := context.WithCancel(context.Background())
	s := &session{
		client:    b.client,
		model:     model,
		system:    spec.SystemPrompt,
		maxTokens: b.maxTokens,
		id:        "api-" + hex.EncodeToString(id[:]),
		ctx:       sctx,
		cancel:    cancel,
		events:    make(chan agent.Event, eventBufferSize),
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
	}
	if spec.Prompt != "" {
		s.queue = append(s.queue, spec.Prompt)
	}
	go s.run()
	return s, nil
}

type session struct {
	client    anthropic.Client
	model     string
	system    string
	maxTokens int64
	id        string

	ctx    context.Context
	cancel context.CancelFunc
	events chan agent.Event
	wake   chan struct{} // capacity 1: "the queue changed"
	done   chan struct{} // closed after the events channel is closed

	mu    sync.Mutex
	queue []string // messages waiting for the next turn

	// history is owned by the run goroutine. It is append-only: the API
	// caches by prefix, and the thinking blocks in it must come back
	// byte for byte.
	history []anthropic.MessageParam
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

// turn sends the queued messages as one user message and streams the reply.
func (s *session) turn(batch []string) {
	start := time.Now()
	blocks := make([]anthropic.ContentBlockParamUnion, len(batch))
	for i, t := range batch {
		blocks[i] = anthropic.NewTextBlock(t)
	}
	s.history = append(s.history, anthropic.NewUserMessage(blocks...))

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(s.model),
		MaxTokens: s.maxTokens,
		Messages:  s.history,
	}
	if s.system != "" {
		params.System = []anthropic.TextBlockParam{{Text: s.system}}
	}
	// No Thinking field: Opus 5.5 and Fable 5.1 always think, and sending
	// any thinking config to them can be a 400. Haiku defaults to none.

	stream := s.client.Messages.NewStreaming(s.ctx, params)
	defer stream.Close()
	var msg anthropic.Message
	var sent agent.Usage // cumulative usage already emitted for this message
	for stream.Next() {
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			s.fail(start, "The reply from the Claude API could not be read: "+err.Error())
			return
		}
		switch ev.Type {
		case "message_start", "message_delta":
			// Counts on the message are cumulative, so emit only what is new.
			cur := usageOf(msg.Usage)
			if d := diff(cur, sent); !d.IsZero() {
				s.emit(agent.Event{Kind: agent.EventUsage, Usage: d})
			}
			sent = maxUsage(sent, cur)
		case "content_block_stop":
			if int(ev.Index) < len(msg.Content) && msg.Content[ev.Index].Type == "text" {
				s.emit(agent.Event{Kind: agent.EventText, Text: msg.Content[ev.Index].Text})
			}
		}
	}
	if err := stream.Err(); err != nil {
		if s.ctx.Err() != nil {
			return // stopped; run emits the exit
		}
		s.fail(start, apiErrorText(err))
		return
	}

	res := agent.Event{Kind: agent.EventResult, Duration: time.Since(start), Turns: 1}
	var texts []string
	for _, c := range msg.Content {
		if c.Type == "text" {
			texts = append(texts, c.Text)
		}
	}
	res.Text = strings.Join(texts, "\n")
	if msg.StopReason == anthropic.StopReasonRefusal {
		// The refused reply is left out of history: it may be partial or
		// empty, and an empty assistant turn is rejected on the next call.
		res.IsError = true
		res.Text = refusalText(msg)
	} else if len(msg.Content) > 0 {
		s.history = append(s.history, msg.ToParam())
	}
	s.emit(res)
}

// fail reports a turn that ended in an error with a single error EventResult
// (no EventError besides it, so the supervisor does not log it twice). The user message stays in
// history, so the next Send continues the conversation with it included.
func (s *session) fail(start time.Time, text string) {
	s.emit(agent.Event{Kind: agent.EventResult, IsError: true, Text: text, Duration: time.Since(start), Turns: 1})
}

func refusalText(msg anthropic.Message) string {
	d := msg.StopDetails
	switch {
	case d.Explanation != "":
		return "The model declined this request: " + d.Explanation
	case d.Category != "":
		return fmt.Sprintf("The model declined this request (%s).", d.Category)
	}
	return "The model declined this request."
}

// apiErrorText turns an SDK error into one short sentence, without the raw
// request dump the SDK puts in Error().
func apiErrorText(err error) string {
	var ae *anthropic.Error
	if errors.As(err, &ae) {
		var body struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal([]byte(ae.RawJSON()), &body)
		if m := body.Error.Message; m != "" {
			return fmt.Sprintf("The Claude API returned an error (%d): %s", ae.StatusCode, m)
		}
		return fmt.Sprintf("The Claude API returned an error (%d).", ae.StatusCode)
	}
	return "The Claude API could not be reached: " + err.Error()
}

func usageOf(u anthropic.Usage) agent.Usage {
	out := agent.Usage{
		Input:        u.InputTokens,
		Output:       u.OutputTokens,
		CacheRead:    u.CacheReadInputTokens,
		CacheWrite5m: u.CacheCreation.Ephemeral5mInputTokens,
		CacheWrite1h: u.CacheCreation.Ephemeral1hInputTokens,
	}
	if out.CacheWrite5m+out.CacheWrite1h == 0 {
		out.CacheWrite5m = u.CacheCreationInputTokens
	}
	return out
}

func diff(a, b agent.Usage) agent.Usage {
	pos := func(n int64) int64 { return max(n, 0) }
	return agent.Usage{
		Input:        pos(a.Input - b.Input),
		Output:       pos(a.Output - b.Output),
		CacheRead:    pos(a.CacheRead - b.CacheRead),
		CacheWrite5m: pos(a.CacheWrite5m - b.CacheWrite5m),
		CacheWrite1h: pos(a.CacheWrite1h - b.CacheWrite1h),
	}
}

func maxUsage(a, b agent.Usage) agent.Usage {
	return agent.Usage{
		Input:        max(a.Input, b.Input),
		Output:       max(a.Output, b.Output),
		CacheRead:    max(a.CacheRead, b.CacheRead),
		CacheWrite5m: max(a.CacheWrite5m, b.CacheWrite5m),
		CacheWrite1h: max(a.CacheWrite1h, b.CacheWrite1h),
	}
}
