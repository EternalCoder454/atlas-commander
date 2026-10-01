package ui

import (
	"strings"
	"time"

	qt "github.com/mappu/miqt/qt6"
)

// setupBanner is the card the board shows while Claude Code can't be used:
// what is wrong, in the setup check's own sentences, and a way to fix it.
type setupBanner struct {
	app     *App
	W       *qt.QFrame
	text    *qt.QLabel
	lastQ   time.Time
	problem string
}

const setupInterval = 5 * time.Second

func newSetupBanner(a *App) *setupBanner {
	b := &setupBanner{app: a, W: qt.NewQFrame2()}
	setProp(b.W.QWidget, "card", true)
	l := qt.NewQHBoxLayout(b.W.QWidget)
	l.SetContentsMargins(16, 12, 14, 12)
	l.SetSpacing(14)
	dot := qt.NewQLabel3("●")
	setProp(dot.QWidget, "status", "warning")
	l.AddWidget3(dot.QWidget, 0, qt.AlignTop)
	col := qt.NewQVBoxLayout2()
	col.SetSpacing(2)
	col.AddWidget(qt.NewQLabel3("Claude Code isn't ready").QWidget)
	b.text = caption("")
	b.text.SetWordWrap(true)
	b.text.SetTextInteractionFlags(qt.TextSelectableByMouse)
	col.AddWidget(b.text.QWidget)
	l.AddLayout2(col.QLayout, 1)
	open := qt.NewQPushButton3("Open settings")
	open.OnClicked(func() { a.side.select_("settings") })
	l.AddWidget3(open.QWidget, 0, qt.AlignVCenter)
	b.W.SetVisible(false)
	return b
}

// refresh re-reads the setup check now and then; the check itself is cached
// by the controller.
func (b *setupBanner) refresh() {
	if time.Since(b.lastQ) < setupInterval {
		return
	}
	b.lastQ = time.Now()
	info := b.app.ctl.Setup()
	p := strings.Join(info.Problems, " ")
	if p == b.problem {
		return
	}
	b.problem = p
	b.text.SetText(p + " Agents on the Claude API backend still work.")
	b.W.SetVisible(p != "")
}
