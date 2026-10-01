package ui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unsafe"

	qt "github.com/mappu/miqt/qt6"
)

// The Atlas intro, ported from Atlas Monitor's internal/ui/intro.go: the mark
// draws itself the way assets/atlas-mark-animated.svg does (left leg rising,
// right leg falling from the fold, the crossbar sliding in), then moves aside
// while the product's name slides out from under its right leg, and the whole
// thing fades into the window. Monitor and Notes draw it with Cairo; this is
// the same animation on QPainter, with the same timings, curves and paths, so
// a change there belongs here too.

const (
	introSlideAt = 1.10 // the mark starts moving aside for the name
	introSlide   = 0.75
	introFadeAt  = 2.55 // the intro starts fading into the window
	introFade    = 0.40
	introSkip    = 0.22 // how long the fade takes when a click or key cuts it short
)

// introMarkHeight is the mark's height, in logical pixels, where the window
// has room for it.
const introMarkHeight = 148.0

// intro covers the window while it plays. It is a child of the main window,
// above everything, and paints its own background, so the content under it
// shows through only as the intro fades.
type intro struct {
	app     *App
	w       *qt.QWidget
	product string
	onDone  func()

	start   time.Time
	t       float64
	fadeAt  float64
	fadeDur float64
	timer   *qt.QTimer
	filter  *qt.QObject
	// resizer follows the window's size from an event filter, so it doesn't
	// take over the window's own OnResizeEvent handler.
	resizer *qt.QObject
	running bool // the frame timer has been started
	done    bool
	opacity float64 // the whole intro's, during the fade

	name *productName
}

func newIntro(a *App, product string, onDone func()) *intro {
	in := &intro{app: a, product: product, onDone: onDone, fadeAt: introFadeAt, fadeDur: introFade, opacity: 1}
	in.w = qt.NewQWidget(a.win.QWidget)
	in.w.SetGeometry(0, 0, a.win.Width(), a.win.Height())
	in.w.OnPaintEvent(func(super func(*qt.QPaintEvent), ev *qt.QPaintEvent) { in.paint() })
	in.w.OnMousePressEvent(func(super func(*qt.QMouseEvent), e *qt.QMouseEvent) { in.skip() })
	in.resizer = qt.NewQObject()
	in.resizer.OnEventFilter(func(super func(*qt.QObject, *qt.QEvent) bool, watched *qt.QObject, ev *qt.QEvent) bool {
		if ev.Type() == qt.QEvent__Resize && !in.done {
			in.w.SetGeometry(0, 0, a.win.Width(), a.win.Height())
		}
		return false
	})
	a.win.InstallEventFilter(in.resizer)
	// The clock runs from the first show, wherever that is triggered from.
	in.w.OnShowEvent(func(super func(*qt.QShowEvent), e *qt.QShowEvent) {
		super(e)
		in.begin()
	})

	// Any key cuts it short, and still goes on to whatever has focus: someone
	// who starts typing straight away should not lose the first letter.
	in.filter = qt.NewQObject()
	in.filter.OnEventFilter(func(super func(*qt.QObject, *qt.QEvent) bool, watched *qt.QObject, ev *qt.QEvent) bool {
		if ev.Type() == qt.QEvent__KeyPress {
			in.skip()
		}
		return false
	})
	a.qapp.InstallEventFilter(in.filter)

	in.timer = qt.NewQTimer()
	in.timer.SetTimerType(qt.PreciseTimer)
	in.timer.OnTimeout(in.tick)
	in.w.Raise()
	in.w.Show()
	return in
}

// begin starts the frame timer; call it as the window is shown. The clock
// itself starts at the first frame painted, so a slow start-up doesn't eat
// the opening of the animation.
func (in *intro) begin() {
	if in.running || in.done {
		return
	}
	in.running = true
	in.timer.Start(16)
}

// skip starts the fade now, if it has not started already.
func (in *intro) skip() {
	if in.done || in.t >= in.fadeAt {
		return
	}
	in.fadeAt = in.t
	in.fadeDur = introSkip
}

func (in *intro) tick() {
	if in.start.IsZero() {
		in.w.Update()
		return
	}
	in.t = time.Since(in.start).Seconds()
	if in.t >= in.fadeAt {
		// Fading, it lets clicks through to the window it is uncovering.
		in.w.SetAttribute2(qt.WA_TransparentForMouseEvents, true)
		f := tween(in.t, in.fadeAt, in.fadeDur, 0, 1, splineStandard)
		in.opacity = 1 - f
		if f >= 1 {
			in.finish()
			return
		}
	}
	in.w.Update()
}

func (in *intro) finish() {
	if in.done {
		return
	}
	in.done = true
	in.timer.Stop()
	in.app.qapp.RemoveEventFilter(in.filter)
	in.app.win.RemoveEventFilter(in.resizer)
	in.w.Hide()
	in.w.DeleteLater()
	in.timer.DeleteLater()
	in.filter.DeleteLater()
	in.resizer.DeleteLater()
	if in.name != nil {
		in.name.free()
		in.name = nil
	}
	if in.onDone != nil {
		in.onDone()
	}
}

func (in *intro) paint() {
	if in.done { // name was freed; don't build it again for a late paint
		return
	}
	w, h := float64(in.w.Width()), float64(in.w.Height())
	if w <= 0 || h <= 0 {
		return
	}
	if in.start.IsZero() {
		in.start = time.Now()
	}
	p := qt.NewQPainter2(in.w.QPaintDevice)
	defer p.Delete()
	defer p.End()
	p.SetRenderHint(qt.QPainter__Antialiasing)
	p.SetRenderHint(qt.QPainter__SmoothPixmapTransform)

	pal := in.app.pal
	p.SetOpacity(in.opacity)
	bg := pal.page.q(1)
	p.FillRect5(0, 0, int(w)+1, int(h)+1, bg)
	bg.Delete()

	if in.name == nil {
		in.name = newProductName(in.product)
	}
	t := in.t

	// The lockup, mark to the end of the name, in mark units: the mark, the
	// gap and "Atlas" are where atlas-lockup.svg puts them, then a word space
	// and the product.
	lockup := wordmarkEnd + nameSpace + in.name.width - markLeft

	// Big enough to be the point of the window, small enough to leave the
	// window around it.
	k := math.Min(introMarkHeight/(markBottom-markTop), 0.24*h/(markBottom-markTop))
	k = math.Min(k, 0.84*w/lockup)

	// Where the mark's left edge is: centred alone, then moved so the whole
	// lockup is centred.
	slide := tween(t, introSlideAt, introSlide, 0, 1, splineSlide)
	alone := w/2 - (markRight-markLeft)*k/2
	together := w/2 - lockup*k/2
	left := alone + (together-alone)*slide

	p.Save()
	p.Translate(ptf(left-markLeft*k, h/2-markMidY*k))
	p.Scale(k, k)
	m := markPainter{p: p, base: in.opacity}
	m.draw(t, false)
	if slide > 0 {
		in.name.draw(m, slide, pal.t.Dark)
	}
	p.Restore()
}

// --- The mark ---------------------------------------------------------------

// The mark is drawn in the SVG's own units: a 1024 square, the mark itself
// inside it from x 136 to 888 and y 168 to 872.
const (
	markLeft, markRight = 136.0, 888.0
	markTop, markBottom = 168.0, 872.0
	markMidY            = 520.0
)

const (
	legLeftData  = "M411.9 197.6Q424 168 456 168L590 168Q600 168 596.2 177.3L324.1 842.4Q312 872 280 872L168 872Q136 872 148.1 842.4Z"
	legRightData = "M445.3 191.9Q424 168 456 168L590 168Q600 168 603.8 177.3L875.9 842.4Q888 872 856 872L744 872Q712 872 699.9 842.4L527.1 420.1Q512 383.1 527.1 346.1L544.7 303.1Z"
)

// markShapes are the mark's paths, gradients and glows, made on first use
// and kept for the life of the process (the header and the window icon use
// them too).
type markShapes struct {
	legLeft, legRight, crossbar, crossbarShadow, crossbarHilite *qt.QPainterPath
	apexShadow, apexHilite, wordmark, crossbarClip              *qt.QPainterPath

	gLegLeft, gLegRight, gCrossbar, gApexShadow, gCrossbarShadow, gHighlight *qt.QBrush

	glowLeft, glowRight *qt.QImage
}

var shapes *markShapes

func mark() *markShapes {
	if shapes != nil {
		return shapes
	}
	s := &markShapes{
		legLeft:        mustPath(legLeftData, 1),
		legRight:       mustPath(legRightData, 1),
		crossbar:       mustPath("M346.7 572L677.3 572L732.5 707L291.5 707Z", 1),
		crossbarShadow: mustPath("M545.3 572L591.3 572L646.5 707L600.5 707Z", 1),
		crossbarHilite: mustPath("M430.7 572.5L593.3 572.5L593.3 574.5L430.7 574.5Z", 1),
		apexShadow:     mustPath("M592.6 186L607.4 186L630 241.3L542 456.4L527.1 420.1Q512 383.1 527.1 346.1L544.7 303.1Z", 1),
		apexHilite:     mustPath("M456 168.5L586 168.5L586 170.5L456 170.5Z", 1),
		wordmark:       mustPath(wordmarkData, wordmarkScale),
		crossbarClip:   mustPath("M-1400 0L460 0L878.9 1024L-1400 1024Z", 1),

		gLegLeft: linear(220, 168, 340, 872,
			hexStop(0, "#C3B8FF", 1), hexStop(1, "#9C8CF8", 1)),
		gLegRight: linear(600, 168, 660, 872,
			hexStop(0, "#251B72", 1), hexStop(.3, "#7262EA", 1), hexStop(1, "#6252DD", 1)),
		gCrossbar: linear(434.7, 572, 644.5, 707,
			hexStop(0, "#8A7AF4", 1), hexStop(1, "#7A69EE", 1)),
		gApexShadow: linear(546, 300, 601.5, 322.7,
			hexStop(0, "#1A2A80", .45), hexStop(1, "#1A2A80", 0)),
		gCrossbarShadow: linear(576.2, 656.2, 616.9, 639.5,
			hexStop(0, "#1A2A80", 0), hexStop(1, "#1A2A80", .42)),
		gHighlight: linear(360, 0, 900, 0,
			hexStop(0, "#FFE6F7", .8), hexStop(1, "#FFE6F7", .35)),
	}
	s.glowRight = glowImage(s.legRight, "#7262EA")
	s.glowLeft = glowImage(s.legLeft, "#C3B8FF")
	shapes = s
	return s
}

type stop struct {
	at    float64
	color string
	alpha float64
}

func hexStop(at float64, hex string, alpha float64) stop { return stop{at, hex, alpha} }

// linear is a linear gradient brush in mark units.
func linear(x1, y1, x2, y2 float64, stops ...stop) *qt.QBrush {
	g := qt.NewQLinearGradient3(x1, y1, x2, y2)
	defer g.Delete()
	for _, s := range stops {
		c := parseHex(s.color).q(s.alpha)
		g.SetColorAt(s.at, c)
		c.Delete()
	}
	return qt.NewQBrush10(g.QGradient)
}

// shine is the light that sweeps across the mark once it is whole, offset by
// (dx, dy) along its path. The caller deletes it.
func shine(dx, dy float64) *qt.QBrush {
	c := "#FFF3FB"
	return linear(dx, dy, 1024+dx, 614+dy,
		hexStop(0, c, 0), hexStop(.40, c, 0),
		hexStop(.455, c, .22), hexStop(.485, c, .5), hexStop(.505, c, .22),
		hexStop(.53, c, 0), hexStop(.545, c, 0),
		hexStop(.555, c, .38), hexStop(.565, c, 0),
		hexStop(1, c, 0))
}

// The curves the SVG names in keySplines.
var (
	splineStandard = [4]float64{.4, 0, .2, 1}
	splineSettle   = [4]float64{.16, 1, .3, 1}
	splineRise     = [4]float64{.55, 0, .85, .55}
	splineFall     = [4]float64{.15, .45, .35, 1}
	splineSweep    = [4]float64{.2, .8, .2, 1}
	splineGlow     = [4]float64{.45, 0, .4, 1}
	splineShine    = [4]float64{.5, 0, .3, 1}
	splineSlide    = [4]float64{.65, 0, .25, 1}
)

// tween is SMIL's <animate> with calcMode="spline": from until begin, to after
// begin+dur, and the cubic Bézier sp in between.
func tween(t, begin, dur, from, to float64, sp [4]float64) float64 {
	switch {
	case t <= begin:
		return from
	case t >= begin+dur:
		return to
	}
	return from + (to-from)*bezier((t-begin)/dur, sp)
}

// bezier solves the CSS-style timing curve through (0,0), (x1,y1), (x2,y2),
// (1,1) for x and returns its y.
func bezier(x float64, sp [4]float64) float64 {
	x1, y1, x2, y2 := sp[0], sp[1], sp[2], sp[3]
	at := func(a, b, s float64) float64 {
		return 3*a*s*(1-s)*(1-s) + 3*b*s*s*(1-s) + s*s*s
	}
	lo, hi := 0.0, 1.0
	s := x
	for range 24 {
		if v := at(x1, x2, s); math.Abs(v-x) < 1e-5 {
			break
		} else if v < x {
			lo = s
		} else {
			hi = s
		}
		s = (lo + hi) / 2
	}
	return at(y1, y2, s)
}

// markPainter draws the mark on p, in mark units. QPainter's opacity is
// absolute, not multiplied, so every fill scales by base itself.
type markPainter struct {
	p    *qt.QPainter
	base float64
}

func (m markPainter) fill(path *qt.QPainterPath, b *qt.QBrush, alpha float64) {
	if alpha <= 0 {
		return
	}
	m.p.SetOpacity(m.base * alpha)
	m.p.FillPath(path, b)
}

// draw paints the mark at time t. still draws it finished and at rest, with
// no shine, for the icon and the header.
func (m markPainter) draw(t float64, still bool) {
	s := mark()
	p := m.p
	if still {
		t = 2
	}

	// The whole mark fades in over the first fifth of a second and settles
	// from 94% to full size, around (512, 520).
	alpha := tween(t, 0, .2, 0, 1, splineStandard)
	sc := tween(t, 0, 1.25, .94, 1, splineSettle)
	p.Save()
	p.Translate(ptf(512, 520))
	p.Scale(sc, sc)
	p.Translate(ptf(-512, -520))

	var sh *qt.QBrush
	if t > 1.25 && !still {
		// One sweep, 1.5 s, from above the top left to below the bottom right.
		f := tween(t, 1.25, 1.5, 0, 1, splineShine)
		sh = shine(-1100+2200*f, -660+1320*f)
		defer sh.Delete()
	}

	// Crossbar, under both legs: revealed by a slanted edge sliding right.
	p.Save()
	dx := tween(t, .64, .55, -500, 0, splineSweep)
	p.Translate(ptf(dx, 0))
	p.SetClipPath2(s.crossbarClip, qt.IntersectClip)
	p.Translate(ptf(-dx, 0))
	m.fill(s.crossbar, s.gCrossbar, alpha)
	if sh != nil {
		m.fill(s.crossbar, sh, alpha)
	}
	m.fill(s.crossbarShadow, s.gCrossbarShadow, alpha)
	m.fill(s.crossbarHilite, s.gHighlight, alpha*tween(t, 1.05, .4, 0, 1, splineStandard))
	p.Restore()

	glowRight, glowLeft := glowLevels(t)
	if still {
		glowRight, glowLeft = 0, 0 // the icon has no glow; at small sizes it is only a smudge
	}

	// Right leg: falls from the fold at the top.
	p.Save()
	p.SetClipRect3(rectf(0, 80, 1024, tween(t, .43, .5, 70, 1040, splineFall)), qt.IntersectClip)
	m.glow(s.glowRight, alpha*glowRight)
	m.fill(s.legRight, s.gLegRight, alpha)
	if sh != nil {
		m.fill(s.legRight, sh, alpha)
	}
	p.Restore()

	// Left leg: rises from the bottom.
	p.Save()
	p.SetClipRect3(rectf(0, tween(t, 0, .47, 880, 90, splineRise), 1024, 1024), qt.IntersectClip)
	m.glow(s.glowLeft, alpha*glowLeft)
	m.fill(s.legLeft, s.gLegLeft, alpha)
	if sh != nil {
		m.fill(s.legLeft, sh, alpha)
	}
	p.Restore()

	// The fold's shadow and its edge of light, last, over the legs.
	apex := alpha * tween(t, .62, .35, 0, 1, splineStandard)
	m.fill(s.apexShadow, s.gApexShadow, apex)
	m.fill(s.apexHilite, s.gHighlight, apex*tween(t, .95, .4, 0, 1, splineStandard))

	p.Restore()
	p.SetOpacity(m.base)
}

// glowLevels are the opacities of the soft light behind each leg at t: it
// comes up with the legs and then breathes, as in the animated SVG.
func glowLevels(t float64) (right, left float64) {
	right = tween(t, .15, 1.3, 0, .5, splineGlow)
	left = tween(t, .15, 1.3, 0, .55, splineGlow)
	if t > 1.45 {
		c := math.Mod(t-1.45, 5)
		if c < 1.75 {
			right = tween(c, 0, 1.75, .5, .75, splineStandard)
			left = tween(c, 0, 1.75, .55, .8, splineStandard)
		} else {
			right = tween(c, 1.75, 3.25, .75, .5, [4]float64{.4, 0, .6, 1})
			left = tween(c, 1.75, 3.25, .8, .55, [4]float64{.4, 0, .6, 1})
		}
	}
	return right, left
}

// --- The glow ---------------------------------------------------------------

// glowSize is the glow images' side. The glow is a blur, so a quarter of the
// mark's 1024 units is plenty; it is scaled up smoothly when painted.
const glowSize = 256

func (m markPainter) glow(img *qt.QImage, alpha float64) {
	if img == nil || alpha <= 0 {
		return
	}
	m.p.SetOpacity(m.base * alpha)
	m.p.DrawImage(rectf(0, 0, 1024, 1024), img, rectf(0, 0, glowSize, glowSize))
}

// glowImage is the leg's own shape in colour, blurred as the SVG's
// feGaussianBlur stdDeviation="22" does: three box blurs approximate a
// Gaussian closely.
func glowImage(path *qt.QPainterPath, colour string) *qt.QImage {
	img := qt.NewQImage3(glowSize, glowSize, qt.QImage__Format_ARGB32_Premultiplied)
	img.Fill(0)
	p := qt.NewQPainter2(img.QPaintDevice)
	p.SetRenderHint(qt.QPainter__Antialiasing)
	p.Scale(glowSize/1024.0, glowSize/1024.0)
	c := parseHex(colour).q(1)
	b := qt.NewQBrush3(c)
	p.FillPath(path, b)
	b.Delete()
	c.Delete()
	p.End()
	p.Delete()

	stride := int(img.BytesPerLine())
	px := unsafe.Slice(img.Bits(), stride*glowSize)
	sigma := 22.0 * glowSize / 1024
	r := int(math.Round((math.Sqrt(12*sigma*sigma/3+1) - 1) / 2))
	blurRGBA(px, glowSize, glowSize, stride, r)
	return img
}

// blurRGBA runs three horizontal and three vertical box blurs of radius r
// over premultiplied 32-bit pixels.
func blurRGBA(px []byte, w, h, stride, r int) {
	if r < 1 {
		return
	}
	n := max(w, h)
	line := make([]int, n*4)
	out := make([]byte, n*4)
	pass := func(get func(i, ch int) byte, set func(i, ch int, v byte), length int) {
		for ch := range 4 {
			for i := range length {
				line[i] = int(get(i, ch))
			}
			sum := 0
			for i := -r; i <= r; i++ {
				if i >= 0 && i < length {
					sum += line[i]
				}
			}
			for i := range length {
				out[i] = byte(sum / (2*r + 1))
				if j := i - r; j >= 0 {
					sum -= line[j]
				}
				if j := i + r + 1; j < length {
					sum += line[j]
				}
			}
			for i := range length {
				set(i, ch, out[i])
			}
		}
	}
	for range 3 {
		for y := range h {
			row := px[y*stride:]
			pass(func(i, ch int) byte { return row[i*4+ch] }, func(i, ch int, v byte) { row[i*4+ch] = v }, w)
		}
		for x := range w {
			pass(func(i, ch int) byte { return px[i*stride+x*4+ch] }, func(i, ch int, v byte) { px[i*stride+x*4+ch] = v }, h)
		}
	}
}

// --- The name ---------------------------------------------------------------

// The wordmark, "Atlas", is atlas-lockup.svg's outlines, in that file's units
// (half a mark unit), so it is the brand's own lettering whatever fonts the
// machine has. The product beside it is set in the UI font, medium weight and
// in the mark's purple, with its capitals as tall as the wordmark's.
const (
	wordmarkScale = 2.0             // lockup units to mark units
	wordmarkEnd   = 1277.9 * 2      // where "Atlas" ends, in mark units
	wordmarkCap   = (369 - 151) * 2 // the wordmark's cap height, in mark units
	wordmarkBase  = 369 * 2         // its baseline
	nameSpace     = 190.0           // the word space before the product
)

// productName is the product's name as a path, scaled so its capitals match
// the wordmark's: outlines scale cleanly at any size, where a font at a huge
// point size under a tiny transform may not.
type productName struct {
	path        *qt.QPainterPath
	scale       float64 // path units to mark units
	inkX, width float64 // in mark units: where the ink starts, and its right edge
}

func newProductName(text string) *productName {
	f := qt.NewQFont5(qt.QApplication_Font())
	defer f.Delete()
	f.SetWeight(qt.QFont__Medium)
	f.SetPointSizeF(100)
	h := qt.NewQPainterPath()
	defer h.Delete()
	h.AddText2(0, 0, f, "H")
	capH := h.BoundingRect().Height()
	n := &productName{path: qt.NewQPainterPath(), scale: 1}
	n.path.AddText2(0, 0, f, text)
	if capH > 0 {
		n.scale = wordmarkCap / capH
	}
	br := n.path.BoundingRect()
	n.inkX = br.Left() * n.scale
	n.width = br.Right() * n.scale
	return n
}

func (n *productName) free() { n.path.Delete() }

// draw draws "Atlas" and the product, sliding out from under the mark's right
// leg as slide goes from 0 to 1, in mark units.
func (n *productName) draw(m markPainter, slide float64, dark bool) {
	s := mark()
	p := m.p
	wordmarkStart := 566.2 * wordmarkScale
	travel := (wordmarkEnd + nameSpace + n.width - wordmarkStart) * 0.55
	offset := -(1 - slide) * travel

	p.Save()
	// Everything right of the right leg's outer edge, a little out from it.
	edge := func(y float64) float64 { return 603.8 + 80 + (y-177.3)*(875.9-603.8)/(842.4-177.3) }
	clip := qt.NewQPainterPath()
	clip.MoveTo2(edge(0), 0)
	clip.LineTo2(1e5, 0)
	clip.LineTo2(1e5, 1024)
	clip.LineTo2(edge(1024), 1024)
	clip.CloseSubpath()
	p.SetClipPath2(clip, qt.IntersectClip)
	clip.Delete()

	alpha := tween(slide, 0, .45, 0, 1, splineStandard)
	p.Translate(ptf(offset, 0))

	word, prod := "#1B1748", "#6252DD"
	if dark {
		word, prod = "#FFFFFF", "#C3B8FF"
	}
	wc := parseHex(word).q(1)
	wb := qt.NewQBrush3(wc)
	m.fill(s.wordmark, wb, alpha)
	wb.Delete()
	wc.Delete()

	// The product, its baseline on the wordmark's.
	p.Translate(ptf(wordmarkEnd+nameSpace-n.inkX, wordmarkBase))
	p.Scale(n.scale, n.scale)
	pc := parseHex(prod).q(1)
	pb := qt.NewQBrush3(pc)
	m.fill(n.path, pb, alpha)
	pb.Delete()
	pc.Delete()
	p.Restore()
	p.SetOpacity(m.base)
}

// --- Paths ------------------------------------------------------------------

// mustPath parses an SVG path of absolute M, L, Q and Z commands, which is
// all the logo's files use, into a QPainterPath scaled by k.
func mustPath(d string, k float64) *qt.QPainterPath {
	p, err := parsePath(d, k)
	if err != nil {
		panic(err)
	}
	return p
}

func parsePath(d string, k float64) (*qt.QPainterPath, error) {
	fields := strings.FieldsFunc(d, func(r rune) bool { return r == ' ' || r == ',' })
	// Split commands glued to their numbers ("M411.9", "369ZM639.4").
	var toks []string
	for _, f := range fields {
		for len(f) > 0 {
			if strings.ContainsRune("MLQZ", rune(f[0])) {
				toks = append(toks, f[:1])
				f = f[1:]
				continue
			}
			i := strings.IndexAny(f, "MLQZ")
			if i < 0 {
				toks = append(toks, f)
				break
			}
			toks = append(toks, f[:i])
			f = f[i:]
		}
	}
	nums := func(i, n int) ([4]float64, error) {
		var v [4]float64
		if i+n > len(toks) {
			return v, fmt.Errorf("path ends early")
		}
		for j := range n {
			x, err := strconv.ParseFloat(toks[i+j], 64)
			if err != nil {
				return v, err
			}
			v[j] = x * k
		}
		return v, nil
	}
	path := qt.NewQPainterPath()
	for i := 0; i < len(toks); {
		c := toks[i]
		i++
		switch c {
		case "M", "L":
			v, err := nums(i, 2)
			if err != nil {
				path.Delete()
				return nil, err
			}
			if c == "M" {
				path.MoveTo2(v[0], v[1])
			} else {
				path.LineTo2(v[0], v[1])
			}
			i += 2
		case "Q":
			v, err := nums(i, 4)
			if err != nil {
				path.Delete()
				return nil, err
			}
			path.QuadTo2(v[0], v[1], v[2], v[3])
			i += 4
		case "Z":
			path.CloseSubpath()
		default:
			path.Delete()
			return nil, fmt.Errorf("path command %q not supported", c)
		}
	}
	return path, nil
}

// --- The mark elsewhere -----------------------------------------------------

// markIcon renders the finished mark as the window icon.
func markIcon() *qt.QIcon {
	icon := qt.NewQIcon()
	for _, px := range []int{16, 32, 48, 64, 128, 256} {
		pm := qt.NewQPixmap2(px, px)
		clear := qt.NewQColor2(qt.Transparent)
		pm.FillWithFillColor(clear)
		clear.Delete()
		p := qt.NewQPainter2(pm.QPaintDevice)
		p.SetRenderHint(qt.QPainter__Antialiasing)
		p.SetRenderHint(qt.QPainter__SmoothPixmapTransform)
		k := float64(px) / 1024
		p.Scale(k, k)
		markPainter{p: p, base: 1}.draw(0, true)
		p.End()
		p.Delete()
		icon.AddPixmap(pm)
		pm.Delete()
	}
	return icon
}

// newMarkWidget is the finished mark, size pixels tall, for the header.
func newMarkWidget(size int) *qt.QWidget { return newFaintMark(size, 1) }

// newFaintMark is the mark at the given opacity, for empty states.
func newFaintMark(size int, opacity float64) *qt.QWidget {
	w := qt.NewQWidget2()
	w.SetFixedSize2(size, size)
	w.OnPaintEvent(func(super func(*qt.QPaintEvent), ev *qt.QPaintEvent) {
		p := qt.NewQPainter2(w.QPaintDevice)
		p.SetRenderHint(qt.QPainter__Antialiasing)
		p.SetRenderHint(qt.QPainter__SmoothPixmapTransform)
		// The mark spans 752 by 704 of its 1024 units; fit it to the widget
		// by its wider side and centre it.
		k := float64(size) / (markRight - markLeft)
		p.Translate(ptf(float64(size)/2-(markLeft+markRight)/2*k, float64(size)/2-(markTop+markBottom)/2*k))
		p.Scale(k, k)
		markPainter{p: p, base: opacity}.draw(0, true)
		p.End()
		p.Delete()
	})
	return w
}

// wordmarkData is "Atlas" from atlas-lockup.svg.
const wordmarkData = "M610.2 369L566.2 369L654.0 151L688.4 151L775.8 369L730.9 369L715.7 328.4L625.7 328.4L610.2 369ZM639.4 293.0L702.0 293.0L671.0 210.5L639.4 293.0ZM850.6 369L809.9 369L809.9 254.9L774.9 254.9L774.9 219.2L809.9 219.2L809.9 156.9L850.6 156.9L850.6 219.2L885.6 219.2L885.6 254.9L850.6 254.9L850.6 369ZM949.2 369L908.6 369L908.6 144.8L949.2 144.8L949.2 369ZM1047.8 372.1L1047.8 372.1Q1027.3 372.1 1010.9 361.9Q994.4 351.6 985.1 334.0Q975.8 316.3 975.8 294.3L975.8 294.3Q975.8 271.9 985.1 254.3Q994.4 236.6 1010.9 226.4Q1027.3 216.1 1047.8 216.1L1047.8 216.1Q1063.9 216.1 1076.6 222.6L1076.6 222.6Q1085.0 227.0 1091.2 233.5L1091.2 233.5L1091.2 219.2L1131.5 219.2L1131.5 369L1091.2 369L1091.2 354.4Q1085.0 360.9 1076.6 365.3L1076.6 365.3Q1063.9 372.1 1047.8 372.1ZM1055.2 334.6L1055.2 334.6Q1072.3 334.6 1082.8 323.3Q1093.4 311.9 1093.4 294.0L1093.4 294.0Q1093.4 282.2 1088.6 273.0Q1083.8 263.9 1075.2 258.8Q1066.7 253.6 1055.2 253.6L1055.2 253.6Q1044.1 253.6 1035.5 258.8Q1027.0 263.9 1022.2 273.0Q1017.4 282.2 1017.4 294.0L1017.4 294.0Q1017.4 306.0 1022.2 315.2Q1027.0 324.3 1035.5 329.5Q1044.1 334.6 1055.2 334.6ZM1219.0 372.4L1219.0 372.4Q1206.2 372.4 1194.0 369Q1181.7 365.6 1171.5 359.5Q1161.3 353.5 1153.8 344.8L1153.8 344.8L1178.0 320.3Q1185.8 329.0 1196.0 333.3Q1206.2 337.7 1218.7 337.7L1218.7 337.7Q1228.6 337.7 1233.7 334.9Q1238.8 332.1 1238.8 326.5L1238.8 326.5Q1238.8 320.3 1233.4 316.9Q1228.0 313.5 1219.3 311.2Q1210.6 308.8 1201.1 305.9Q1191.7 302.9 1183.0 298.1Q1174.3 293.3 1168.9 284.8Q1163.5 276.3 1163.5 262.6L1163.5 262.6Q1163.5 248.4 1170.4 237.8Q1177.4 227.3 1190.4 221.4Q1203.5 215.5 1221.1 215.5L1221.1 215.5Q1239.7 215.5 1254.8 222.0Q1269.8 228.5 1279.7 241.5L1279.7 241.5L1255.2 266.0Q1248.4 257.7 1239.9 254.0Q1231.4 250.2 1221.4 250.2L1221.4 250.2Q1212.4 250.2 1207.6 253.0Q1202.8 255.8 1202.8 260.8L1202.8 260.8Q1202.8 266.4 1208.3 269.5Q1213.7 272.6 1222.4 274.9Q1231.1 277.2 1240.5 280.2Q1250.0 283.1 1258.5 288.4Q1267.0 293.6 1272.5 302.3Q1277.9 311.0 1277.9 324.7L1277.9 324.7Q1277.9 346.7 1262.1 359.5Q1246.3 372.4 1219.0 372.4Z"
