package ui

import (
	"fmt"

	qt "github.com/mappu/miqt/qt6"

	"atlas-commander/internal/fleet"
)

// providerReady says whether step one of the guide is done. It is the only
// place that reads SetupInfo for that, so the test changes in one spot when
// more providers arrive.
func providerReady(info fleet.SetupInfo) bool {
	return info.ClaudePath != "" && len(info.Problems) == 0
}

// guideStep is one numbered row of the Get started card.
type guideStep struct {
	status *qt.QLabel
	sub    *qt.QLabel
	btn    *qt.QPushButton
}

// startGuide is what the Board shows before the first agent exists: three
// steps with their state and one button each. It only reads state handed to
// refresh on the main-thread tick; the buttons open the usual dialogs.
type startGuide struct {
	W     *qt.QWidget
	app   *App
	steps [3]guideStep
}

func newStartGuide(a *App) *startGuide {
	g := &startGuide{app: a, W: qt.NewQWidget2()}
	outer := qt.NewQVBoxLayout(g.W)
	outer.AddStretch()

	card := qt.NewQFrame2()
	setProp(card.QWidget, "card", true)
	card.SetMaximumWidth(680)
	cl := qt.NewQVBoxLayout(card.QWidget)
	cl.SetContentsMargins(20, 18, 20, 8)
	cl.SetSpacing(0)
	h := qt.NewQLabel3("Get started")
	setProp(h.QWidget, "heading", true)
	cl.AddWidget(h.QWidget)
	intro := caption("Three steps and your first agent is working. This guide goes away once you have one.")
	intro.SetWordWrap(true)
	cl.AddWidget(intro.QWidget)
	cl.AddSpacing(10)

	defs := []struct{ title, sub, btn string }{
		{"Check a provider", "Commander runs agents through Claude Code, so it needs to be installed and signed in.", "Open Settings"},
		{"Create a fleet", "A fleet is a project folder your agents work in, with an optional budget.", "New fleet"},
		{"Add an agent and give it a job", "An agent has a name, a model and instructions. Start it with a prompt from the Board.", "New agent"},
	}
	for i, d := range defs {
		line := qt.NewQFrame2()
		line.SetFixedHeight(1)
		setProp(line.QWidget, "hairline", true)
		cl.AddWidget(line.QWidget)

		row := qt.NewQHBoxLayout2()
		row.SetContentsMargins(0, 12, 0, 12)
		row.SetSpacing(14)
		num := qt.NewQLabel3(fmt.Sprint(i + 1))
		setProp(num.QWidget, "heading", true)
		num.SetFixedWidth(22)
		row.AddWidget3(num.QWidget, 0, qt.AlignTop)
		col := qt.NewQVBoxLayout2()
		col.SetSpacing(2)
		col.AddWidget(qt.NewQLabel3(d.title).QWidget)
		st := &g.steps[i]
		st.sub = caption(d.sub)
		st.sub.SetWordWrap(true)
		col.AddWidget(st.sub.QWidget)
		row.AddLayout2(col.QLayout, 1)
		st.status = qt.NewQLabel3("")
		st.status.SetFixedWidth(64)
		row.AddWidget3(st.status.QWidget, 0, qt.AlignVCenter)
		st.btn = qt.NewQPushButton3(d.btn)
		st.btn.SetMinimumWidth(136)
		row.AddWidget3(st.btn.QWidget, 0, qt.AlignVCenter)
		cl.AddLayout(row.QLayout)
	}
	g.steps[0].btn.OnClicked(func() { a.side.select_("settings") })
	// editFleet only needs the app when creating, so a bare page value runs
	// the Fleets page's own dialog instead of a second copy of it.
	g.steps[1].btn.OnClicked(func() { (&fleetsPage{app: a}).editFleet("") })
	g.steps[2].btn.OnClicked(func() { a.editAgent("", "") })

	mid := qt.NewQHBoxLayout2()
	mid.AddStretch()
	mid.AddWidget2(card.QWidget, 1)
	mid.AddStretch()
	outer.AddLayout(mid.QLayout)
	outer.AddStretch()
	outer.AddStretch()
	for i := range g.steps {
		g.set(i, false)
	}
	return g
}

func (g *startGuide) set(i int, done bool) {
	st := &g.steps[i]
	text, state := "To do", "idle"
	if done {
		text, state = "✓ Done", "ok"
	}
	if st.status.Text() != text {
		st.status.SetText(text)
		setProp(st.status.QWidget, "status", state)
	}
}

// refresh updates the step states from the snapshot and the cached setup
// check. Buttons stay usable when a step is done, so a fleet can be added
// later. Step three is never "done": the guide is gone once an agent exists.
func (g *startGuide) refresh(s *fleet.Snapshot) {
	g.set(0, providerReady(g.app.ctl.Setup()))
	hasFleet := len(s.Fleets) > 0
	g.set(1, hasFleet)
	g.set(2, false)
	g.steps[2].btn.SetEnabled(hasFleet)
	sub := "An agent has a name, a model and instructions. Start it with a prompt from the Board."
	if !hasFleet {
		sub = "Needs a fleet first: every agent belongs to one."
	}
	if g.steps[2].sub.Text() != sub {
		g.steps[2].sub.SetText(sub)
	}
}
