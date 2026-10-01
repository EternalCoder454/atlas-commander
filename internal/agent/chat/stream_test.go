package chat

import (
	"io"
	"net/http"
	"testing"
	"time"

	"atlas-commander/internal/agent"
)

// A connection that drops mid-reply ends the stream cleanly from the client's
// side. Treating the half reply as finished would put it in the history and
// the audit log as if the model had said it, so it must be reported.
func TestStreamWithoutDoneIsCutOff(t *testing.T) {
	cut := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		delta(w, "half a sent")
	}
	f := newFakeAPI(t, cut)
	s := start(t, opts(f), agent.Spec{Prompt: "x"})
	evs := until(t, s, agent.EventResult)
	last := evs[len(evs)-1]
	if !last.IsError || last.Text != "The reply from OpenAI was cut off." {
		t.Errorf("got %+v, want the cut-off error", last)
	}
	for _, e := range evs {
		if e.Kind == agent.EventText {
			t.Errorf("got a text event %q, want none for a cut-off reply", e.Text)
		}
	}
}

// A 200 with nothing in it (a proxy answering instead of the provider) must
// not look like a model that chose to say nothing.
func TestStreamWithNoDataIsAnError(t *testing.T) {
	empty := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, ": keep-alive\n\n")
	}
	f := newFakeAPI(t, empty)
	s := start(t, opts(f), agent.Spec{Prompt: "x"})
	evs := until(t, s, agent.EventResult)
	if last := evs[len(evs)-1]; !last.IsError || last.Text != "OpenAI sent an empty reply." {
		t.Errorf("got %+v, want the empty-reply error", last)
	}
}

// A finish_reason is a complete reply even when the server never sends [DONE],
// which some compatible servers don't.
func TestFinishReasonWithoutDoneIsComplete(t *testing.T) {
	h := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		delta(w, "hi")
		data(w, `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	}
	f := newFakeAPI(t, h)
	s := start(t, opts(f), agent.Spec{Prompt: "x"})
	evs := until(t, s, agent.EventResult)
	if last := evs[len(evs)-1]; last.IsError || last.Text != "hi" {
		t.Errorf("got %+v, want the reply hi", last)
	}
}

// A server that goes silent mid-stream would hold the turn forever; the stall
// timer must cancel it and report a cut-off reply.
func TestStalledStreamIsCancelled(t *testing.T) {
	old := stallTimeout
	stallTimeout = 200 * time.Millisecond
	t.Cleanup(func() { stallTimeout = old })
	stall := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		delta(w, "start")
		<-r.Context().Done()
	}
	f := newFakeAPI(t, stall)
	s := start(t, opts(f), agent.Spec{Prompt: "x"})
	evs := until(t, s, agent.EventResult)
	if last := evs[len(evs)-1]; !last.IsError || last.Text != "The reply from OpenAI was cut off." {
		t.Errorf("got %+v, want the cut-off error", last)
	}
}
