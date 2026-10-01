package ui

import (
	"math"
	"time"

	qt "github.com/mappu/miqt/qt6"

	"atlas-commander/internal/ui/qtx"
)

// Animation helpers. Qt style sheets cannot transition anything, so hover,
// selection and page changes are animated by hand. Everything here runs on
// the Qt main thread (QVariantAnimation and QTimer deliver on it), so it may
// touch widgets; goroutines still never call into it. Every animation asks
// qtx.AnimationsEnabled when it starts, so a change in the desktop's setting
// applies without a restart.

// Durations are short so the interface never feels like it is waiting.
const (
	pageFadeMs   = 140
	navSlideMs   = 200
	hoverInMs    = 120
	hoverOutMs   = 160
	animFrameMs  = 16 // about 60 fps, only while something is moving
	pageFadeFrom = 0.0
)

// easeOutCubic maps progress 0..1 to eased progress: fast first, then settling.
// It matches Qt's OutCubic curve so Go-timed and Qt-timed animations agree.
func easeOutCubic(t float64) float64 {
	t = clamp01(t)
	u := 1 - t
	return 1 - u*u*u
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

// ramp is a value moving from one number to another over a fixed time with an
// OutCubic curve. The zero value is a constant 0. It is plain data so the
// owner can keep many of them and drive them from one timer.
type ramp struct {
	from, to float64
	start    time.Time
	dur      time.Duration
}

// at is the value at time now.
func (r ramp) at(now time.Time) float64 {
	if r.dur <= 0 {
		return r.to
	}
	return lerp(r.from, r.to, easeOutCubic(float64(now.Sub(r.start))/float64(r.dur)))
}

// done reports whether the ramp has reached its target.
func (r ramp) done(now time.Time) bool { return r.dur <= 0 || now.Sub(r.start) >= r.dur }

// retarget starts a new ramp from wherever this one is now, so reversing
// mid-way continues smoothly instead of jumping. With animations off it
// returns a finished ramp at the target.
func (r ramp) retarget(now time.Time, to float64, ms int) ramp {
	if ms <= 0 || !qtx.AnimationsEnabled() {
		return ramp{from: to, to: to}
	}
	return ramp{from: r.at(now), to: to, start: now, dur: time.Duration(ms) * time.Millisecond}
}

// anim is a running Qt animation that can be stopped. A nil *anim is a
// finished one, so callers need no checks.
type anim struct {
	q    *qt.QVariantAnimation
	dead bool
}

// Stop ends the animation without calling done. The final step is not run;
// the caller resets whatever it was driving.
func (a *anim) Stop() {
	if a == nil || a.dead {
		return
	}
	a.dead = true
	a.q.Stop()
	a.q.DeleteLater()
}

// animate runs step with values easing from from to to over ms milliseconds,
// then calls done (which may be nil). It never blocks. When animations are
// off it calls step(to) and done at once and returns nil. owner parents the
// Qt object, so it dies with the widget.
func animate(owner *qt.QObject, ms int, from, to float64, step func(v float64), done func()) *anim {
	if ms <= 0 || !qtx.AnimationsEnabled() {
		step(to)
		if done != nil {
			done()
		}
		return nil
	}
	a := &anim{q: qt.NewQVariantAnimation2(owner)}
	ec := qt.NewQEasingCurve3(qt.QEasingCurve__OutCubic)
	a.q.SetEasingCurve(ec)
	ec.Delete()
	sv, ev := qt.NewQVariant9(from), qt.NewQVariant9(to)
	a.q.SetStartValue(sv)
	a.q.SetEndValue(ev)
	sv.Delete()
	ev.Delete()
	a.q.SetDuration(ms)
	a.q.OnValueChanged(func(v *qt.QVariant) {
		if !a.dead {
			step(v.ToDouble())
		}
	})
	a.q.OnFinished(func() {
		if a.dead {
			return
		}
		a.dead = true
		step(to)
		if done != nil {
			done()
		}
		a.q.DeleteLater()
	})
	a.q.Start()
	return a
}

// pageFader fades the page that was just shown. There is one window, so one
// fader serves the whole process.
type pageFader struct {
	cur *anim
	w   *qt.QWidget
}

var fader pageFader

// reset stops any running fade and takes the effect off its page, so a quick
// second switch cannot leave the first page see-through. Setting a nil effect
// makes Qt delete the old one.
func (f *pageFader) reset() {
	f.cur.Stop()
	f.cur = nil
	if f.w != nil {
		f.w.SetGraphicsEffect(nil)
		f.w = nil
	}
}

// fadeIn fades w from transparent to opaque. The effect is removed at the end
// because an effect forces offscreen rendering, which is wasted work on a
// table or scroll area that is only sitting there.
func (f *pageFader) fadeIn(w *qt.QWidget) {
	f.reset()
	if !qtx.AnimationsEnabled() {
		return
	}
	eff := qt.NewQGraphicsOpacityEffect2(w.QObject)
	eff.SetOpacity(pageFadeFrom)
	w.SetGraphicsEffect(eff.QGraphicsEffect)
	f.w = w
	f.cur = animate(w.QObject, pageFadeMs, pageFadeFrom, 1, eff.SetOpacity, func() {
		f.cur = nil
		f.w = nil
		w.SetGraphicsEffect(nil)
	})
}

// switchTo makes w, the page for id, the visible one, fading it in when the
// window is already on screen and showing a different page. The very first
// show (startup, window not yet visible) and a repeat of the current page
// skip the fade.
func (a *App) switchTo(id string, w *qt.QWidget) {
	fade := a.current != "" && a.current != id && a.win.IsVisible()
	a.current = id
	if fade {
		fader.fadeIn(w)
	} else {
		fader.reset()
	}
	a.stack.SetCurrentWidget(w)
}
