package ui

import (
	"math"

	qt "github.com/mappu/miqt/qt6"
)

// Series is one line on a Chart. Values are oldest first; the newest sample
// sits at the right edge.
type Series struct {
	Values []float64
	Color  int  // index into the theme's chart colours
	Dashed bool // second series of a pair (Monitor: write, upload, swap)
}

// Chart is Atlas Monitor's chart, drawn with QPainter: a caption band above
// (label, current value, peak), a bordered plot with a square grid, a wash
// fill under a thin line, and the time span below. Nothing is animated: the
// owner sets new values and calls Update on each UI tick.
type Chart struct {
	W *qt.QWidget

	ui     *App
	Label  string
	Span   string // bottom-left caption, e.g. "60 minutes"
	Max    float64
	Format func(float64) string
	Series []Series
	// Samples is how many points span the full width. Shorter series start
	// partway across, as in Monitor, so a fresh chart fills from the right.
	Samples int
}

func newChart(app *App, label, span string, height int, format func(float64) string) *Chart {
	c := &Chart{W: qt.NewQWidget2(), ui: app, Label: label, Span: span, Format: format, Samples: 60}
	c.W.SetMinimumHeight(height)
	c.W.SetMaximumHeight(height)
	c.W.OnPaintEvent(func(super func(*qt.QPaintEvent), ev *qt.QPaintEvent) { c.paint() })
	return c
}

const (
	captionPad = 4
	gridCell   = 36 // Monitor's target cell; rows = round(plotH/36), clamped 2..10
)

func (c *Chart) paint() {
	p := c.ui.pal
	painter := qt.NewQPainter2(c.W.QPaintDevice)
	defer painter.Delete()
	defer painter.End()
	painter.SetRenderHint(qt.QPainter__Antialiasing)

	font := c.W.Font()
	small := qt.NewQFont5(font)
	defer small.Delete()
	// A font set in pixels reports a point size of -1; scaling that would
	// make the font invalid, so fall back to its pixel size.
	if pt := font.PointSizeF(); pt > 0 {
		small.SetPointSizeF(pt * 0.82)
	} else if px := font.PixelSize(); px > 0 {
		small.SetPixelSize(max(1, int(float64(px)*0.82)))
	}
	painter.SetFont(small)
	fm := qt.NewQFontMetrics(small)
	defer fm.Delete()

	w := float64(c.W.Width())
	h := float64(c.W.Height())
	band := float64(fm.Height() + captionPad)
	top, plotH := band, h-2*band
	if plotH < 10 || w < 20 {
		return
	}

	// Grid: faint, crisp (half-pixel aligned), square cells anchored to the
	// right edge so new samples appear to push it left.
	rows := int(math.Round(plotH / gridCell))
	rows = max(2, min(10, rows))
	cell := plotH / float64(rows)
	gc := p.fg.q(0.08)
	defer gc.Delete()
	painter.SetPen(gc)
	for i := 1; i < rows; i++ {
		y := math.Floor(top+cell*float64(i)) + 0.5
		painter.DrawLine4(ptf(0, y), ptf(w, y))
	}
	for x := w - cell; x > 1; x -= cell {
		xx := math.Floor(x) + 0.5
		painter.DrawLine4(ptf(xx, top), ptf(xx, top+plotH))
	}

	scale := c.Max
	peak := 0.0
	for _, s := range c.Series {
		for _, v := range s.Values {
			if finite(v) {
				peak = math.Max(peak, v)
			}
		}
	}
	if scale <= 0 {
		scale = math.Max(peak*1.25, 1e-9)
	}

	painter.Save()
	painter.SetClipRect(rectf(0, top, w, plotH))
	n := max(c.Samples, 2)
	dx := w / float64(n-1)
	bottom := top + plotH
	for _, s := range c.Series {
		vals := s.Values
		if len(vals) > n {
			vals = vals[len(vals)-n:]
		}
		if len(vals) < 2 {
			continue
		}
		line := qt.NewQPainterPath()
		fill := qt.NewQPainterPath()
		started := false
		for i, v := range vals {
			if !finite(v) {
				continue // a NaN or Inf would poison the whole path
			}
			x := w - dx*float64(len(vals)-1-i)
			y := bottom - math.Max(0, math.Min(1, v/scale))*plotH
			pt := ptf(x, y)
			if !started {
				started = true
				line.MoveTo(pt)
				fill.MoveTo(ptf(x, bottom))
			}
			line.LineTo(pt)
			fill.LineTo(pt)
		}
		fill.LineTo(ptf(w, bottom))
		fill.CloseSubpath()

		alpha := 0.22
		if !p.t.Dark {
			alpha = 0.16
		}
		fc := p.charts[s.Color%len(p.charts)].q(alpha)
		fb := qt.NewQBrush3(fc)
		painter.FillPath(fill, fb)

		lc := p.chartLine(s.Color).q(1)
		lb := qt.NewQBrush3(lc)
		pen := qt.NewQPen4(lb, 1.5)
		if s.Dashed {
			// Qt dash lengths are in pen widths: 4 px on, 3 px off.
			pen.SetDashPattern([]float64{4 / 1.5, 3 / 1.5})
		}
		painter.StrokePath(line, pen)

		pen.Delete()
		lb.Delete()
		lc.Delete()
		fb.Delete()
		fc.Delete()
		fill.Delete()
		line.Delete()
	}
	painter.Restore()

	// Border over the series.
	bc := p.fg.q(0.30)
	defer bc.Delete()
	painter.SetPen(bc)
	painter.SetBrushWithStyle(qt.NoBrush)
	painter.DrawRect(rectf(0.5, top+0.5, w-1, plotH-1))

	// Captions. Label dim, current value bright, peak or max at the right.
	cur := 0.0
	if len(c.Series) > 0 && len(c.Series[0].Values) > 0 {
		v := c.Series[0].Values
		if cur = v[len(v)-1]; !finite(cur) {
			cur = 0
		}
	}
	format := c.Format
	if format == nil {
		format = func(v float64) string { return fmtFloat(v) }
	}
	textY := 2.0
	lblC := p.fg.q(0.66)
	defer lblC.Delete()
	valC := p.fg.q(0.92)
	defer valC.Delete()
	dimC := p.fg.q(0.5)
	defer dimC.Delete()

	painter.SetPen(lblC)
	lbl := c.Label + "  "
	painter.DrawText5(rectf(1, textY, w, band), int(qt.AlignLeft|qt.AlignTop), lbl)
	lw := float64(fm.HorizontalAdvance(lbl))
	painter.SetPen(valC)
	val := format(cur)
	painter.DrawText5(rectf(1+lw, textY, w, band), int(qt.AlignLeft|qt.AlignTop), val)
	used := lw + float64(fm.HorizontalAdvance(val))

	right := "peak " + format(peak)
	if c.Max > 0 {
		right = format(c.Max)
	}
	if float64(fm.HorizontalAdvance(right))+used+12 < w {
		painter.SetPen(dimC)
		painter.DrawText5(rectf(0, textY, w-1, band), int(qt.AlignRight|qt.AlignTop), right)
	}
	painter.SetPen(dimC)
	painter.DrawText5(rectf(1, bottom+textY, w, band), int(qt.AlignLeft|qt.AlignTop), c.Span)
	painter.DrawText5(rectf(0, bottom+textY, w-1, band), int(qt.AlignRight|qt.AlignTop), "0")
}

// ptf and rectf make Qt geometry. They are tiny value objects; the painter
// copies them, and the finalizer frees them, so callers need not.
func ptf(x, y float64) *qt.QPointF {
	p := qt.NewQPointF3(x, y)
	p.GoGC()
	return p
}

func rectf(x, y, w, h float64) *qt.QRectF {
	r := qt.NewQRectF4(x, y, w, h)
	r.GoGC()
	return r
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
