package ui

import (
	"time"

	qt "github.com/mappu/miqt/qt6"

	"atlas-commander/internal/fleet"
	"atlas-commander/internal/ui/qtx"
)

// The status dot of a running agent breathes: a ring grows out of the dot and
// fades. An agent waiting on an approval pulses a little harder, because that
// is the one state that needs a person. pulseAt is plain arithmetic on the
// clock so the card view can draw the same halo in step with the table.

const (
	pulseDotR    = 6.0 // the status dot is 12 px across
	pulsePeriod  = 1800 * time.Millisecond
	pulseFrameMs = 33  // about 30 fps while a halo is on screen
	pulseGateMs  = 250 // how often to check whether any halo should be on screen
)

// pulseAt is the halo around a status dot at time now: the ring's radius in
// pixels and its opacity, 0 to 1. A status that does not pulse gets alpha 0.
// The ring starts at the dot's edge, grows with an OutCubic ease and fades as
// it goes, so it reads as a breath out and the dot is quiet for a moment
// before the next one.
func pulseAt(now time.Time, st fleet.Status) (radius, alpha float64) {
	var reach, peak float64
	period := pulsePeriod
	switch st {
	case fleet.StatusRunning:
		reach, peak = 5, 0.38
	case fleet.StatusApproval:
		reach, peak = 7, 0.62
		period = pulsePeriod * 2 / 3 // quicker, to draw the eye
	default:
		return 0, 0
	}
	phase := float64(now.UnixNano()%int64(period)) / float64(period)
	radius = pulseDotR + reach*easeOutCubic(phase)
	fade := 1 - phase
	return radius, peak * fade * fade
}

// pulses reports whether this status has a halo.
func pulses(st fleet.Status) bool {
	return st == fleet.StatusRunning || st == fleet.StatusApproval
}

// pulseState is a board's pulse timer and whether halos are drawn now. It is
// kept beside the page rather than in it so the page struct stays as it is.
type pulseState struct {
	timer *qt.QTimer
	on    bool // halos are being drawn; set by the timer, read by paintCell
	fast  bool // the timer is at frame rate rather than at the slow check
}

var pulseStates = map[*boardPage]*pulseState{}

// startPulse arms the board's pulse timer. The timer idles at 4 Hz doing a
// few comparisons and speeds up to frame rate only while the board is the
// page on screen, the window is shown and not minimised, animations are on
// and at least one visible row is running or waiting for approval.
func (b *boardPage) startPulse() {
	ps := &pulseState{timer: qt.NewQTimer2(b.w.QObject)}
	pulseStates[b] = ps
	ps.timer.OnTimeout(func() { b.pulseTick(ps) })
	ps.timer.Start(pulseGateMs)
}

// pulseTick runs on the main thread. It decides whether halos should show and
// repaints only the status column while they do.
func (b *boardPage) pulseTick(ps *pulseState) {
	want := b.pulseWanted()
	if want != ps.on {
		ps.on = want
		b.updateStatusColumn() // draw the first halo, or erase the last
	}
	if want {
		b.updateStatusColumn()
	}
	if want != ps.fast {
		ps.fast = want
		if want {
			ps.timer.Start(pulseFrameMs)
		} else {
			ps.timer.Start(pulseGateMs)
		}
	}
}

// pulseWanted is true when a halo would be seen.
func (b *boardPage) pulseWanted() bool {
	if !qtx.AnimationsEnabled() || !b.table.IsVisible() {
		return false
	}
	if win := b.w.Window(); win == nil || win.IsMinimized() {
		return false
	}
	vh := b.table.Viewport().Height()
	for r := range b.rows {
		if !pulses(b.rows[r].Status) {
			continue
		}
		y := b.table.RowViewportPosition(r)
		if y+b.table.RowHeight(r) > 0 && y < vh {
			return true
		}
	}
	return false
}

// updateStatusColumn repaints the status column's strip of the table and
// nothing else.
func (b *boardPage) updateStatusColumn() {
	vp := b.table.Viewport()
	vp.Update2(b.table.ColumnViewportPosition(colStatus), 0, b.table.ColumnWidth(colStatus), vp.Height())
}

// paintHalo draws the ring for one status dot centred on (cx, cy), clipped to
// the cell so a tall ring never spills onto the next row.
func (b *boardPage) paintHalo(p *qt.QPainter, st fleet.Status, c rgb, cx, cy float64, clip [4]int) {
	ps := pulseStates[b]
	if ps == nil || !ps.on || !pulses(st) {
		return
	}
	radius, alpha := pulseAt(time.Now(), st)
	if alpha <= 0.005 {
		return
	}
	p.SetClipRect2(clip[0], clip[1], clip[2], clip[3])
	line := c.q(alpha)
	defer line.Delete()
	fill := c.q(alpha * 0.3)
	defer fill.Delete()
	fb := qt.NewQBrush3(fill)
	defer fb.Delete()
	lb := qt.NewQBrush3(line)
	defer lb.Delete()
	pen := qt.NewQPen4(lb, 1.5)
	defer pen.Delete()
	p.SetPenWithPen(pen)
	p.SetBrush(fb)
	p.DrawEllipse(rectf(cx-radius, cy-radius, 2*radius, 2*radius))
	p.SetClipping(false)
}
