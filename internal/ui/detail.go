package ui

import (
	"fmt"
	"html"
	"strings"
	"time"

	qt "github.com/mappu/miqt/qt6"

	"atlas-commander/internal/agent"
	"atlas-commander/internal/fleet"
)

// detailPage is one agent: its numbers, a transcript that follows the
// session live, a box to prompt or redirect it, and the controls.
type detailPage struct {
	app *App
	w   *qt.QWidget
	id  string

	title  *liveLabel
	status *liveLabel
	meta   *liveLabel
	stats  map[string]*liveLabel
	burn   *Chart

	approval     *qt.QFrame
	approvalText *liveLabel
	approvalID   string

	log  *qt.QPlainTextEdit
	next int // transcript index to fetch from

	input *qt.QPlainTextEdit
	send  *qt.QPushButton
	btn   struct{ hold, resume, stop, kill *qt.QPushButton }

	lastStatus fleet.Status

	// What the widgets show now, so the 250 ms tick only touches those whose
	// value changed.
	approvalBtns       []*qt.QPushButton
	approvalOn         bool
	approvalShown      bool
	ctlOn              [4]bool // hold, resume, stop, kill
	sendText, sendHint string
}

var detailStats = []string{"Tokens", "Cost", "Input / output", "All time", "Cache read / write", "Latency", "Session", "Started", "Working dir", "Worktree"}

func newDetailPage(a *App) *detailPage {
	d := &detailPage{app: a, w: qt.NewQWidget2(), stats: map[string]*liveLabel{}}
	l := newPageLayout(d.w)

	nav := qt.NewQHBoxLayout2()
	back := qt.NewQPushButton3("← Board")
	back.OnClicked(func() { a.side.active = ""; a.side.select_("board") })
	nav.AddWidget(back.QWidget)
	nav.AddStretch()
	d.btn.hold = qt.NewQPushButton3("Hold")
	d.btn.hold.SetToolTip("Stop the agent at its next tool call")
	d.btn.hold.OnClicked(func() { a.report(a.ctl.Hold(d.id)) })
	d.btn.resume = qt.NewQPushButton3("Resume")
	d.btn.resume.OnClicked(func() { a.report(a.ctl.Resume(d.id)) })
	d.btn.stop = qt.NewQPushButton3("Stop")
	d.btn.stop.OnClicked(func() { a.report(a.ctl.Stop(d.id)) })
	d.btn.kill = qt.NewQPushButton3("Kill")
	setProp(d.btn.kill.QWidget, "danger", true)
	d.btn.kill.OnClicked(func() { a.report(a.ctl.Kill(d.id)) })
	for _, b := range []*qt.QPushButton{d.btn.hold, d.btn.resume, d.btn.stop, d.btn.kill} {
		nav.AddWidget(b.QWidget)
	}
	l.AddLayout(nav.QLayout)

	head := qt.NewQHBoxLayout2()
	head.SetSpacing(12)
	t := pageTitle("")
	d.title = &liveLabel{L: t}
	head.AddWidget(t.QWidget)
	d.status = newLiveLabel("")
	head.AddWidget(d.status.L.QWidget)
	head.AddStretch()
	l.AddLayout(head.QLayout)
	d.meta = newLiveLabel("")
	setProp(d.meta.L.QWidget, "caption", true)
	l.AddWidget(d.meta.L.QWidget)

	// Stats grid: two label/value pairs per row, as Monitor's statGrid.
	top := qt.NewQHBoxLayout2()
	top.SetSpacing(18)
	grid := qt.NewQGridLayout2()
	grid.SetHorizontalSpacing(12)
	grid.SetVerticalSpacing(8)
	for i, k := range detailStats {
		r, c := i/2, (i%2)*2
		grid.AddWidget2(caption(k).QWidget, r, c)
		v := newLiveLabel("–")
		setProp(v.L.QWidget, "mono", true)
		v.L.SetTextInteractionFlags(qt.TextSelectableByMouse)
		grid.AddWidget2(v.L.QWidget, r, c+1)
		d.stats[k] = v
	}
	grid.SetColumnStretch(1, 1)
	grid.SetColumnStretch(3, 1)
	top.AddLayout2(grid.QLayout, 3)
	d.burn = newChart(a, "Token burn", "60 seconds", 120, fmtRate)
	top.AddWidget2(d.burn.W, 2)
	l.AddLayout(top.QLayout)

	// Pending approval for this agent, shown only while one waits.
	d.approval = qt.NewQFrame2()
	setProp(d.approval.QWidget, "card", true)
	al := qt.NewQHBoxLayout(d.approval.QWidget)
	al.SetContentsMargins(14, 10, 10, 10)
	d.approvalText = newLiveLabel("")
	d.approvalText.L.SetWordWrap(true)
	al.AddWidget2(d.approvalText.L.QWidget, 1)
	deny := qt.NewQPushButton3("Deny")
	d.approvalBtns = append(d.approvalBtns, deny)
	deny.OnClicked(func() { d.decide(false) })
	allow := qt.NewQPushButton3("Allow")
	d.approvalBtns = append(d.approvalBtns, allow)
	setProp(allow.QWidget, "accent", true)
	allow.OnClicked(func() { d.decide(true) })
	al.AddWidget(deny.QWidget)
	al.AddWidget(allow.QWidget)
	d.approval.Hide()
	d.approvalOn = true
	l.AddWidget(d.approval.QWidget)

	d.log = qt.NewQPlainTextEdit2()
	d.log.SetReadOnly(true)
	d.log.SetMaximumBlockCount(5000)
	d.log.SetLineWrapMode(qt.QPlainTextEdit__WidgetWidth)
	d.log.SetPlaceholderText("Nothing yet. Start the agent to see its session here.")
	l.AddWidget2(d.log.QWidget, 1)

	comp := qt.NewQHBoxLayout2()
	comp.SetSpacing(8)
	d.input = qt.NewQPlainTextEdit2()
	d.input.SetFixedHeight(72)
	d.input.OnKeyPressEvent(func(super func(*qt.QKeyEvent), e *qt.QKeyEvent) {
		if (e.Key() == int(qt.Key_Return) || e.Key() == int(qt.Key_Enter)) && e.Modifiers()&qt.ControlModifier != 0 {
			d.submit()
			return
		}
		super(e)
	})
	comp.AddWidget2(d.input.QWidget, 1)
	d.send = qt.NewQPushButton3("Send")
	setProp(d.send.QWidget, "accent", true)
	d.send.OnClicked(d.submit)
	comp.AddWidget(d.send.QWidget)
	l.AddLayout(comp.QLayout)
	for _, b := range []*qt.QPushButton{d.btn.hold, d.btn.resume, d.btn.stop, d.btn.kill} {
		b.SetEnabled(false) // matches ctlOn; refresh enables them as the status allows
	}
	return d
}

func (d *detailPage) widget() *qt.QWidget { return d.w }

func (d *detailPage) setAgent(id string) {
	if id == d.id {
		return
	}
	d.id = id
	d.next = 0
	d.log.Clear()
	d.input.Clear()
	d.lastStatus = ""
	d.sendText, d.sendHint = "", ""
}

func (d *detailPage) current(s *fleet.Snapshot) *fleet.AgentView {
	for i := range s.Agents {
		if s.Agents[i].ID == d.id {
			return &s.Agents[i]
		}
	}
	return nil
}

func (d *detailPage) submit() {
	text := strings.TrimSpace(d.input.ToPlainText())
	if text == "" || d.app.snap == nil {
		return
	}
	a := d.current(d.app.snap)
	if a == nil {
		return
	}
	var err error
	if a.Status.Live() {
		err = d.app.ctl.Send(d.id, text)
	} else {
		err = d.app.ctl.Start(d.id, text)
	}
	if err == nil {
		d.input.Clear()
	}
	d.app.report(err)
}

func (d *detailPage) decide(allow bool) {
	if d.approvalID == "" {
		return
	}
	reason := ""
	if !allow {
		reason = "Denied in Atlas Commander."
	}
	d.app.report(d.app.ctl.Decide(d.approvalID, allow, reason))
}

func (d *detailPage) refresh(s *fleet.Snapshot) {
	a := d.current(s)
	if a == nil {
		d.title.Set("Agent removed")
		// Nothing the page offered applies any more: a click on a stale
		// Allow would decide somebody else's request.
		d.approvalID = ""
		d.setApproval(false)
		d.setControls([4]bool{})
		return
	}
	now := time.Now()
	d.title.Set(a.Name)
	d.status.Set("● " + statusText(a.Status))
	if a.Status != d.lastStatus {
		d.lastStatus = a.Status
		tone := map[fleet.Tone]string{fleet.ToneOK: "ok", fleet.ToneWarn: "warn", fleet.ToneError: "error", fleet.ToneIdle: "idle"}[a.Status.Tone()]
		setProp(d.status.L.QWidget, "status", tone)
	}
	meta := []string{a.FleetName, a.Backend, a.Model}
	if a.Error != "" {
		meta = append(meta, a.Error)
	}
	d.meta.Set(strings.Join(meta, " · "))

	u := a.Session
	cost := fmtUSD(a.SessionCost)
	if a.CapUSD > 0 {
		cost += " of " + fmtUSD(a.CapUSD) + " cap"
	}
	set := func(k, v string) { d.stats[k].Set(v) }
	set("Tokens", fmtTokens(u.Total()))
	set("Cost", cost)
	set("Input / output", fmtTokens(u.Input)+" / "+fmtTokens(u.Output))
	set("All time", fmtUSD(a.CostUSD)+" · "+fmtTokens(a.Total.Total())+" tokens")
	set("Cache read / write", fmtTokens(u.CacheRead)+" / "+fmtTokens(u.CacheWrite5m+u.CacheWrite1h))
	set("Latency", fmtDur(a.Latency))
	set("Session", orDash(a.SessionID))
	if a.StartedAt.IsZero() {
		set("Started", "–")
	} else {
		set("Started", fmtAgo(a.StartedAt, now)+" ago")
	}
	set("Working dir", orDash(a.WorkDir))
	if a.Branch != "" {
		set("Worktree", a.Branch)
	} else {
		set("Worktree", "none")
	}
	d.burn.Series = []Series{{Values: a.Burn}}
	d.burn.W.Update()

	d.approvalID = ""
	for _, ap := range s.Approvals {
		if ap.AgentID == a.ID {
			d.approvalID = ap.ID
			d.approvalText.Set(fmt.Sprintf("Wants to run %s: %s", ap.Tool, ap.Summary))
			break
		}
	}
	d.setApproval(d.approvalID != "")

	live := a.Status.Live()
	d.setControls([4]bool{live && a.Status != fleet.StatusHeld, live && a.Status == fleet.StatusHeld, live, live})
	text, hint := "Send", "Message the agent (Ctrl+Enter)"
	switch {
	case !live:
		text, hint = "Start", "Prompt for a new session (Ctrl+Enter)"
	case a.Status == fleet.StatusRunning || a.Status == fleet.StatusApproval:
		text, hint = "Redirect", "Reaches the agent before its next step (Ctrl+Enter)"
	}
	if text != d.sendText {
		d.sendText = text
		d.send.SetText(text)
	}
	if hint != d.sendHint {
		d.sendHint = hint
		d.input.SetPlaceholderText(hint)
	}

	d.appendTranscript()
}

// setApproval shows the approval card and enables its buttons together, and
// only touches them when that changes.
func (d *detailPage) setApproval(on bool) {
	if on != d.approvalShown {
		d.approvalShown = on
		d.approval.SetVisible(on)
	}
	if on != d.approvalOn {
		d.approvalOn = on
		for _, b := range d.approvalBtns {
			b.SetEnabled(on)
		}
	}
}

func (d *detailPage) setControls(on [4]bool) {
	for i, b := range []*qt.QPushButton{d.btn.hold, d.btn.resume, d.btn.stop, d.btn.kill} {
		if on[i] != d.ctlOn[i] {
			d.ctlOn[i] = on[i]
			b.SetEnabled(on[i])
		}
	}
}

// appendTranscript fetches entries past d.next and appends them, keeping
// the view pinned to the bottom only if it already was.
func (d *detailPage) appendTranscript() {
	entries, next := d.app.ctl.Transcript(d.id, d.next)
	d.next = next
	if len(entries) == 0 {
		return
	}
	sb := d.log.VerticalScrollBar()
	atBottom := sb.Value() >= sb.Maximum()-4
	// Opening a long transcript appends hundreds of blocks; repainting after
	// each one is what makes it slow.
	batch := len(entries) > 1
	if batch {
		d.log.SetUpdatesEnabled(false)
	}
	for _, e := range entries {
		d.log.AppendHtml(entryHTML(d.app, e))
	}
	if batch {
		d.log.SetUpdatesEnabled(true)
	}
	if atBottom {
		sb.SetValue(sb.Maximum())
	}
}

// entryHTML is one transcript line as rich text, for the detail and observed
// views.
func entryHTML(a *App, e fleet.Entry) string {
	p := a.pal
	hex := func(c rgb, alpha float64) string {
		// QPlainTextEdit's HTML subset takes rgba().
		return fmt.Sprintf("rgba(%d,%d,%d,%.2f)", clamp255(c.r), clamp255(c.g), clamp255(c.b), alpha)
	}
	monoName := html.EscapeString(a.mono)
	ts := `<span style="color:` + hex(p.fg, 0.45) + `; font-family:'` + monoName + `'">` + e.At.Local().Format("15:04:05") + `</span>&nbsp;&nbsp;`
	text := strings.ReplaceAll(html.EscapeString(strings.TrimRight(e.Text, "\n")), "\n", "<br>")
	mono := `font-family:'` + monoName + `';`
	switch {
	case e.User:
		return ts + `<span style="color:` + hex(p.accent, 1) + `; font-weight:600">You</span>&nbsp;&nbsp;` + text
	case e.Kind == agent.EventToolUse:
		return ts + `<span style="color:` + hex(p.fg, 0.7) + `;` + mono + `">▸ ` + html.EscapeString(e.Tool) + `</span>&nbsp;&nbsp;<span style="color:` + hex(p.fg, 0.7) + `;` + mono + `">` + text + `</span>`
	case e.Kind == agent.EventToolResult && e.IsError:
		return ts + `<span style="color:` + hex(p.err, 1) + `;` + mono + `">✕ ` + text + `</span>`
	case e.Kind == agent.EventToolResult:
		return ts + `<span style="color:` + hex(p.fg, 0.5) + `;` + mono + `">` + text + `</span>`
	case e.IsError || e.Kind == agent.EventError:
		return ts + `<span style="color:` + hex(p.err, 1) + `">` + text + `</span>`
	case e.Kind == agent.EventResult, e.Kind == agent.EventExit, e.Kind == agent.EventInit:
		return ts + `<span style="color:` + hex(p.fg, 0.55) + `">` + text + `</span>`
	}
	return ts + text
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}
