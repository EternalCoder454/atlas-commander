package ui

import (
	"slices"

	qt "github.com/mappu/miqt/qt6"
)

// navItem is one row in the sidebar. Group rows are the small uppercase
// labels between sections; they are not selectable.
type navItem struct {
	id    string
	title string
	group bool
	badge func() string // live value at the right (counts, cost); may be nil
	tip   string        // one sentence for the hover tooltip
}

// sidebar is painted by hand rather than built from a QListView so it can
// match Atlas Monitor exactly: 38px rows in a 6px gutter, 5px radius, an 8%
// fill on the active row and a 3x16px accent pill at its left edge, 5% on
// hover. Each row has a 20px icon before its label. The sidebar is always
// shown; there is no collapsed state. Settings is pinned to the bottom.
type sidebar struct {
	W *qt.QWidget

	app      *App
	items    []navItem
	footer   []navItem
	active   string
	hover    string
	onSelect func(id string)

	// rects are rebuilt on every paint so hit-testing matches what is drawn.
	rects map[string][4]int
}

const (
	sidebarWidth  = 220
	navRowH       = 38
	navRowPitch   = 40
	navGutter     = 6
	navGroupTop   = 14
	navGroupBelow = 4
	navIconPx     = 20 // Monitor's icon size
	navIconX      = 14 // icon's left edge inside the row
	navLabelX     = 44 // label's left edge inside the row
)

func newSidebar(app *App, items, footer []navItem) *sidebar {
	s := &sidebar{W: qt.NewQWidget2(), app: app, items: items, footer: footer, rects: map[string][4]int{}}
	setName(s.W, "sidebar")
	s.W.SetFixedWidth(sidebarWidth)
	s.W.SetMouseTracking(true)
	s.W.OnPaintEvent(func(super func(*qt.QPaintEvent), ev *qt.QPaintEvent) { s.paint() })
	s.W.OnMouseMoveEvent(func(super func(*qt.QMouseEvent), ev *qt.QMouseEvent) {
		pos := ev.Position()
		h := s.hit(int(pos.X()), int(pos.Y()))
		if h != s.hover {
			s.hover = h
			s.W.SetToolTip(s.tipFor(h))
			s.W.Update()
		}
	})
	s.W.OnLeaveEvent(func(super func(*qt.QEvent), ev *qt.QEvent) {
		if s.hover != "" {
			s.hover = ""
			s.W.Update()
		}
	})
	s.W.OnMousePressEvent(func(super func(*qt.QMouseEvent), ev *qt.QMouseEvent) {
		if ev.Button() != qt.LeftButton {
			return
		}
		pos := ev.Position()
		h := s.hit(int(pos.X()), int(pos.Y()))
		if h != "" {
			s.select_(h)
		}
	})
	return s
}

// setItems swaps the rows, as when the layout changes. Hover and hit
// rectangles belong to the old rows, so they are dropped; the next paint
// rebuilds the rectangles.
func (s *sidebar) setItems(items []navItem) {
	s.items = items
	s.hover = ""
	clear(s.rects)
	s.W.SetToolTip("")
	s.W.Update()
}

func (s *sidebar) select_(id string) {
	if id == s.active {
		return
	}
	s.active = id
	s.W.Update()
	if s.onSelect != nil {
		s.onSelect(id)
	}
}

func (s *sidebar) hit(x, y int) string {
	for id, r := range s.rects {
		if x >= r[0] && x < r[0]+r[2] && y >= r[1] && y < r[1]+r[3] {
			return id
		}
	}
	return ""
}

func (s *sidebar) paint() {
	p := s.app.pal
	painter := qt.NewQPainter2(s.W.QPaintDevice)
	defer painter.Delete()
	defer painter.End()
	painter.SetRenderHint(qt.QPainter__Antialiasing)

	w := s.W.Width()
	clear(s.rects)

	font := s.W.Font()
	groupFont := qt.NewQFont5(font)
	defer groupFont.Delete()
	groupFont.SetPointSizeF(font.PointSizeF() * 0.74)
	groupFont.SetBold(true)
	groupFont.SetLetterSpacing(qt.QFont__PercentageSpacing, 107)
	badgeFont := qt.NewQFont5(font)
	defer badgeFont.Delete()
	badgeFont.SetPointSizeF(font.PointSizeF() * 0.85)
	mono := s.app.monoFont(0.85)
	defer mono.Delete()

	text := p.fg.q(1)
	defer text.Delete()
	groupC := p.fg.q(0.48)
	defer groupC.Delete()
	badgeC := p.fg.q(0.55)
	defer badgeC.Delete()

	y := 6
	drawRow := func(it navItem) {
		x := navGutter
		rw := w - 2*navGutter
		s.rects[it.id] = [4]int{x, y, rw, navRowH}
		var fill float64
		switch {
		case it.id == s.active:
			fill = 0.08
		case it.id == s.hover:
			fill = 0.05
		}
		if fill > 0 {
			c := p.fg.q(fill)
			b := qt.NewQBrush3(c)
			painter.SetPenWithStyle(qt.NoPen)
			painter.SetBrush(b)
			painter.DrawRoundedRect(rectf(float64(x), float64(y), float64(rw), navRowH), 5, 5)
			b.Delete()
			c.Delete()
		}
		if it.id == s.active {
			c := p.accent.q(1)
			b := qt.NewQBrush3(c)
			painter.SetBrush(b)
			painter.DrawRoundedRect(rectf(float64(x), float64(y+(navRowH-16)/2), 3, 16), 1.5, 1.5)
			b.Delete()
			c.Delete()
		}
		painter.SetFont(font)
		painter.SetPen(text)
		// The icon is named for the row's id, so the two cannot drift apart.
		dpr := s.W.DevicePixelRatioF()
		painter.DrawPixmap9(x+navIconX, y+(navRowH-navIconPx)/2, iconPixmap(it.id, p.fg, 0.9, navIconPx, dpr))
		painter.DrawText7(x+navLabelX, y, rw-navLabelX-12, navRowH, int(qt.AlignLeft|qt.AlignVCenter), it.title)
		if it.badge != nil {
			if v := it.badge(); v != "" {
				painter.SetFont(mono)
				painter.SetPen(badgeC)
				painter.DrawText7(x+12, y, rw-22, navRowH, int(qt.AlignRight|qt.AlignVCenter), v)
			}
		}
		y += navRowPitch
	}

	for _, it := range s.items {
		if it.group {
			y += navGroupTop - 6
			painter.SetFont(groupFont)
			painter.SetPen(groupC)
			gm := qt.NewQFontMetrics(groupFont)
			gh := gm.Height()
			gm.Delete()
			painter.DrawText7(18, y, w-36, gh, int(qt.AlignLeft|qt.AlignVCenter), upper(it.title))
			y += gh + navGroupBelow
			continue
		}
		drawRow(it)
	}

	// Footer rows sit at the bottom: padding 8 below, as in Monitor.
	y = s.W.Height() - 8 - len(s.footer)*navRowPitch + (navRowPitch - navRowH)
	for _, it := range s.footer {
		drawRow(it)
	}
}

// pageFrame paints the rounded sheet the views sit on: the page colour with
// a 10px top-left radius and a 1px hairline on the top and left edges only,
// over the window's frame colour. That is what makes the sidebar and the
// title area read as one surface and the page as a sheet above it.
func (a *App) paintPage(w *qt.QWidget) {
	p := a.pal
	painter := qt.NewQPainter2(w.QPaintDevice)
	defer painter.Delete()
	defer painter.End()
	painter.SetRenderHint(qt.QPainter__Antialiasing)

	fw, fh := float64(w.Width()), float64(w.Height())
	const r = 10.0

	path := qt.NewQPainterPath()
	defer path.Delete()
	path.MoveTo(ptf(0, fh))
	path.LineTo(ptf(0, r))
	path.ArcTo(rectf(0, 0, 2*r, 2*r), 180, -90)
	path.LineTo(ptf(fw, 0))
	if cr := a.corner(); cr > 0 {
		// The window's bottom-right corner is round; the sheet follows it.
		path.LineTo(ptf(fw, fh-cr))
		path.ArcTo(rectf(fw-2*cr, fh-2*cr, 2*cr, 2*cr), 0, -90)
	} else {
		path.LineTo(ptf(fw, fh))
	}
	path.CloseSubpath()
	pc := p.page.q(a.glass.Page)
	defer pc.Delete()
	pb := qt.NewQBrush3(pc)
	defer pb.Delete()
	painter.FillPath(path, pb)

	edge := qt.NewQPainterPath()
	defer edge.Delete()
	edge.MoveTo(ptf(0.5, fh))
	edge.LineTo(ptf(0.5, r))
	edge.ArcTo(rectf(0.5, 0.5, 2*r, 2*r), 180, -90)
	edge.LineTo(ptf(fw, 0.5))
	hc := p.fg.q(0.07)
	defer hc.Delete()
	hb := qt.NewQBrush3(hc)
	defer hb.Delete()
	pen := qt.NewQPen4(hb, 1)
	defer pen.Delete()
	painter.StrokePath(edge, pen)
}

// tipFor is the tooltip text of the item with this id, or "" for none.
func (s *sidebar) tipFor(id string) string {
	for _, it := range append(slices.Clone(s.items), s.footer...) {
		if it.id == id {
			return it.tip
		}
	}
	return ""
}
