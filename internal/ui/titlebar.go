package ui

import (
	qt "github.com/mappu/miqt/qt6"
)

// Commander draws its own title bar, as Atlas Monitor does: the window is
// frameless and the header is the bar. The system does the moving and
// resizing (QWindow.StartSystemMove / StartSystemResize), so snapping, edge
// tiling and the compositor's animations still work on Linux and Windows.

const (
	windowRadius = 12 // the rounded corners of a floating window
	edgeGrip     = 6  // the resize margin round a floating window
	winButtonD   = 26 // the round minimise, maximise and close buttons
)

// maximizedNow is whether the window fills the screen, where the corners
// are square and there is no resize margin.
func (a *App) maximizedNow() bool { return a.win.IsMaximized() || a.win.IsFullScreen() }

// corner is the radius the window's surfaces round their outer corners to:
// none when maximised, and none where the window cannot be translucent (the
// corners would show a solid backdrop instead of the desktop).
func (a *App) corner() float64 {
	if !transparencyPlatform() || a.maximizedNow() {
		return 0
	}
	return windowRadius
}

// syncChrome follows the window between floating and maximised: the resize
// margin and the rounding both go away when it is maximised.
func (a *App) syncChrome() {
	max := a.maximizedNow()
	if max == a.chromeMax && a.chromeSet {
		return
	}
	a.chromeMax, a.chromeSet = max, true
	m := edgeGrip
	if max {
		m = 0
	}
	a.rootLayout.SetContentsMargins(m, m, m, m)
	a.frame.Update()
	a.sheet.Update()
	if a.header.max != nil {
		a.header.max.w.Update()
	}
}

// toggleMaximize is the maximise button and the header's double-click.
func (a *App) toggleMaximize() {
	if a.maximizedNow() {
		a.win.ShowNormal()
	} else {
		a.win.ShowMaximized()
	}
}

// dragHeader makes w move the window when pressed, and maximise it when
// double-clicked.
func (a *App) dragHeader(w *qt.QWidget) {
	w.OnMousePressEvent(func(super func(*qt.QMouseEvent), ev *qt.QMouseEvent) {
		if ev.Button() != qt.LeftButton {
			super(ev)
			return
		}
		if h := a.win.WindowHandle(); h != nil {
			h.StartSystemMove()
		}
	})
	w.OnMouseDoubleClickEvent(func(super func(*qt.QMouseEvent), ev *qt.QMouseEvent) {
		if ev.Button() == qt.LeftButton {
			a.toggleMaximize()
		}
	})
}

// edgesAt is which window edges a point in the root widget is within the
// resize margin of; 0 when it is not (or the window is maximised).
func (a *App) edgesAt(x, y, w, h int) qt.Edge {
	if a.maximizedNow() {
		return 0
	}
	var e qt.Edge
	if x < edgeGrip {
		e |= qt.LeftEdge
	} else if x >= w-edgeGrip {
		e |= qt.RightEdge
	}
	if y < edgeGrip {
		e |= qt.TopEdge
	} else if y >= h-edgeGrip {
		e |= qt.BottomEdge
	}
	return e
}

func edgeCursor(e qt.Edge) qt.CursorShape {
	switch e {
	case qt.LeftEdge, qt.RightEdge:
		return qt.SizeHorCursor
	case qt.TopEdge, qt.BottomEdge:
		return qt.SizeVerCursor
	case qt.LeftEdge | qt.TopEdge, qt.RightEdge | qt.BottomEdge:
		return qt.SizeFDiagCursor
	}
	return qt.SizeBDiagCursor
}

// makeResizable lets root, whose margin is the resize grip, start system
// resizes. Only the margin ever sees these events: the frame inside it takes
// its own mouse input, and sets the plain arrow so it does not inherit the
// resize cursor.
func (a *App) makeResizable(root *qt.QWidget) {
	root.SetMouseTracking(true)
	last := qt.Edge(-1)
	root.OnMouseMoveEvent(func(super func(*qt.QMouseEvent), ev *qt.QMouseEvent) {
		p := ev.Position()
		e := a.edgesAt(int(p.X()), int(p.Y()), root.Width(), root.Height())
		if e == last || e == 0 {
			return
		}
		last = e
		c := qt.NewQCursor2(edgeCursor(e))
		root.SetCursor(c)
		c.Delete()
	})
	root.OnMousePressEvent(func(super func(*qt.QMouseEvent), ev *qt.QMouseEvent) {
		p := ev.Position()
		e := a.edgesAt(int(p.X()), int(p.Y()), root.Width(), root.Height())
		if e == 0 || ev.Button() != qt.LeftButton {
			return
		}
		if h := a.win.WindowHandle(); h != nil {
			h.StartSystemResize(e)
		}
	})
}

// winButton is one of the round buttons at the right of the header, drawn as
// Monitor's are: a faint disc with a small glyph that darkens on hover.
type winButton struct {
	w     *qt.QWidget
	kind  string // "minimize", "maximize" or "close"
	hover bool
}

func (a *App) newWinButton(kind, tip string, click func()) *winButton {
	b := &winButton{w: qt.NewQWidget2(), kind: kind}
	b.w.SetFixedSize2(winButtonD, winButtonD)
	b.w.SetToolTip(tip)
	b.w.SetAccessibleName(tip)
	pointer(b.w)
	b.w.OnEnterEvent(func(super func(*qt.QEnterEvent), ev *qt.QEnterEvent) { b.hover = true; b.w.Update() })
	b.w.OnLeaveEvent(func(super func(*qt.QEvent), ev *qt.QEvent) { b.hover = false; b.w.Update() })
	b.w.OnMouseReleaseEvent(func(super func(*qt.QMouseEvent), ev *qt.QMouseEvent) {
		p := ev.Position()
		if ev.Button() == qt.LeftButton && p.X() >= 0 && p.Y() >= 0 && int(p.X()) < winButtonD && int(p.Y()) < winButtonD {
			click()
		}
	})
	// A press must not reach the header underneath and start a window move.
	b.w.OnMousePressEvent(func(super func(*qt.QMouseEvent), ev *qt.QMouseEvent) {})
	b.w.OnPaintEvent(func(super func(*qt.QPaintEvent), ev *qt.QPaintEvent) { b.paint(a) })
	return b
}

func (b *winButton) paint(a *App) {
	pal := a.pal
	p := qt.NewQPainter2(b.w.QPaintDevice)
	defer p.Delete()
	defer p.End()
	p.SetRenderHint(qt.QPainter__Antialiasing)
	c := winButtonD / 2.0
	bg := 0.08
	if b.hover {
		bg = 0.16
	}
	fillEllipse(p, c, c, c, pal.fg, bg)
	g := 4.0 // half the glyph
	switch b.kind {
	case "minimize":
		fillRound(p, c-g, c+g-2, 2*g, 1.8, 0.9, pal.fg, 0.9)
	case "maximize":
		strokeRound(p, c-g+0.5, c-g+0.5, 2*g-1, 2*g-1, 1.5, pal.fg, 0.9, 1.6)
	case "close":
		col := pal.fg.q(0.9)
		pen := qt.NewQPen3(col)
		pen.SetWidthF(1.6)
		pen.SetCapStyle(qt.RoundCap)
		p.SetPenWithPen(pen)
		for _, l := range [][4]float64{{c - g + 0.5, c - g + 0.5, c + g - 0.5, c + g - 0.5}, {c + g - 0.5, c - g + 0.5, c - g + 0.5, c + g - 0.5}} {
			line := qt.NewQLineF3(l[0], l[1], l[2], l[3])
			p.DrawLine(line)
			line.Delete()
		}
		pen.Delete()
		col.Delete()
	}
}
