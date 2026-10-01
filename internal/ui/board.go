package ui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	qt "github.com/mappu/miqt/qt6"

	"atlas-commander/internal/fleet"
)

// boardCol is one column of the status board. Every cell is painted by the
// delegate from the Go-side row, so the model hands Qt almost nothing: no
// per-cell QVariants, no string copies across cgo on each repaint.
type boardCol struct {
	title string
	width int // 0 stretches
	mono  bool
	right bool
	text  func(a *fleet.AgentView, now time.Time) string
	less  func(a, b *fleet.AgentView) int
}

var boardCols = []boardCol{
	{title: "Status", width: 124,
		text: func(a *fleet.AgentView, _ time.Time) string { return statusText(a.Status) },
		less: func(a, b *fleet.AgentView) int { return cmp.Compare(statusRank(a.Status), statusRank(b.Status)) }},
	{title: "Agent", width: 170,
		text: func(a *fleet.AgentView, _ time.Time) string { return a.Name },
		less: func(a, b *fleet.AgentView) int { return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) }},
	{title: "Model", width: 104, mono: true,
		text: func(a *fleet.AgentView, _ time.Time) string { return strings.TrimPrefix(a.Model, "claude-") },
		less: func(a, b *fleet.AgentView) int { return cmp.Compare(a.Model, b.Model) }},
	{title: "Task", width: 0,
		text: func(a *fleet.AgentView, _ time.Time) string { return firstLine(a.Task) },
		less: func(a, b *fleet.AgentView) int { return cmp.Compare(a.Task, b.Task) }},
	{title: "Tokens", width: 70, mono: true, right: true,
		text: func(a *fleet.AgentView, _ time.Time) string {
			if a.Session.IsZero() {
				return "–"
			}
			return fmtTokens(a.Session.Total())
		},
		less: func(a, b *fleet.AgentView) int { return cmp.Compare(a.Session.Total(), b.Session.Total()) }},
	{title: "Burn", width: 96, mono: true, right: true,
		text: func(a *fleet.AgentView, _ time.Time) string {
			if !a.Status.Live() {
				return "–"
			}
			return fmtRate(lastOr(a.Burn, 0))
		},
		less: func(a, b *fleet.AgentView) int { return cmp.Compare(lastOr(a.Burn, 0), lastOr(b.Burn, 0)) }},
	{title: "Cost", width: 112, mono: true, right: true,
		text: func(a *fleet.AgentView, _ time.Time) string {
			if a.CapUSD > 0 {
				return fmtUSD(a.CostUSD) + "/" + fmtCap(a.CapUSD)
			}
			return fmtUSD(a.CostUSD)
		},
		less: func(a, b *fleet.AgentView) int { return cmp.Compare(a.CostUSD, b.CostUSD) }},
	{title: "Latency", width: 66, mono: true, right: true,
		text: func(a *fleet.AgentView, _ time.Time) string { return fmtDur(a.Latency) },
		less: func(a, b *fleet.AgentView) int { return cmp.Compare(a.Latency, b.Latency) }},
	{title: "Last tool", width: 170,
		text: func(a *fleet.AgentView, now time.Time) string {
			if a.LastTool == "" {
				return ""
			}
			return fmtAgo(a.LastToolAt, now) + "  " + firstLine(a.LastTool)
		},
		less: func(a, b *fleet.AgentView) int { return a.LastToolAt.Compare(b.LastToolAt) }},
	{title: "Worktree", width: 120, mono: true,
		text: func(a *fleet.AgentView, _ time.Time) string {
			return strings.TrimPrefix(a.Branch, "atlas/")
		},
		less: func(a, b *fleet.AgentView) int { return cmp.Compare(a.Branch, b.Branch) }},
}

const (
	colStatus   = 0
	colAgent    = 1
	colModel    = 2
	colTask     = 3
	colBurn     = 5
	colCost     = 6
	colWorktree = 9
)

func statusText(s fleet.Status) string {
	switch s {
	case fleet.StatusApproval:
		return "Approval"
	case fleet.StatusCapped:
		return "Cost cap hit"
	}
	str := string(s)
	if str == "" {
		return ""
	}
	return strings.ToUpper(str[:1]) + str[1:]
}

// statusRank orders the board so what needs a human sorts first.
func statusRank(s fleet.Status) int {
	switch s {
	case fleet.StatusApproval:
		return 0
	case fleet.StatusError:
		return 1
	case fleet.StatusCapped:
		return 2
	case fleet.StatusHeld:
		return 3
	case fleet.StatusRunning:
		return 4
	case fleet.StatusStarting:
		return 5
	case fleet.StatusWaiting:
		return 6
	case fleet.StatusStopped:
		return 7
	}
	return 8
}

type boardPage struct {
	app *App
	w   *qt.QWidget

	summary    *liveLabel
	setup      *setupBanner
	burn, cost *Chart
	views      *qt.QStackedWidget
	empty      *qt.QWidget
	guide      *startGuide
	table      *qt.QTableView
	cmdBar     *qt.QWidget // the Start/Hold/.../Open row; hidden in Simple
	model      *qt.QAbstractTableModel
	delegate   *qt.QStyledItemDelegate

	rows     []fleet.AgentView
	sortCol  int
	sortDesc bool
	sorted   bool // user picked a column; otherwise status order
	seq      uint64
	selected string // agent id, kept across resets
	hover    int    // row under the mouse, -1 for none
	lastTick time.Time

	btn struct {
		start, hold, resume, stop, kill, open *qt.QPushButton
	}

	// Qt copies every QVariant returned from a model callback and never frees
	// ours, so the last one is kept and freed on the next call.
	ret   *qt.QVariant
	blank *qt.QVariant

	monoF *qt.QFont

	// Simple layout: agent cards in a grid instead of the table.
	simple     bool
	cardScroll *qt.QScrollArea
	cardHost   *qt.QWidget
	cardGrid   *qt.QGridLayout
	cards      map[string]*agentCard // by agent id
	cardPlaced []string              // ids in the grid, in order
	cardWanted []string
	cardCols   int
}

func newBoardPage(a *App) *boardPage {
	b := &boardPage{app: a, w: qt.NewQWidget2(), blank: qt.NewQVariant(), sortCol: colStatus, hover: -1}
	l := newPageLayout(b.w)

	top := qt.NewQHBoxLayout2()
	top.SetSpacing(12)
	top.AddWidget(pageTitle("Board").QWidget)
	b.summary = newLiveLabel("")
	setProp(b.summary.L.QWidget, "caption", true)
	top.AddWidget(b.summary.L.QWidget)
	top.AddStretch()
	add := qt.NewQPushButton3("New agent")
	setProp(add.QWidget, "accent", true)
	add.SetToolTip("Register an agent in a fleet")
	add.OnClicked(func() { a.editAgent("", "") })
	top.AddWidget(add.QWidget)
	l.AddLayout(top.QLayout)

	b.setup = newSetupBanner(a)
	l.AddWidget(b.setup.W.QWidget)

	charts := qt.NewQHBoxLayout2()
	charts.SetSpacing(18)
	b.burn = newChart(a, "Token burn", "60 seconds", 132, fmtRate)
	b.burn.Samples = 60
	b.cost = newChart(a, "Spend rate", "60 seconds", 132, func(v float64) string { return fmtUSD(v) + "/min" })
	b.cost.Samples = 60
	charts.AddWidget(b.burn.W)
	charts.AddWidget(b.cost.W)
	l.AddLayout(charts.QLayout)

	b.cmdBar = qt.NewQWidget2()
	cmds := b.buildCommands()
	b.cmdBar.SetLayout(cmds.QLayout)
	cmds.SetContentsMargins(0, 0, 0, 0)
	l.AddWidget(b.cmdBar)

	b.views = qt.NewQStackedWidget2()
	b.guide = newStartGuide(a)
	b.empty = b.guide.W
	b.views.AddWidget(b.empty)

	b.buildTable()
	b.views.AddWidget(b.table.QWidget)
	b.buildCards()
	b.views.AddWidget(b.cardScroll.QWidget)
	l.AddWidget(b.views.QWidget)

	a.themed = append(a.themed, b.themeChanged)
	b.themeChanged()
	b.updateButtons()
	b.startPulse()
	return b
}

func (b *boardPage) widget() *qt.QWidget { return b.w }

func (b *boardPage) buildCommands() *qt.QHBoxLayout {
	row := qt.NewQHBoxLayout2()
	row.SetSpacing(4)
	// There is no pause, stop or block icon in the sets Commander draws from,
	// so Hold, Resume, Stop and Kill are words only.
	b.btn.start = b.app.commandButton("Start", "start", "Start a session with a prompt")
	b.btn.start.OnClicked(func() {
		if b.selected != "" {
			b.startAgent(b.selected)
		}
	})
	b.btn.hold, b.btn.resume, b.btn.stop, b.btn.kill = b.app.agentControls(func() string { return b.selected })
	b.btn.open = b.app.commandButton("Open", "open", "Show the transcript and controls")
	b.btn.open.OnClicked(func() {
		if b.selected != "" {
			b.app.openAgent(b.selected)
		}
	})
	for _, w := range []*qt.QPushButton{b.btn.start, b.btn.hold, b.btn.resume, b.btn.stop, b.btn.kill} {
		row.AddWidget(w.QWidget)
	}
	row.AddStretch()
	row.AddWidget(b.btn.open.QWidget)
	return row
}

func (b *boardPage) startAgent(id string) {
	var name string
	for _, r := range b.rows {
		if r.ID == id {
			name = r.Name
		}
	}
	ok := false
	text := qt.QInputDialog_GetMultiLineText3(b.app.win.QWidget, "Start "+name,
		"Prompt for this session. The agent's pinned prompt is sent first.", "", &ok)
	if !ok || strings.TrimSpace(text) == "" {
		return
	}
	b.app.report(b.app.ctl.Start(id, text))
}

func (b *boardPage) buildTable() {
	b.table = qt.NewQTableView2()
	t := b.table
	t.SetShowGrid(false)
	t.SetSelectionBehavior(qt.QAbstractItemView__SelectRows)
	t.SetSelectionMode(qt.QAbstractItemView__SingleSelection)
	t.SetEditTriggers(qt.QAbstractItemView__NoEditTriggers)
	t.SetVerticalScrollMode(qt.QAbstractItemView__ScrollPerPixel)
	t.SetHorizontalScrollMode(qt.QAbstractItemView__ScrollPerPixel)
	t.SetMouseTracking(true)
	setProp(t.QWidget, "painted", true)
	t.SetWordWrap(false)
	t.VerticalHeader().Hide()
	// Hover is tracked per row: Qt's item hover is per cell, and a lone
	// lit cell reads as a selection.
	t.OnMouseMoveEvent(func(super func(*qt.QMouseEvent), e *qt.QMouseEvent) {
		row := t.IndexAt(e.Pos()).Row()
		if row != b.hover {
			b.hover = row
			t.Viewport().Update()
		}
		super(e)
	})
	t.OnLeaveEvent(func(super func(*qt.QEvent), e *qt.QEvent) {
		b.hover = -1
		t.Viewport().Update()
		super(e)
	})

	m := qt.NewQAbstractTableModel()
	b.model = m
	m.OnRowCount(func(parent *qt.QModelIndex) int {
		if parent != nil && parent.IsValid() {
			return 0
		}
		return len(b.rows)
	})
	m.OnColumnCount(func(parent *qt.QModelIndex) int {
		if parent != nil && parent.IsValid() {
			return 0
		}
		return len(boardCols)
	})
	m.OnData(func(idx *qt.QModelIndex, role int) *qt.QVariant {
		r, c := idx.Row(), idx.Column()
		if r < 0 || r >= len(b.rows) || c < 0 || c >= len(boardCols) {
			return b.blank
		}
		a := &b.rows[r]
		switch qt.ItemDataRole(role) {
		case qt.DisplayRole: // for accessibility and keyboard search
			return b.give(qt.NewQVariant11(boardCols[c].text(a, b.lastTick)))
		case qt.ToolTipRole:
			if tip := cellTip(a, c); tip != "" {
				return b.give(qt.NewQVariant11(tip))
			}
		}
		return b.blank
	})
	m.OnHeaderData(func(super func(int, qt.Orientation, int) *qt.QVariant, section int, o qt.Orientation, role int) *qt.QVariant {
		if o != qt.Horizontal || section < 0 || section >= len(boardCols) {
			return b.blank
		}
		switch qt.ItemDataRole(role) {
		case qt.DisplayRole:
			return b.give(qt.NewQVariant11(boardCols[section].title))
		case qt.TextAlignmentRole:
			al := qt.AlignLeft | qt.AlignVCenter
			if boardCols[section].right {
				al = qt.AlignRight | qt.AlignVCenter
			}
			return b.give(qt.NewQVariant4(int(al)))
		}
		return b.blank
	})
	m.OnFlags(func(super func(*qt.QModelIndex) qt.ItemFlag, idx *qt.QModelIndex) qt.ItemFlag {
		return qt.ItemIsEnabled | qt.ItemIsSelectable
	})
	t.SetModel(m.QAbstractItemModel)

	b.delegate = qt.NewQStyledItemDelegate()
	b.delegate.OnPaint(func(super func(*qt.QPainter, *qt.QStyleOptionViewItem, *qt.QModelIndex), p *qt.QPainter, opt *qt.QStyleOptionViewItem, idx *qt.QModelIndex) {
		b.paintCell(p, opt, idx)
	})
	b.delegate.OnSizeHint(func(super func(*qt.QStyleOptionViewItem, *qt.QModelIndex) *qt.QSize, opt *qt.QStyleOptionViewItem, idx *qt.QModelIndex) *qt.QSize {
		c := idx.Column()
		w := 80
		if c >= 0 && c < len(boardCols) && boardCols[c].width > 0 {
			w = boardCols[c].width
		}
		return b.giveSize(qt.NewQSize2(w, b.app.metrics.RowHeight))
	})
	t.SetItemDelegate(b.delegate.QAbstractItemDelegate)

	h := t.HorizontalHeader()
	h.SetHighlightSections(false)
	h.SetSortIndicatorShown(true)
	h.SetSectionsClickable(true)
	h.SetMinimumSectionSize(48)
	for i, c := range boardCols {
		if c.width == 0 {
			h.SetSectionResizeMode2(i, qt.QHeaderView__Stretch)
		} else {
			h.SetSectionResizeMode2(i, qt.QHeaderView__Interactive)
			t.SetColumnWidth(i, c.width)
		}
	}
	h.OnSectionClicked(func(i int) {
		if b.sorted && i == b.sortCol {
			b.sortDesc = !b.sortDesc
		} else {
			b.sortCol, b.sortDesc, b.sorted = i, false, true
		}
		order := qt.AscendingOrder
		if b.sortDesc {
			order = qt.DescendingOrder
		}
		h.SetSortIndicator(i, order)
		b.resort()
		t.ScrollToTop()
	})
	h.SetSortIndicator(-1, qt.AscendingOrder)

	t.SelectionModel().OnCurrentRowChanged(func(cur, prev *qt.QModelIndex) {
		if cur.IsValid() && cur.Row() < len(b.rows) {
			b.selected = b.rows[cur.Row()].ID
		} else {
			b.selected = ""
		}
		b.updateButtons()
	})
	t.OnDoubleClicked(func(idx *qt.QModelIndex) {
		if idx.IsValid() && idx.Row() < len(b.rows) {
			b.app.openAgent(b.rows[idx.Row()].ID)
		}
	})
}

// give hands a QVariant to Qt and frees the previous one, which Qt has
// already copied by the time the next callback runs.
func (b *boardPage) give(v *qt.QVariant) *qt.QVariant {
	if b.ret != nil {
		b.ret.Delete()
	}
	b.ret = v
	return v
}

var lastSize *qt.QSize

func (b *boardPage) giveSize(s *qt.QSize) *qt.QSize {
	if lastSize != nil {
		lastSize.Delete()
	}
	lastSize = s
	return s
}

func cellTip(a *fleet.AgentView, col int) string {
	switch col {
	case colStatus:
		if a.Error != "" {
			return a.Error
		}
	case colTask:
		return a.Task
	case colCost:
		return fmt.Sprintf("This session %s · all time %s", fmtUSD(a.SessionCost), fmtUSD(a.CostUSD))
	case colAgent:
		return a.Name + " in " + a.FleetName
	case colWorktree:
		return a.Worktree
	}
	return ""
}

func (b *boardPage) themeChanged() {
	if b.monoF != nil {
		b.monoF.Delete()
	}
	b.monoF = b.app.monoFont(1)
	b.table.VerticalHeader().SetDefaultSectionSize(b.app.metrics.RowHeight)
	b.table.Viewport().Update()
}

// paintCell draws one cell the way Monitor's tables look: no zebra, a faint
// hover, selection as the accent at 25 %, a 1 px row line in the rowline
// blend, column dividers in the divider blend, and a 12 px status dot.
func (b *boardPage) paintCell(p *qt.QPainter, opt *qt.QStyleOptionViewItem, idx *qt.QModelIndex) {
	r, c := idx.Row(), idx.Column()
	if r < 0 || r >= len(b.rows) || c < 0 || c >= len(boardCols) {
		return
	}
	a := &b.rows[r]
	col := boardCols[c]
	pal := b.app.pal
	rect := opt.Rect()
	x, y, w, h := rect.X(), rect.Y(), rect.Width(), rect.Height()

	p.Save()
	defer p.Restore()

	state := opt.State()
	switch {
	case state&qt.QStyle__State_Selected != 0:
		fill := pal.accentB.q(0.25)
		p.FillRect5(x, y, w, h, fill)
		fill.Delete()
	case r == b.hover:
		fill := pal.fg.q(0.05)
		p.FillRect5(x, y, w, h, fill)
		fill.Delete()
	}
	line := pal.rowline.q(1)
	p.FillRect5(x, y+h-1, w, 1, line)
	line.Delete()
	if c < len(boardCols)-1 {
		div := pal.divider.q(1)
		p.FillRect5(x+w-1, y, 1, h, div)
		div.Delete()
	}

	const padX = 10
	tx, tw := x+padX, w-2*padX
	text := col.text(a, b.lastTick)

	if c == colStatus {
		tone := a.Status.Tone()
		dot := pal.idle
		switch tone {
		case fleet.ToneOK:
			dot = pal.ok
		case fleet.ToneWarn:
			dot = pal.warn
		case fleet.ToneError:
			dot = pal.err
		}
		b.paintHalo(p, a.Status, dot, float64(tx)+6, float64(y)+float64(h)/2, [4]int{x, y, w, h})
		dc := dot.q(1)
		br := qt.NewQBrush3(dc)
		p.SetRenderHint(qt.QPainter__Antialiasing)
		p.SetPenWithStyle(qt.NoPen)
		p.SetBrush(br)
		p.DrawEllipse(rectf(float64(tx), float64(y+(h-12)/2), 12, 12))
		br.Delete()
		dc.Delete()
		tx += 12 + 10
		tw -= 12 + 10
	}

	if c == colBurn {
		b.paintSpark(p, a.Burn, float64(x+3), float64(y+5), float64(w-7), float64(h-10))
	}
	if text == "" {
		return
	}
	var fm *qt.QFontMetrics
	if col.mono {
		p.SetFont(b.monoF)
		fm = qt.NewQFontMetrics(b.monoF)
	} else {
		f := b.table.Font()
		p.SetFont(f)
		fm = qt.NewQFontMetrics(f)
	}
	defer fm.Delete()

	alpha := 1.0
	switch {
	case c == colTask && a.Task == "":
		alpha = 0.5
	case c == colWorktree || c == colModel:
		alpha = 0.75
	}
	tc := pal.fg.q(alpha)
	if c == colCost && a.CapUSD > 0 && a.CostUSD >= a.CapUSD*0.8 {
		tc.Delete()
		tc = pal.warn.q(1)
	}
	p.SetPen(tc)
	tc.Delete()

	if c == colAgent {
		name := fm.ElidedText(a.Name, qt.ElideRight, tw)
		p.DrawText7(tx, y, tw, h, int(qt.AlignLeft|qt.AlignVCenter), name)
		used := fm.HorizontalAdvance(name) + 8
		if a.FleetName != "" && used < tw-24 {
			dim := pal.fg.q(0.5)
			p.SetPen(dim)
			dim.Delete()
			p.DrawText7(tx+used, y, tw-used, h, int(qt.AlignLeft|qt.AlignVCenter), fm.ElidedText(a.FleetName, qt.ElideRight, tw-used))
		}
		return
	}
	align := qt.AlignLeft | qt.AlignVCenter
	if col.right {
		align = qt.AlignRight | qt.AlignVCenter
	}
	p.DrawText7(tx, y, tw, h, int(align), fm.ElidedText(text, qt.ElideRight, tw))
}

func (b *boardPage) refresh(s *fleet.Snapshot) {
	now := time.Now()
	b.lastTick = now
	b.setup.refresh()
	b.burn.Series = []Series{{Values: s.Burn, Color: 0}}
	b.cost.Series = []Series{{Values: s.CostRate, Color: 3}}
	b.burn.W.Update()
	b.cost.W.Update()

	if s.Seq == b.seq && len(b.rows) > 0 {
		// Nothing new, but relative times ("12s ago") still move.
		b.table.Viewport().Update()
		return
	}
	b.seq = s.Seq

	live, waiting, approval, agents := 0, 0, 0, 0
	for _, a := range s.Agents {
		if a.Archived {
			continue
		}
		agents++
		switch {
		case a.Status == fleet.StatusApproval:
			approval++
		case a.Status == fleet.StatusWaiting:
			waiting++
		case a.Status.Live():
			live++
		}
	}
	parts := []string{plural(agents, "agent") + " in " + plural(len(s.Fleets), "fleet")}
	if live > 0 {
		parts = append(parts, fmt.Sprintf("%d running", live))
	}
	if waiting > 0 {
		parts = append(parts, fmt.Sprintf("%d waiting for input", waiting))
	}
	if approval > 0 {
		parts = append(parts, fmt.Sprintf("%d need approval", approval))
	}
	b.summary.Set(strings.Join(parts, " · "))

	rows := make([]fleet.AgentView, 0, len(s.Agents))
	for _, a := range s.Agents {
		if !a.Archived {
			rows = append(rows, a)
		}
	}
	b.sortRows(rows)

	sameIDs := len(rows) == len(b.rows)
	for i := 0; sameIDs && i < len(rows); i++ {
		sameIDs = rows[i].ID == b.rows[i].ID
	}
	if sameIDs {
		b.rows = rows
		if len(rows) > 0 {
			tl := b.model.CreateIndex(0, 0)
			br := b.model.CreateIndex(len(rows)-1, len(boardCols)-1)
			b.model.DataChanged(&tl, &br)
		}
	} else {
		b.model.BeginResetModel()
		b.rows = rows
		b.model.EndResetModel()
		b.restoreSelection()
	}
	if len(rows) == 0 {
		b.guide.refresh(s)
	}
	if b.simple {
		b.refreshCards()
	}
	b.showRows()
	b.updateButtons()
}

func (b *boardPage) sortRows(rows []fleet.AgentView) {
	if !b.sorted {
		slices.SortStableFunc(rows, func(x, y fleet.AgentView) int {
			if c := cmp.Compare(statusRank(x.Status), statusRank(y.Status)); c != 0 {
				return c
			}
			if c := cmp.Compare(x.FleetName, y.FleetName); c != 0 {
				return c
			}
			return cmp.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
		})
		return
	}
	less := boardCols[b.sortCol].less
	slices.SortStableFunc(rows, func(x, y fleet.AgentView) int {
		c := less(&x, &y)
		if c == 0 {
			c = cmp.Compare(x.ID, y.ID)
		}
		if b.sortDesc {
			return -c
		}
		return c
	})
}

func (b *boardPage) resort() {
	rows := slices.Clone(b.rows)
	b.sortRows(rows)
	b.model.BeginResetModel()
	b.rows = rows
	b.model.EndResetModel()
	b.restoreSelection()
}

func (b *boardPage) restoreSelection() {
	for i, r := range b.rows {
		if r.ID == b.selected {
			b.table.SelectRow(i)
			return
		}
	}
	b.selected = ""
}

func (b *boardPage) current() *fleet.AgentView {
	for i := range b.rows {
		if b.rows[i].ID == b.selected {
			return &b.rows[i]
		}
	}
	return nil
}

func (b *boardPage) updateButtons() {
	a := b.current()
	var st fleet.Status
	if a != nil {
		st = a.Status
	}
	live := a != nil && st.Live()
	b.btn.start.SetEnabled(a != nil && !live)
	b.btn.hold.SetEnabled(live && st != fleet.StatusHeld)
	b.btn.resume.SetEnabled(live && st == fleet.StatusHeld)
	b.btn.stop.SetEnabled(live)
	b.btn.kill.SetEnabled(live)
	b.btn.open.SetEnabled(a != nil)
}

// fmtCap drops the cents from a round cap: "$10" reads faster than "$10.00".
func fmtCap(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("$%d", int64(v))
	}
	return fmtUSD(v)
}

// paintSpark draws an agent's last minute of token burn behind its Burn
// figure: a faint filled area under a thin line, scaled to its own peak, so
// a busy agent is visible at a glance without reading numbers.
func (b *boardPage) paintSpark(p *qt.QPainter, v []float64, x, y, w, h float64) {
	peak := 0.0
	for _, s := range v {
		peak = max(peak, s)
	}
	if peak <= 0 || len(v) < 2 {
		return
	}
	peak *= 1.3 // headroom, so a steady burn reads as a band, not a ceiling
	pal := b.app.pal
	line := qt.NewQPainterPath()
	defer line.Delete()
	step := w / float64(len(v)-1)
	for i, s := range v {
		px, py := x+float64(i)*step, y+h-h*s/peak
		if i == 0 {
			line.MoveTo2(px, py)
		} else {
			line.LineTo2(px, py)
		}
	}
	area := qt.NewQPainterPath3(line)
	defer area.Delete()
	area.LineTo2(x+w, y+h)
	area.LineTo2(x, y+h)
	area.CloseSubpath()

	p.SetRenderHint(qt.QPainter__Antialiasing)
	c := pal.chartLine(0)
	fill := pal.charts[0].q(0.16)
	br := qt.NewQBrush3(fill)
	p.FillPath(area, br)
	br.Delete()
	fill.Delete()
	lc := c.q(0.75)
	pen := qt.NewQPen3(lc)
	pen.SetWidthF(1.2)
	p.SetPenWithPen(pen)
	p.SetBrushWithStyle(qt.NoBrush)
	p.DrawPath(line)
	pen.Delete()
	lc.Delete()
}
