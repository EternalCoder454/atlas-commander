package ui

import (
	"cmp"
	"html"
	"slices"
	"strings"

	qt "github.com/mappu/miqt/qt6"

	"atlas-commander/internal/config"
	"atlas-commander/internal/fleet"
	"atlas-commander/internal/ui/qtx"
)

// The Simple layout shows agents as cards instead of the table. Everything
// here runs on the main thread, fed by boardPage.refresh from the 250 ms tick,
// so a card only touches a widget when the value it shows has changed.

const (
	cardMinWidth = 300 // narrowest a card gets before the grid drops a column
	cardMaxCols  = 3
	cardGap      = 12
	cardPad      = 14 // left and right padding inside a card
	cardDotBox   = 28 // room for the status dot and its widest halo
	cardFadeMs   = 180
)

// Which buttons a card shows. The mode is derived from the agent's status and
// compared with the last one, so buttons are only toggled when it changes.
const (
	cardModeIdle     = iota // Start, Open
	cardModeLive            // Hold, Stop, Open
	cardModeHeld            // Resume, Stop, Open
	cardModeApproval        // Review, Stop, Open
)

// agentCard is one agent in the Simple layout. The board keeps these in
// boardPage.cards keyed by agent id so a later change (an animation, say) can
// find the card for an agent without walking the grid.
type agentCard struct {
	id string
	f  *qt.QFrame

	dot    *qt.QWidget // painted status dot, with the same halo as the table
	status *qt.QLabel
	name   *qt.QLabel
	fleet  *qt.QLabel
	task   *qt.QLabel
	tool   *qt.QLabel
	cost   *qt.QLabel

	start, hold, resume, stop, review, open *qt.QPushButton

	// What the card last showed, so unchanged values cost nothing.
	tone       string
	dotStatus  fleet.Status
	statusText string
	nameText   string
	fleetText  string
	taskRaw    string
	toolRaw    string
	costText   string
	elideW     int // width the task and tool were elided for
	mode       int
	modeSet    bool
	taskLineH  int
	taskDim    bool // showing the "No task yet" placeholder
}

// simpleLayout reports whether the Simple layout is on. Anything but an
// explicit "advanced" counts as simple, matching config.NormalizeLayout.
func (a *App) simpleLayout() bool { return a.settings.Layout != config.LayoutAdvanced }

// buildCards makes the scrolling grid that replaces the table in Simple.
func (b *boardPage) buildCards() {
	b.cards = map[string]*agentCard{}
	b.cardScroll = qt.NewQScrollArea2()
	s := b.cardScroll
	s.SetWidgetResizable(true)
	s.SetFrameShape(qt.QFrame__NoFrame)
	s.SetHorizontalScrollBarPolicy(qt.ScrollBarAlwaysOff)
	s.Viewport().SetAutoFillBackground(false)

	b.cardHost = qt.NewQWidget2()
	b.cardHost.SetAutoFillBackground(false)
	outer := qt.NewQVBoxLayout(b.cardHost)
	outer.SetContentsMargins(0, 0, 8, 0) // clear of the scroll bar
	b.cardGrid = qt.NewQGridLayout2()
	b.cardGrid.SetSpacing(cardGap)
	outer.AddLayout(b.cardGrid.QLayout)
	outer.AddStretch()
	s.SetWidget(b.cardHost)

	s.OnResizeEvent(func(super func(*qt.QResizeEvent), ev *qt.QResizeEvent) {
		super(ev)
		b.placeCards(false)
	})
}

// cardColumns is how many columns fit in width w.
func cardColumns(w int) int {
	return max(1, min(cardMaxCols, (w+cardGap)/(cardMinWidth+cardGap)))
}

// newCard builds the widgets for one agent and registers it in b.cards. It
// does not place the card; placeCards does, so order stays in one place.
func (b *boardPage) newCard(a *fleet.AgentView) *agentCard {
	c := &agentCard{id: a.ID, f: qt.NewQFrame2()}
	setProp(c.f.QWidget, "card", true)
	c.f.SetCursor(qt.NewQCursor2(qt.PointingHandCursor))
	c.f.OnMousePressEvent(func(super func(*qt.QMouseEvent), ev *qt.QMouseEvent) {
		super(ev)
		if ev.Button() == qt.LeftButton {
			b.app.openAgent(c.id)
		}
	})
	l := qt.NewQVBoxLayout(c.f.QWidget)
	l.SetContentsMargins(cardPad, 12, cardPad, 12)
	l.SetSpacing(6)

	top := qt.NewQHBoxLayout2()
	top.SetSpacing(8)
	c.dot = qt.NewQWidget2()
	c.dot.SetFixedSize2(cardDotBox, cardDotBox)
	c.dot.OnPaintEvent(func(super func(*qt.QPaintEvent), ev *qt.QPaintEvent) { b.paintCardDot(c) })
	// The dot's box is wide enough for the halo, which already leaves a gap,
	// so the label sits right against it.
	dotRow := qt.NewQHBoxLayout2()
	dotRow.SetSpacing(0)
	dotRow.AddWidget(c.dot)
	c.status = qt.NewQLabel3("")
	dotRow.AddWidget(c.status.QWidget)
	top.AddLayout(dotRow.QLayout)
	c.name = qt.NewQLabel3("")
	c.name.SetTextFormat(qt.RichText)
	c.name.SetSizePolicy2(qt.QSizePolicy__Ignored, qt.QSizePolicy__Preferred)
	top.AddWidget2(c.name.QWidget, 1)
	c.fleet = caption("")
	c.fleet.SetAlignment(qt.AlignRight | qt.AlignVCenter)
	top.AddWidget(c.fleet.QWidget)
	l.AddLayout(top.QLayout)

	c.task = qt.NewQLabel3("")
	c.task.SetWordWrap(true)
	c.task.SetAlignment(qt.AlignLeft | qt.AlignTop)
	l.AddWidget(c.task.QWidget)

	info := qt.NewQHBoxLayout2()
	info.SetSpacing(8)
	c.tool = caption("")
	c.tool.SetSizePolicy2(qt.QSizePolicy__Ignored, qt.QSizePolicy__Preferred)
	info.AddWidget2(c.tool.QWidget, 1)
	c.cost = caption("")
	setProp(c.cost.QWidget, "mono", true)
	info.AddWidget(c.cost.QWidget)
	l.AddLayout(info.QLayout)

	btns := qt.NewQHBoxLayout2()
	btns.SetSpacing(6)
	mk := func(text, tip string, f func()) *qt.QPushButton {
		p := qt.NewQPushButton3(text)
		p.SetToolTip(tip)
		p.OnClicked(f)
		btns.AddWidget(p.QWidget)
		return p
	}
	app := b.app
	c.start = mk("Start", "Start a session with a prompt", func() { b.startAgent(c.id) })
	c.hold = mk("Hold", "Stop the agent at its next tool call", func() { app.report(app.ctl.Hold(c.id)) })
	c.resume = mk("Resume", "Let a held agent continue", func() { app.report(app.ctl.Resume(c.id)) })
	c.stop = mk("Stop", "End the session after asking the agent to stop", func() { app.report(app.ctl.Stop(c.id)) })
	c.review = mk("Review", "See what this agent wants to run", func() { app.side.select_("approvals") })
	setProp(c.review.QWidget, "accent", true)
	btns.AddStretch()
	c.open = mk("Open", "Show the transcript and controls", func() { app.openAgent(c.id) })
	l.AddLayout(btns.QLayout)

	b.cards[a.ID] = c
	return c
}

// removeCard drops the card for an agent that is gone.
func (b *boardPage) removeCard(id string) {
	c, ok := b.cards[id]
	if !ok {
		return
	}
	b.cardGrid.RemoveWidget(c.f.QWidget)
	c.f.Hide()
	c.f.DeleteLater()
	delete(b.cards, id)
}

// cardOrder sorts agents the way the table does by default (what needs a
// human first), whatever column the user last sorted the table by.
func cardOrder(rows []fleet.AgentView) []fleet.AgentView {
	out := slices.Clone(rows)
	slices.SortStableFunc(out, func(x, y fleet.AgentView) int {
		if c := cmp.Compare(statusRank(x.Status), statusRank(y.Status)); c != 0 {
			return c
		}
		if c := cmp.Compare(x.FleetName, y.FleetName); c != 0 {
			return c
		}
		return cmp.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
	})
	return out
}

// refreshCards brings the cards in line with b.rows: adds, removes, updates
// in place, and re-places them only when the order or column count changed.
func (b *boardPage) refreshCards() {
	rows := cardOrder(b.rows)
	seen := make(map[string]bool, len(rows))
	order := make([]string, 0, len(rows))
	for i := range rows {
		a := &rows[i]
		seen[a.ID] = true
		order = append(order, a.ID)
		c, ok := b.cards[a.ID]
		if !ok {
			c = b.newCard(a)
			// A card that appears while the grid is on screen fades in, so
			// a new agent is noticed; the first fill at startup does not.
			if b.cardScroll.IsVisible() {
				fadeInCard(c)
			}
		}
		b.updateCard(c, a)
	}
	for id := range b.cards {
		if !seen[id] {
			b.removeCard(id)
		}
	}
	if !slices.Equal(order, b.cardPlaced) {
		b.cardWanted = order
		b.placeCards(true)
	}
}

// placeCards puts the cards in the grid in b.cardWanted order. It does
// nothing when neither the order nor the column count changed, so the 250 ms
// tick never moves a widget.
func (b *boardPage) placeCards(orderChanged bool) {
	cols := cardColumns(b.cardScroll.Viewport().Width())
	if !orderChanged && cols == b.cardCols {
		return
	}
	for _, id := range b.cardPlaced {
		if c, ok := b.cards[id]; ok {
			b.cardGrid.RemoveWidget(c.f.QWidget)
		}
	}
	for i := 0; i < max(cols, b.cardCols); i++ {
		stretch := 0
		if i < cols {
			stretch = 1
		}
		b.cardGrid.SetColumnStretch(i, stretch)
	}
	for i, id := range b.cardWanted {
		c := b.cards[id]
		if c == nil {
			continue
		}
		b.cardGrid.AddWidget2(c.f.QWidget, i/cols, i%cols)
		c.f.Show()
	}
	b.cardPlaced = slices.Clone(b.cardWanted)
	b.cardCols = cols

	// Cards changed width, so text elided for the old width is stale.
	w := b.cardInnerWidth(cols)
	for _, c := range b.cards {
		b.fitCardText(c, w)
	}
}

// cardInnerWidth is the room for text inside a card at the given column count.
func (b *boardPage) cardInnerWidth(cols int) int {
	vw := b.cardScroll.Viewport().Width() - 8
	return max(120, (vw-(cols-1)*cardGap)/cols-2*cardPad)
}

// fitCardText sets the task and last tool text for a card width. The task is
// clipped to two lines: the label gets a fixed two-line height and the text
// is elided to about two lines of width so the cut shows an ellipsis.
func (b *boardPage) fitCardText(c *agentCard, w int) {
	c.elideW = w
	fm := c.task.FontMetrics()
	lh := fm.LineSpacing()
	task, tip := c.taskRaw, c.taskRaw
	if task == "" {
		task, tip = "No task yet", ""
	}
	task = strings.Join(strings.Fields(task), " ")
	c.task.SetText(fm.ElidedText(task, qt.ElideRight, 2*w-w/6))
	if lh != c.taskLineH {
		c.taskLineH = lh
		c.task.SetFixedHeight(2*lh + 2)
	}
	c.task.SetToolTip(tip)
	if dim := c.taskRaw == ""; dim != c.taskDim {
		c.taskDim = dim
		setProp(c.task.QWidget, "caption", dim)
	}
	// Leave the cost its room; the tool name takes the rest.
	tm := c.tool.FontMetrics()
	cw := c.cost.SizeHint().Width()
	c.tool.SetText(tm.ElidedText(c.toolRaw, qt.ElideRight, max(40, w-cw-8)))
	c.tool.SetToolTip(c.toolRaw)
}

// updateCard shows agent a on card c, touching only what changed.
func (b *boardPage) updateCard(c *agentCard, a *fleet.AgentView) {
	tone := "idle"
	switch a.Status.Tone() {
	case fleet.ToneOK:
		tone = "ok"
	case fleet.ToneWarn:
		tone = "warn"
	case fleet.ToneError:
		tone = "error"
	}
	if a.Status != c.dotStatus {
		c.dotStatus = a.Status
		c.dot.Update()
	}
	if st := statusText(a.Status); st != c.statusText {
		c.statusText = st
		c.status.SetText(st)
	}
	if tone != c.tone {
		c.tone = tone
		setProp(c.status.QWidget, "status", tone)
	}
	if a.Name != c.nameText {
		c.nameText = a.Name
		c.name.SetText("<b>" + html.EscapeString(a.Name) + "</b>")
		c.name.SetToolTip(a.Name)
	}
	if a.FleetName != c.fleetText {
		c.fleetText = a.FleetName
		c.fleet.SetText(a.FleetName)
	}

	cost := fmtUSD(a.CostUSD)
	if a.CapUSD > 0 {
		cost += " of " + fmtCap(a.CapUSD)
	}
	if cost != c.costText {
		c.costText = cost
		c.cost.SetText(cost)
	}

	tool := ""
	if a.LastTool != "" {
		tool = firstLine(a.LastTool)
	}
	if a.Task != c.taskRaw || tool != c.toolRaw || c.elideW == 0 {
		c.taskRaw, c.toolRaw = a.Task, tool
		w := c.elideW
		if w == 0 {
			w = b.cardInnerWidth(max(1, b.cardCols))
		}
		b.fitCardText(c, w)
	}

	mode := cardModeIdle
	switch {
	case a.Status == fleet.StatusApproval:
		mode = cardModeApproval
	case a.Status == fleet.StatusHeld:
		mode = cardModeHeld
	case a.Status.Live():
		mode = cardModeLive
	}
	if !c.modeSet || mode != c.mode {
		c.mode, c.modeSet = mode, true
		c.start.SetVisible(mode == cardModeIdle)
		c.hold.SetVisible(mode == cardModeLive)
		c.resume.SetVisible(mode == cardModeHeld)
		c.review.SetVisible(mode == cardModeApproval)
		c.stop.SetVisible(mode != cardModeIdle)
	}
}

// applyLayoutMode switches the board between the card grid and the table.
func (b *boardPage) applyLayoutMode(simple bool) {
	b.simple = simple
	for _, w := range []*qt.QWidget{b.burn.W, b.cost.W, b.cmdBar} {
		w.SetVisible(!simple)
	}
	b.guide.setSimple(simple)
	if simple {
		b.refreshCards()
	}
	b.showRows()
}

// showRows picks what fills the board: the start guide when there are no
// agents, otherwise the cards or the table for the current layout.
func (b *boardPage) showRows() {
	switch {
	case len(b.rows) == 0:
		b.views.SetCurrentWidget(b.empty)
	case b.simple:
		b.views.SetCurrentWidget(b.cardScroll.QWidget)
	default:
		b.views.SetCurrentWidget(b.table.QWidget)
	}
}

// paintCardDot draws a card's status dot, and its halo while the board's
// pulse timer has halos on.
func (b *boardPage) paintCardDot(c *agentCard) {
	pal := b.app.pal
	if pal == nil {
		return
	}
	p := qt.NewQPainter2(c.dot.QPaintDevice)
	defer p.Delete()
	defer p.End()
	p.SetRenderHint(qt.QPainter__Antialiasing)
	col := pal.toneColor(c.dotStatus.Tone())
	mid := cardDotBox / 2.0
	b.paintHalo(p, c.dotStatus, col, mid, mid, [4]int{0, 0, cardDotBox, cardDotBox})
	dc := col.q(1)
	defer dc.Delete()
	br := qt.NewQBrush3(dc)
	defer br.Delete()
	p.SetPenWithStyle(qt.NoPen)
	p.SetBrush(br)
	p.DrawEllipse(rectf(mid-pulseDotR, mid-pulseDotR, 2*pulseDotR, 2*pulseDotR))
}

// fadeInCard fades a new card from transparent. The effect belongs to the
// card, so a card removed mid-fade takes its animation with it.
func fadeInCard(c *agentCard) {
	if !qtx.AnimationsEnabled() {
		return
	}
	eff := qt.NewQGraphicsOpacityEffect2(c.f.QObject)
	eff.SetOpacity(0)
	c.f.SetGraphicsEffect(eff.QGraphicsEffect)
	animate(c.f.QObject, cardFadeMs, 0, 1, eff.SetOpacity, func() { c.f.SetGraphicsEffect(nil) })
}
