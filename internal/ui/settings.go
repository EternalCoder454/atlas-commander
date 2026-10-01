package ui

import (
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	qt "github.com/mappu/miqt/qt6"

	"atlas-commander/internal/config"
	"atlas-commander/internal/fleet"
	"atlas-commander/internal/paths"
	"atlas-commander/internal/theme"
	"atlas-commander/internal/ui/qtx"
)

// settingsPage is Atlas Monitor's settings, which take their layout from
// Windows 11's Task Manager: every setting on one scrolling page under a few
// section headings, each setting a card of its own — an icon, a title, one
// line saying what it does, and the control at the right-hand end. Options
// that belong to another sit in the same card beneath it, lined up with its
// title, and are always shown: there is nothing to expand. Every change
// applies and saves at once; there is no Apply.
type settingsPage struct {
	app *App
	w   *qt.QWidget
	col *qt.QVBoxLayout

	themeHead  *settingRow
	system     *qt.QPushButton
	circles    []*themeCircle
	circleGrid *qt.QGridLayout
	circleBox  *qt.QWidget
	circleWide int // -1 until laid out, then 0 (stacked) or 1 (side by side)
	delTheme   *qt.QPushButton
	swatches   []*swatch
	themeAcc   rgb // the chosen theme's own accent, for the first swatch

	uiFont   *qt.QFontComboBox
	monoFont *qt.QFontComboBox

	setupState *qt.QLabel
	lastSetup  time.Time
	openaiKey  *qt.QLabel
	geminiKey  *qt.QLabel
	ollamaNow  *qt.QLabel

	// flush applies text typed but not yet confirmed; leaving the page
	// runs it, as leaving the field would have.
	flush []func()

	phone *phoneCards // nil without a phone server
}

// A card's first row has its icon settingIconStart in from the card's edge
// and its title settingIconEnd after the icon. settingIndent is where that
// title starts, for the rows and blocks beneath it that have no icon of
// their own but line up with it.
const (
	settingIconSize  = 20
	settingIconStart = 18
	settingIconEnd   = 16
	settingIndent    = settingIconStart + settingIconSize + settingIconEnd
)

// accentChoices are GNOME's accent colours, the set Monitor offers.
var accentChoices = []struct{ name, hex string }{
	{"Blue", "#3584e4"}, {"Teal", "#2190a4"}, {"Green", "#3a944a"},
	{"Yellow", "#c88800"}, {"Orange", "#ed5b00"}, {"Red", "#e62d42"},
	{"Pink", "#d56199"}, {"Purple", "#9141ac"}, {"Slate", "#6f8396"},
}

func newSettingsPage(a *App) *settingsPage {
	p := &settingsPage{app: a, w: qt.NewQWidget2(), circleWide: -1}
	outer := qt.NewQVBoxLayout(p.w)
	outer.SetContentsMargins(0, 0, 0, 0)

	scroll := qt.NewQScrollArea2()
	scroll.SetWidgetResizable(true)
	scroll.SetFrameShape(qt.QFrame__NoFrame)
	scroll.SetHorizontalScrollBarPolicy(qt.ScrollBarAlwaysOff)
	setProp(scroll.QWidget, "page", true)
	outer.AddWidget(scroll.QWidget)

	// Full width, as in Monitor: the cards run to the page's margins.
	inner := qt.NewQWidget2()
	p.col = qt.NewQVBoxLayout(inner)
	p.col.SetContentsMargins(24, 18, 24, 24)
	p.col.SetSpacing(6)
	scroll.SetWidget(inner)

	p.col.AddWidget(pageTitle("Settings").QWidget)

	p.section("Appearance")
	p.themeCard()
	p.accentCard()
	p.densityCard()
	p.transparencyCard()
	p.introCard()

	p.section("Text")
	p.textCards()

	p.section("Notifications")
	p.notifyCard()

	p.section("Providers")
	p.claudeCards()

	p.phoneSection()

	p.section("Updates")
	p.updateCards()

	p.section("About")
	p.aboutCards()
	p.col.AddStretch()

	p.w.OnHideEvent(func(super func(*qt.QHideEvent), ev *qt.QHideEvent) {
		for _, f := range p.flush {
			f()
		}
		super(ev)
	})
	a.themed = append(a.themed, p.sync)
	p.sync()
	return p
}

func (p *settingsPage) widget() *qt.QWidget        { return p.w }
func (p *settingsPage) save()                      { p.app.saveSettings() }
func (p *settingsPage) retheme()                   { p.save(); p.app.resolveTheme(); p.app.applyTheme() }
func (p *settingsPage) settings() *config.Settings { return &p.app.settings }
func (p *settingsPage) currentID() string          { return p.app.settings.Theme }

// refresh re-reads the Claude Code check now and then while the page is up:
// the answer is cached and refreshed in the background, so a fix made in a
// terminal shows here a few seconds later.
func (p *settingsPage) refresh(_ *fleet.Snapshot) {
	p.phoneRefresh()
	if time.Since(p.lastSetup) >= 3*time.Second {
		p.checkSetup()
	}
}

// sync brings every control in line with the settings, after a change from
// here or a theme switch from anywhere.
func (p *settingsPage) sync() {
	s := p.settings()
	following := s.Theme == ""
	setProp(p.system.QWidget, "chosen", following)
	switch {
	case following && s.ThemeMode == config.ModeLight:
		p.themeHead.setSub("Light, whatever the desktop uses")
	case following && s.ThemeMode == config.ModeDark:
		p.themeHead.setSub("Dark, whatever the desktop uses")
	case following:
		p.themeHead.setSub("Follows the desktop's light and dark setting")
	default:
		t := theme.Find(p.app.themes, s.Theme, p.app.systemDark())
		p.themeHead.setSub(t.Name + " — " + t.Summary)
	}
	for _, c := range p.circles {
		c.w.Update()
	}
	base := theme.Find(p.app.themes, s.Theme, p.app.systemDark()).Filled()
	p.themeAcc = parseHex(base.Colors["accent_bg_color"])
	for _, sw := range p.swatches {
		sw.w.Update()
	}
	p.delTheme.SetEnabled(!following && !theme.IsBuiltin(s.Theme))
}

// Cards.

func (p *settingsPage) section(title string) {
	p.col.AddSpacing(12)
	h := qt.NewQLabel3(title)
	setProp(h.QWidget, "heading", true)
	p.col.AddWidget(h.QWidget)
	p.col.AddSpacing(-2)
}

// settingCard is one card on the page: its rows on one surface, divided by
// hairlines.
type settingCard struct {
	page *settingsPage
	f    *qt.QFrame
	l    *qt.QVBoxLayout
	rows int
}

func (p *settingsPage) card() *settingCard {
	c := &settingCard{page: p, f: qt.NewQFrame2()}
	setProp(c.f.QWidget, "card", true)
	c.l = qt.NewQVBoxLayout(c.f.QWidget)
	c.l.SetContentsMargins(0, 0, 0, 0)
	c.l.SetSpacing(0)
	p.col.AddWidget(c.f.QWidget)
	return c
}

func (c *settingCard) divider() {
	if c.rows > 0 {
		line := qt.NewQFrame2()
		line.SetFixedHeight(1)
		setProp(line.QWidget, "hairline", true)
		c.l.AddWidget(line.QWidget)
	}
	c.rows++
}

type settingRow struct {
	w   *qt.QWidget
	sub *qt.QLabel
}

func (r *settingRow) setSub(s string) {
	if r.sub.Text() != s {
		r.sub.SetText(s)
	}
	r.sub.SetVisible(s != "")
}

// head is a card's first row: its icon, title and description, and the
// controls at the end, at the taller height Task Manager gives it.
func (c *settingCard) head(icon, title, sub string, ctl ...*qt.QWidget) *settingRow {
	return c.row(icon, title, sub, ctl...)
}

// sub is a row that belongs to the one above it: no icon, its title lined
// up with that row's.
func (c *settingCard) sub(title, sub string, ctl ...*qt.QWidget) *settingRow {
	return c.row("", title, sub, ctl...)
}

func (c *settingCard) row(icon, title, sub string, ctl ...*qt.QWidget) *settingRow {
	c.divider()
	r := &settingRow{w: qt.NewQWidget2()}
	hl := qt.NewQHBoxLayout(r.w)
	hl.SetSpacing(0)
	if icon != "" {
		hl.SetContentsMargins(settingIconStart, 10, 14, 10)
		r.w.SetMinimumHeight(62)
		hl.AddWidget3(newThemedIcon(c.page.app, icon, settingIconSize, 0.85).L.QWidget, 0, qt.AlignVCenter)
		hl.AddSpacing(settingIconEnd)
	} else {
		hl.SetContentsMargins(settingIndent, 8, 14, 8)
		r.w.SetMinimumHeight(50)
	}
	text := qt.NewQVBoxLayout2()
	text.SetSpacing(1)
	text.AddStretch()
	text.AddWidget(qt.NewQLabel3(title).QWidget)
	r.sub = caption(sub)
	r.sub.SetWordWrap(true)
	r.sub.SetVisible(sub != "")
	text.AddWidget(r.sub.QWidget)
	text.AddStretch()
	hl.AddLayout2(text.QLayout, 1)
	if len(ctl) > 0 {
		hl.AddSpacing(16)
		cl := qt.NewQHBoxLayout2()
		cl.SetSpacing(8)
		for _, w := range ctl {
			cl.AddWidget3(w, 0, qt.AlignVCenter)
		}
		hl.AddLayout(cl.QLayout)
	}
	c.l.AddWidget(r.w)
	return r
}

// block holds something that is not a row — the theme circles, the accent
// colours — beneath the card's first row, starting where its title does.
func (c *settingCard) block(w *qt.QWidget) {
	c.divider()
	box := qt.NewQWidget2()
	l := qt.NewQVBoxLayout(box)
	l.SetContentsMargins(settingIndent, 10, 14, 14)
	l.AddWidget(w)
	c.l.AddWidget(box)
}

// textField is a text setting's entry. It applies itself on Enter, when focus
// leaves it, and when the page is left; apply stores the trimmed text and
// returns what was stored, which the entry then shows.
func (p *settingsPage) textField(value, placeholder string, apply func(string) string) *qt.QLineEdit {
	e := qt.NewQLineEdit2()
	e.SetText(value)
	e.SetPlaceholderText(placeholder)
	e.SetMinimumWidth(240)
	setProp(e.QWidget, "mono", true)
	last := value
	commit := func() {
		v := strings.TrimSpace(e.Text())
		if v == last {
			return
		}
		last = apply(v)
		if e.Text() != last {
			e.SetText(last)
		}
	}
	e.OnEditingFinished(commit)
	p.flush = append(p.flush, commit)
	return e
}

// iconButton is a push button with an icon from the set before its text.
func (p *settingsPage) iconButton(icon, text string) *qt.QPushButton {
	b := qt.NewQPushButton3(text)
	if icon != "" {
		p.app.buttonIcon(b, icon)
	}
	return b
}

// Appearance.

func (p *settingsPage) themeCard() {
	c := p.card()
	p.system = qt.NewQPushButton3("Use system setting")
	setProp(p.system.QWidget, "choice", true)
	// Pressing it again while it is chosen leaves it chosen.
	p.system.OnClicked(func() {
		s := p.settings()
		if s.Theme == "" && s.ThemeMode == config.ModeSystem {
			return
		}
		s.Theme, s.ThemeMode = "", config.ModeSystem
		p.retheme()
	})
	p.themeHead = c.head("theme", "App theme", "", p.system.QWidget)

	p.circleBox = qt.NewQWidget2()
	p.circleGrid = qt.NewQGridLayout(p.circleBox)
	p.circleGrid.SetContentsMargins(0, 0, 0, 0)
	p.circleGrid.SetHorizontalSpacing(6)
	p.circleGrid.SetVerticalSpacing(10)
	p.circleBox.OnResizeEvent(func(super func(*qt.QResizeEvent), ev *qt.QResizeEvent) {
		super(ev)
		p.layoutCircles()
	})
	p.fillGallery()
	c.block(p.circleBox)

	edit := p.iconButton("edit", "Customize…")
	edit.SetToolTip("Start a new theme from the current one")
	edit.OnClicked(func() { p.editTheme() })
	p.delTheme = p.iconButton("trash", "Delete")
	p.delTheme.SetToolTip("Delete the chosen theme. Built-in themes can't be deleted.")
	p.delTheme.OnClicked(p.deleteTheme)
	c.sub("Your own themes", "Change any colour and watch the window follow. Saved in "+shortPath(paths.Themes())+".",
		p.delTheme.QWidget, edit.QWidget)
}

// fillGallery makes one circle per theme, built-in and your own.
func (p *settingsPage) fillGallery() {
	for _, c := range p.circles {
		p.circleGrid.RemoveWidget(c.w)
		c.w.Hide()
		c.w.DeleteLater()
	}
	p.circles = p.circles[:0]
	for _, t := range p.app.themes {
		p.circles = append(p.circles, newThemeCircle(p, t))
	}
	p.circleWide = -1
	p.layoutCircles()
}

// layoutCircles sets the circles in two halves, side by side where they fit
// and one above the other where they do not: a line of ten, or two of five.
// Wrapping them one by one would leave whatever did not fit alone on a line.
func (p *settingsPage) layoutCircles() {
	n := len(p.circles)
	if n == 0 {
		return
	}
	half := (n + 1) / 2
	const gap = 18
	need := 2*half*circleCellW + (2*half-2)*6 + gap
	wide := 0
	if p.circleBox.Width() >= need {
		wide = 1
	}
	if wide == p.circleWide {
		return
	}
	p.circleWide = wide
	for _, c := range p.circles {
		p.circleGrid.RemoveWidget(c.w)
	}
	for i := 0; i < p.circleGrid.ColumnCount(); i++ {
		p.circleGrid.SetColumnMinimumWidth(i, 0)
		p.circleGrid.SetColumnStretch(i, 0)
	}
	for i, c := range p.circles {
		h, j := i/half, i%half
		if wide == 1 {
			col := h*(half+1) + j // a spacer column between the halves
			p.circleGrid.AddWidget2(c.w, 0, col)
		} else {
			p.circleGrid.AddWidget2(c.w, h, j)
		}
	}
	if wide == 1 {
		p.circleGrid.SetColumnMinimumWidth(half, gap-12)
		p.circleGrid.SetColumnStretch(2*half+1, 1)
	} else {
		p.circleGrid.SetColumnStretch(half, 1)
	}
}

func (p *settingsPage) pickTheme(id string) {
	if p.app.settings.Theme == id {
		return
	}
	p.app.settings.Theme = id
	p.retheme()
}

func (p *settingsPage) pickAccent(hex string) {
	if hex == "custom" {
		start := p.app.settings.Accent
		if start == "" {
			start = p.app.theme.Colors["accent_bg_color"]
		}
		init := parseHex(start).q(1)
		c := qt.QColorDialog_GetColor3(init, p.app.win.QWidget, "Accent colour")
		init.Delete()
		if !c.IsValid() {
			return
		}
		hex = strings.ToLower(c.Name())
	}
	p.app.settings.Accent = hex
	p.retheme()
}

func (p *settingsPage) deleteTheme() {
	id := p.currentID()
	if id == "" || theme.IsBuiltin(id) {
		return
	}
	t := theme.Find(p.app.themes, id, p.app.systemDark())
	box := qt.NewQMessageBox6(qt.QMessageBox__Warning, "Delete theme?",
		fmt.Sprintf("“%s” will be removed from %s. Commander goes back to following the system.", t.Name, shortPath(paths.Themes())),
		qt.QMessageBox__Cancel, p.app.win.QWidget)
	defer box.DeleteLater()
	del := box.AddButton2("Delete", qt.QMessageBox__DestructiveRole)
	box.Exec()
	if box.ClickedButton() != del.QAbstractButton {
		return
	}
	removed, err := theme.Remove(paths.Themes(), id)
	if err != nil {
		p.app.report(fmt.Errorf("Couldn't delete the theme: %w", err))
		return
	}
	if !removed {
		p.app.report(fmt.Errorf("Couldn't find “%s” in %s to delete it", t.Name, shortPath(paths.Themes())))
		return
	}
	p.app.loadThemes()
	p.app.settings.Theme = ""
	p.fillGallery()
	p.retheme()
}

// themeCircle is one theme: a disc of its two colours, the window's and the
// accent's, split on the diagonal, with its name beneath. The chosen one has
// a ring in the current accent.
type themeCircle struct {
	page  *settingsPage
	w     *qt.QWidget
	t     theme.Theme
	hover bool
}

const (
	circleCellW = 72
	circleR     = 17
)

func newThemeCircle(p *settingsPage, t theme.Theme) *themeCircle {
	c := &themeCircle{page: p, w: qt.NewQWidget2(), t: t.Filled()}
	c.w.SetFixedSize2(circleCellW, 2*circleR+10+22)
	c.w.SetToolTip(t.Name + " — " + t.Summary)
	c.w.SetAccessibleName(t.Name)
	pointer(c.w)
	c.w.OnPaintEvent(func(super func(*qt.QPaintEvent), ev *qt.QPaintEvent) { c.paint() })
	c.w.OnEnterEvent(func(super func(*qt.QEnterEvent), ev *qt.QEnterEvent) { c.hover = true; c.w.Update() })
	c.w.OnLeaveEvent(func(super func(*qt.QEvent), ev *qt.QEvent) { c.hover = false; c.w.Update() })
	c.w.OnMousePressEvent(func(super func(*qt.QMouseEvent), ev *qt.QMouseEvent) {
		if ev.Button() == qt.LeftButton {
			p.pickTheme(c.t.ID)
		}
	})
	return c
}

func (c *themeCircle) paint() {
	pal := c.page.app.pal
	p := qt.NewQPainter2(c.w.QPaintDevice)
	defer p.Delete()
	defer p.End()
	p.SetRenderHint(qt.QPainter__Antialiasing)

	cx, cy, r := float64(circleCellW)/2, float64(circleR)+5, float64(circleR)
	primary, secondary := c.t.Primary, c.t.Secondary
	if primary == "" {
		primary = c.t.Colors["window_bg_color"]
	}
	if secondary == "" {
		secondary = c.t.Colors["accent_bg_color"]
	}
	// A hard stop on the diagonal rather than a blend: the two colours are
	// two facts about the theme, and a gradient would invent a third.
	fillEllipse(p, cx, cy, r, parseHex(primary), 1)
	half := qt.NewQPainterPath()
	half.MoveTo2(cx+r, cy-r)
	half.LineTo2(cx+r, cy+r)
	half.LineTo2(cx-r, cy+r)
	half.CloseSubpath()
	p.Save()
	p.SetClipPath(half)
	fillEllipse(p, cx, cy, r, parseHex(secondary), 1)
	p.Restore()
	half.Delete()

	// The outline: a dark theme's window half is nearly the page's colour, and
	// without one it would read as a half circle.
	ring := 0.22
	if c.hover {
		ring = 0.45
	}
	strokeEllipse(p, cx, cy, r-0.5, pal.fg, ring, 1)
	selected := c.page.app.settings.Theme == c.t.ID
	if selected {
		strokeEllipse(p, cx, cy, r+3, pal.accentB, 1, 2.5)
	}

	f := qt.NewQFont5(qt.QApplication_Font())
	f.SetPointSizeF(f.PointSizeF() * 0.88)
	if selected {
		f.SetWeight(qt.QFont__DemiBold)
	}
	p.SetFont(f)
	alpha := 0.7
	if selected {
		alpha = 1
	}
	col := pal.fg.q(alpha)
	p.SetPen(col)
	p.DrawText5(rectf(0, cy+r+6, circleCellW, 20), int(qt.AlignHCenter|qt.AlignTop), c.t.Name)
	col.Delete()
	f.Delete()
}

func (p *settingsPage) accentCard() {
	c := p.card()
	c.head("accent", "Accent colour", "Buttons, selection and the chosen row. The first keeps the theme's own.")
	sw := qt.NewQWidget2()
	sl := qt.NewQHBoxLayout(sw)
	sl.SetContentsMargins(0, 0, 0, 0)
	sl.SetSpacing(8)
	add := func(s *swatch) {
		p.swatches = append(p.swatches, s)
		sl.AddWidget(s.w)
	}
	add(newSwatch(p, "", "The theme's own accent"))
	for _, a := range accentChoices {
		add(newSwatch(p, a.hex, a.name))
	}
	add(newSwatch(p, "custom", "Pick any colour…"))
	sl.AddStretch()
	c.block(sw)
}

func (p *settingsPage) densityCard() {
	s := p.settings()
	densIdx := max(0, slices.Index(config.DensityChoices, s.Density))
	density := dropdown([]string{"Comfortable", "Compact"}, densIdx, func(i int) {
		s.Density = config.DensityChoices[i]
		p.retheme()
	})
	p.card().head("density", "Density", "How tall rows are. Compact fits more agents on the board.", density.QWidget)
}

// transparencyCard sets how much the desktop shows through the window. Where
// that cannot work the card stays, greyed, with the reason under its title.
func (p *settingsPage) transparencyCard() {
	s := p.settings()
	labels := []string{"Off", "Subtle", "Medium", "Strong"}
	idx := max(0, slices.Index(config.TransparencyChoices, s.Transparency))
	level := dropdown(labels, idx, func(i int) {
		s.Transparency = config.TransparencyChoices[i]
		p.retheme()
	})
	sub := "Lets the desktop show through the window. Text and charts stay solid."
	if ok, why := transparencyAvailable(); !ok {
		sub = why
		level.SetEnabled(false)
	}
	p.card().head("opacity", "Window transparency", sub, level.QWidget)
}

func (p *settingsPage) introCard() {
	s := p.settings()
	play := p.iconButton("startup", "Play")
	play.SetToolTip("Play the intro now")
	play.OnClicked(func() { newIntro(p.app, "Commander", nil).begin() })
	intro := newToggle(p.app, s.ShowIntro, func(on bool) {
		s.ShowIntro = on
		p.save()
	})
	sub := "Plays the Atlas logo for a moment when Commander opens. A click or any key skips it."
	if !qtx.AnimationsEnabled() {
		sub += " Your desktop has animations turned off, so it is skipped anyway."
	}
	p.card().head("startup", "Intro at startup", sub, play.QWidget, intro.w)
}

// Text.

func (p *settingsPage) textCards() {
	s := p.settings()

	p.uiFont = qt.NewQFontComboBox2()
	p.uiFont.SetFixedWidth(240)
	p.setFontBox(p.uiFont, s.UIFont, qt.QApplication_Font().Family())
	p.uiFont.OnCurrentFontChanged(func(f *qt.QFont) {
		s.UIFont = f.Family()
		p.retheme()
	})
	reset := p.iconButton("reset", "Default")
	reset.SetToolTip("Use the desktop's font")
	reset.OnClicked(func() {
		s.UIFont = ""
		p.setFontBox(p.uiFont, "", qt.QApplication_Font().Family())
		p.retheme()
	})
	p.card().head("text", "Interface font", "Everything except code, paths and figures.", reset.QWidget, p.uiFont.QWidget)

	p.monoFont = qt.NewQFontComboBox2()
	p.monoFont.SetFixedWidth(240)
	p.monoFont.SetFontFilters(qt.QFontComboBox__MonospacedFonts)
	p.setFontBox(p.monoFont, p.app.mono, p.app.mono)
	p.monoFont.OnCurrentFontChanged(func(f *qt.QFont) {
		s.MonoFont = f.Family()
		p.app.mono = f.Family()
		p.retheme()
	})
	p.card().head("code", "Code font", "Transcripts, tool calls, costs and token counts.", p.monoFont.QWidget)

	size := qt.NewQSpinBox2()
	size.SetRange(config.MinFontPt, config.MaxFontPt)
	size.SetValue(s.FontSize)
	size.SetSuffix(" pt")
	size.SetMinimumWidth(90)
	size.OnValueChanged(func(v int) {
		s.FontSize = v
		p.retheme()
	})
	p.card().head("size", "Text size", "The size everything else is measured from.", size.QWidget)
}

func (p *settingsPage) setFontBox(box *qt.QFontComboBox, family, fallback string) {
	if family == "" {
		family = fallback
	}
	f := qt.NewQFont2(family)
	box.BlockSignals(true)
	box.SetCurrentFont(f)
	box.BlockSignals(false)
	f.Delete()
}

// Notifications.

func (p *settingsPage) notifyCard() {
	s := p.settings()
	notify := newToggle(p.app, s.Notifications, func(on bool) {
		s.Notifications = on
		p.save()
	})
	sub := "When an agent needs approval, finishes, fails or reaches its cost cap, while Commander is in the background."
	if !qt.QSystemTrayIcon_IsSystemTrayAvailable() {
		sub += " Your desktop has no system tray, so there is nowhere to show them."
	}
	p.card().head("notifications", "Desktop notifications", sub, notify.w)
}

// Claude.

func (p *settingsPage) claudeCards() {
	s := p.settings()

	c := p.card()
	p.setupState = qt.NewQLabel2()
	check := p.iconButton("reset", "Check again")
	// The check runs in the background; its answer lands a moment later.
	again := qt.NewQTimer2(p.w.QObject)
	again.SetSingleShot(true)
	again.OnTimeout(p.checkSetup)
	check.OnClicked(func() {
		p.checkSetup()
		again.Start(1500)
	})
	c.head("claude", "Claude Code", "Runs agents on the Claude Code backend. Hover the status for its version and path.",
		p.setupState.QWidget, check.QWidget)
	p.checkSetup()

	var path *qt.QLineEdit
	path = p.textField(s.ClaudePath, "Found on PATH", func(v string) string {
		s.ClaudePath = v
		p.save()
		return v
	})
	browse := p.iconButton("folder", "Browse…")
	browse.OnClicked(func() {
		f := qt.QFileDialog_GetOpenFileName3(p.app.win.QWidget, "Claude Code executable", homeDir())
		if f != "" {
			path.SetText(f)
			path.EditingFinished()
		}
	})
	c.sub("Location", "Leave empty to use the claude on your PATH. Takes effect the next time Commander starts.",
		path.QWidget, browse.QWidget)

	p.openaiKey, p.geminiKey = qt.NewQLabel2(), qt.NewQLabel2()
	p.keyCard("OpenAI", "Chats with OpenAI models using a key.", p.openaiKey, s.OpenAIKeyEnv, config.Defaults().OpenAIKeyEnv,
		func(v string) { s.OpenAIKeyEnv = v })
	p.keyCard("Gemini", "Chats with Google Gemini models using a key.", p.geminiKey, s.GeminiKeyEnv, config.Defaults().GeminiKeyEnv,
		func(v string) { s.GeminiKeyEnv = v })

	p.ollamaNow = qt.NewQLabel2()
	ollamaAgain := qt.NewQTimer2(p.w.QObject)
	ollamaAgain.SetSingleShot(true)
	ollamaAgain.OnTimeout(p.checkSetup)
	ollamaCheck := p.iconButton("reset", "Check again")
	ollamaCheck.OnClicked(func() {
		p.checkSetup()
		ollamaAgain.Start(1500)
	})
	url := p.textField(s.OllamaURL, config.Defaults().OllamaURL, func(v string) string {
		v = config.NormalizeOllamaURL(v)
		s.OllamaURL = v
		p.save()
		return v
	})
	oc := p.card()
	oc.head("code", "Local (Ollama)",
		"Runs models on this machine through Ollama, for free. Start Ollama first; the address takes effect the next time Commander starts.",
		p.ollamaNow.QWidget, ollamaCheck.QWidget)
	oc.sub("Address", "Where Ollama is listening.", url.QWidget)
	p.checkSetup()
}

// keyCard is one provider card: the name of the environment variable that
// holds the key, and whether that variable is set. Commander never stores the
// key itself.
func (p *settingsPage) keyCard(title, blurb string, state *qt.QLabel, env, def string, set func(string)) {
	field := p.textField(env, def, func(v string) string {
		if v == "" {
			v = def
		}
		set(v)
		p.save()
		showKeyState(state, v)
		return v
	})
	showKeyState(state, env)
	p.card().head("key", title,
		blurb+" It reads the key from this environment variable; Commander never stores the key. Takes effect the next time Commander starts.",
		state.QWidget, field.QWidget)
}

func (p *settingsPage) checkSetup() {
	p.lastSetup = time.Now()
	info := p.app.ctl.Setup()
	var text, status, tip string
	if len(info.Problems) == 0 {
		text, status = "●  Ready", "ok"
		if info.ClaudeVersion != "" {
			tip = "Version " + info.ClaudeVersion + " at "
		}
		tip += info.ClaudePath
	} else {
		text, status = "●  Needs attention", "error"
		tip = strings.Join(info.Problems, "\n")
	}
	if p.setupState.Text() != text {
		p.setupState.SetText(text)
		setProp(p.setupState.QWidget, "status", status)
	}
	if p.setupState.ToolTip() != tip {
		p.setupState.SetToolTip(tip)
	}

	// The Local card. The probe runs in the background, so before the first
	// answer there is nothing to claim either way.
	if p.ollamaNow == nil {
		return // the Local card is built after the Claude Code one
	}
	var ollama, ollamaStatus string
	switch {
	case !info.OllamaChecked:
		ollama, ollamaStatus = "Checking…", "idle"
	case info.OllamaReachable:
		ollama, ollamaStatus = "●  Running · "+plural(info.OllamaModels, "model"), "ok"
	default:
		ollama, ollamaStatus = "●  Not running", "error"
	}
	if p.ollamaNow.Text() != ollama {
		p.ollamaNow.SetText(ollama)
		setProp(p.ollamaNow.QWidget, "status", ollamaStatus)
		p.ollamaNow.SetToolTip(info.OllamaURL)
	}
}

// showKeyState says whether the variable that should hold a key is set.
func showKeyState(l *qt.QLabel, env string) {
	if os.Getenv(env) != "" {
		l.SetText("Set")
		setProp(l.QWidget, "status", "ok")
	} else {
		l.SetText("Not set")
		setProp(l.QWidget, "status", "idle")
	}
}

// About.

func (p *settingsPage) aboutCards() {
	p.card().head("atlas", "Atlas Commander",
		"Version "+strings.TrimSpace(p.app.version)+" · MIT License. Run, watch and govern a fleet of AI agents from one window.")

	c := p.card()
	c.head("folder", "Where Commander keeps things", "Select a path to copy it.")
	for _, r := range []struct{ title, path string }{
		{"Settings", paths.Settings()},
		{"Themes", paths.Themes()},
		{"Audit log", paths.Database()},
		{"Worktrees", paths.Worktrees()},
	} {
		l := qt.NewQLabel3(shortPath(r.path))
		setProp(l.QWidget, "mono", true)
		setProp(l.QWidget, "caption", true)
		l.SetTextInteractionFlags(qt.TextSelectableByMouse)
		c.sub(r.title, "", l.QWidget)
	}
}

// Painting helpers, shared with the theme editor.

func pointer(w *qt.QWidget) {
	c := qt.NewQCursor2(qt.PointingHandCursor)
	w.SetCursor(c)
	c.Delete()
}

func fillRect(p *qt.QPainter, x, y, w, h float64, c rgb, alpha float64) {
	q := c.q(alpha)
	p.FillRect4(rectf(x, y, w, h), q)
	q.Delete()
}

func fillRound(p *qt.QPainter, x, y, w, h, r float64, c rgb, alpha float64) {
	q := c.q(alpha)
	b := qt.NewQBrush3(q)
	p.SetPenWithStyle(qt.NoPen)
	p.SetBrush(b)
	p.DrawRoundedRect(rectf(x, y, w, h), r, r)
	b.Delete()
	q.Delete()
}

func strokeRound(p *qt.QPainter, x, y, w, h, r float64, c rgb, alpha, width float64) {
	q := c.q(alpha)
	pen := qt.NewQPen3(q)
	pen.SetWidthF(width)
	p.SetPenWithPen(pen)
	p.SetBrushWithStyle(qt.NoBrush)
	p.DrawRoundedRect(rectf(x, y, w, h), r, r)
	pen.Delete()
	q.Delete()
}

func fillEllipse(p *qt.QPainter, cx, cy, r float64, c rgb, alpha float64) {
	q := c.q(alpha)
	b := qt.NewQBrush3(q)
	p.SetPenWithStyle(qt.NoPen)
	p.SetBrush(b)
	p.DrawEllipse3(ptf(cx, cy), r, r)
	b.Delete()
	q.Delete()
}

func strokeEllipse(p *qt.QPainter, cx, cy, r float64, c rgb, alpha, width float64) {
	q := c.q(alpha)
	pen := qt.NewQPen3(q)
	pen.SetWidthF(width)
	p.SetPenWithPen(pen)
	p.SetBrushWithStyle(qt.NoBrush)
	p.DrawEllipse3(ptf(cx, cy), r, r)
	pen.Delete()
	q.Delete()
}

// swatch is one accent choice: a filled circle, ringed when chosen. hex ""
// is the theme's own accent; "custom" opens a colour picker and shows the
// chosen colour once there is one.
type swatch struct {
	page  *settingsPage
	w     *qt.QWidget
	hex   string
	hover bool
}

const swatchD = 32

func newSwatch(p *settingsPage, hex, tip string) *swatch {
	s := &swatch{page: p, w: qt.NewQWidget2(), hex: hex}
	s.w.SetFixedSize2(swatchD, swatchD)
	s.w.SetToolTip(tip)
	s.w.SetAccessibleName(tip)
	pointer(s.w)
	s.w.OnPaintEvent(func(super func(*qt.QPaintEvent), ev *qt.QPaintEvent) { s.paint() })
	s.w.OnEnterEvent(func(super func(*qt.QEnterEvent), ev *qt.QEnterEvent) { s.hover = true; s.w.Update() })
	s.w.OnLeaveEvent(func(super func(*qt.QEvent), ev *qt.QEvent) { s.hover = false; s.w.Update() })
	s.w.OnMousePressEvent(func(super func(*qt.QMouseEvent), ev *qt.QMouseEvent) {
		if ev.Button() == qt.LeftButton {
			p.pickAccent(s.hex)
		}
	})
	return s
}

func (s *swatch) custom() string {
	acc := s.page.app.settings.Accent
	if acc == "" || slices.ContainsFunc(accentChoices, func(c struct{ name, hex string }) bool { return c.hex == acc }) {
		return ""
	}
	return acc
}

func (s *swatch) selected() bool {
	if s.hex == "custom" {
		return s.custom() != ""
	}
	return s.page.app.settings.Accent == s.hex
}

func (s *swatch) paint() {
	pal := s.page.app.pal
	p := qt.NewQPainter2(s.w.QPaintDevice)
	defer p.Delete()
	defer p.End()
	p.SetRenderHint(qt.QPainter__Antialiasing)
	c := swatchD / 2.0
	r := c - 5
	if s.selected() {
		strokeEllipse(p, c, c, c-1.5, pal.accentB, 1, 2.5)
	} else if s.hover {
		r += 1
	}
	switch s.hex {
	case "":
		// The theme's own accent, halved with the text colour so it reads
		// as "default" rather than as a tenth colour.
		fillEllipse(p, c, c, r, s.page.themeAcc, 1)
		half := qt.NewQPainterPath()
		half.MoveTo2(c+r, c-r)
		half.LineTo2(c+r, c+r)
		half.LineTo2(c-r, c+r)
		half.CloseSubpath()
		p.Save()
		p.SetClipPath(half)
		fillEllipse(p, c, c, r, pal.fg, 0.85)
		p.Restore()
		half.Delete()
	case "custom":
		if hex := s.custom(); hex != "" {
			fillEllipse(p, c, c, r, parseHex(hex), 1)
			return
		}
		g := qt.NewQConicalGradient3(c, c, 90)
		for i, h := range []string{"#e62d42", "#ed5b00", "#c88800", "#3a944a", "#2190a4", "#3584e4", "#9141ac", "#d56199", "#e62d42"} {
			q := parseHex(h).q(1)
			g.SetColorAt(float64(i)/8, q)
			q.Delete()
		}
		b := qt.NewQBrush10(g.QGradient)
		p.SetPenWithStyle(qt.NoPen)
		p.SetBrush(b)
		p.DrawEllipse3(ptf(c, c), r, r)
		b.Delete()
		g.Delete()
		fillEllipse(p, c, c, r-3.5, parseHex(s.page.app.theme.Colors["card_bg_color"]), 1)
		// A plus in the middle.
		fillRound(p, c-4, c-0.9, 8, 1.8, 0.9, pal.fg, 0.8)
		fillRound(p, c-0.9, c-4, 1.8, 8, 0.9, pal.fg, 0.8)
	default:
		fillEllipse(p, c, c, r, parseHex(s.hex), 1)
	}
	strokeEllipse(p, c, c, r-0.5, pal.fg, 0.15, 1)
}

// toggle is a switch: a pill track that fills with the accent when on and a
// round knob that slides across.
type toggle struct {
	w     *qt.QWidget
	on    bool
	pos   float64 // 0 off .. 1 on, animated
	timer *qt.QTimer
	app   *App
}

func newToggle(a *App, on bool, changed func(bool)) *toggle {
	t := &toggle{w: qt.NewQWidget2(), app: a, on: on, pos: map[bool]float64{true: 1}[on]}
	t.w.SetFixedSize2(46, 26)
	t.w.SetFocusPolicy(qt.StrongFocus)
	pointer(t.w)
	t.timer = qt.NewQTimer2(t.w.QObject)
	t.timer.OnTimeout(func() {
		goal := map[bool]float64{true: 1}[t.on]
		t.pos += math.Copysign(0.16, goal-t.pos)
		if math.Abs(goal-t.pos) < 0.16 {
			t.pos = goal
			t.timer.Stop()
		}
		t.w.Update()
	})
	flip := func() {
		t.on = !t.on
		if qtx.AnimationsEnabled() {
			t.timer.Start(16)
		} else {
			t.pos = map[bool]float64{true: 1}[t.on]
			t.w.Update()
		}
		changed(t.on)
	}
	t.w.OnMousePressEvent(func(super func(*qt.QMouseEvent), ev *qt.QMouseEvent) {
		if ev.Button() == qt.LeftButton {
			flip()
		}
	})
	t.w.OnKeyPressEvent(func(super func(*qt.QKeyEvent), ev *qt.QKeyEvent) {
		if ev.Key() == int(qt.Key_Space) || ev.Key() == int(qt.Key_Return) {
			flip()
			return
		}
		super(ev)
	})
	t.w.OnFocusInEvent(func(super func(*qt.QFocusEvent), ev *qt.QFocusEvent) { super(ev); t.w.Update() })
	t.w.OnFocusOutEvent(func(super func(*qt.QFocusEvent), ev *qt.QFocusEvent) { super(ev); t.w.Update() })
	t.w.OnPaintEvent(func(super func(*qt.QPaintEvent), ev *qt.QPaintEvent) { t.paint() })
	return t
}

func (t *toggle) paint() {
	pal := t.app.pal
	p := qt.NewQPainter2(t.w.QPaintDevice)
	defer p.Delete()
	defer p.End()
	p.SetRenderHint(qt.QPainter__Antialiasing)
	w, h := float64(t.w.Width()), float64(t.w.Height())
	off, on := pal.fg.mix(pal.page, 0.78), pal.accentB
	track := off.mix(on, t.pos)
	fillRound(p, 1, 1, w-2, h-2, (h-2)/2, track, 1)
	if t.w.HasFocus() {
		strokeRound(p, 0.5, 0.5, w-1, h-1, (h-1)/2, pal.accentB, 0.6, 1)
	}
	r := (h - 8) / 2
	cx := 4 + r + (w-8-2*r)*t.pos
	fillEllipse(p, cx, h/2+0.6, r+0.6, rgb{}, 0.18) // shadow
	fillEllipse(p, cx, h/2, r, rgb{255, 255, 255}, 1)
}
