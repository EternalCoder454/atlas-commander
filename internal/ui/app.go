// Package ui is Atlas Commander's Qt 6 Widgets interface, through MIQT.
//
// Threading follows Atlas Monitor's pull model: the supervisor's goroutines
// never touch Qt. They publish immutable snapshots, and one main-thread timer
// (250 ms, so about four repaints a second at most) takes the latest and
// updates whatever is visible. Widgets only change when their value does, so
// an idle board costs next to nothing.
package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	qt "github.com/mappu/miqt/qt6"

	"atlas-commander/internal/config"
	"atlas-commander/internal/fleet"
	"atlas-commander/internal/paths"
	"atlas-commander/internal/theme"
	"atlas-commander/internal/ui/qtx"
)

const tickInterval = 250 * time.Millisecond

// page is one view in the stack. refresh is called on every tick while the
// page is visible (and once when it becomes visible); it must be cheap when
// nothing changed.
type page interface {
	widget() *qt.QWidget
	refresh(s *fleet.Snapshot)
}

// App owns the window and the theme. There is one per process.
type App struct {
	qapp    *qt.QApplication
	win     *qt.QMainWindow
	ctl     Controller
	version string

	settings     config.Settings
	settingsPath string

	themes  []theme.Theme
	theme   theme.Theme
	pal     *palette
	metrics theme.Metrics
	qss     string
	scheme  qtx.Scheme
	mono    string // the monospace family actually available

	side    *sidebar
	sheet   *qt.QWidget
	stack   *qt.QStackedWidget
	pages   map[string]page
	current string

	header struct {
		spend   *liveLabel
		killAll *qt.QPushButton
	}

	glass theme.Glass // opacities in force; solid unless transparency is on and available
	frame *qt.QWidget // the window's surface under the header, sidebar and sheet

	snap  *fleet.Snapshot
	timer *qt.QTimer

	tray       *qt.QSystemTrayIcon
	trayIcon   *qt.QIcon // the mark, rendered once for notifications
	previewing bool      // the theme editor is open: leave its preview alone
	saveTimer  *qt.QTimer
	saveDirty  bool
	lastNotice fleet.Notice

	// themed are called after every theme change, for widgets that cache
	// colours or fonts.
	themed []func()
}

// Options are what main hands the UI.
type Options struct {
	Controller Controller
	Version    string
	Settings   config.Settings
	// SettingsPath is where changes are saved; "" uses paths.Settings().
	SettingsPath string
}

// New creates the QApplication and the window but does not show it. It must
// be called on the main goroutine before anything else touches Qt.
func New(o Options) *App {
	a := &App{
		ctl:          o.Controller,
		version:      o.Version,
		settings:     o.Settings,
		settingsPath: o.SettingsPath,
		pages:        map[string]page{},
	}
	if a.settingsPath == "" {
		a.settingsPath = paths.Settings()
	}

	qt.QCoreApplication_SetOrganizationName("Atlas")
	qt.QCoreApplication_SetApplicationName("Atlas Commander")
	qt.QGuiApplication_SetDesktopFileName("com.atlas.Commander")
	a.qapp = qt.NewQApplication(os.Args)
	qt.QApplication_SetStyleWithStyle("Fusion")

	a.mono = loadFonts(a.settings.MonoFont)
	theme.IndicatorDir = filepath.Join(paths.Runtime(), "style")
	a.loadThemes()
	a.scheme = qtx.SystemScheme()
	a.resolveTheme()

	a.build()
	a.applyTheme()
	a.buildTray()

	a.timer = qt.NewQTimer()
	a.timer.OnTimeout(a.tick)
	return a
}

// Run shows the window and runs the event loop until it closes.
func (a *App) Run() int {
	// The intro covers the window and fades into it. Not when ATLAS_VIEW
	// picks the page, which is a development capture.
	var in *intro
	if a.settings.ShowIntro && qtx.AnimationsEnabled() && os.Getenv("ATLAS_VIEW") == "" {
		in = newIntro(a, "Commander", nil)
	}
	a.win.Show()
	if in != nil {
		in.begin()
	}
	a.tick()
	a.timer.Start(int(tickInterval / time.Millisecond))
	code := qt.QApplication_Exec()
	if a.saveDirty {
		a.saveSettingsNow()
	}
	return code
}

// Raise brings the window forward; the single-instance check calls it when
// a second launch asks this one to show itself. Safe from any goroutine.
// Quit ends Run at once, with no questions asked, for a signal. It is safe
// to call from any goroutine (a signal handler, say).
func (a *App) Quit() {
	post(func() { qt.QCoreApplication_Quit() })
}

func (a *App) Raise() {
	post(func() {
		if a.win.IsMinimized() {
			a.win.ShowNormal()
		}
		a.win.Show()
		a.win.Raise()
		a.win.ActivateWindow()
	})
}

func (a *App) loadThemes() {
	themes, errs := theme.LoadAll(paths.Themes())
	for _, err := range errs {
		fmt.Fprintln(os.Stderr, "atlas-commander: theme:", err)
	}
	a.themes = themes
}

// systemDark is whether the light/dark choice comes out dark: the explicit
// mode if set, otherwise the desktop's colour scheme.
func (a *App) systemDark() bool {
	switch a.settings.ThemeMode {
	case config.ModeDark:
		return true
	case config.ModeLight:
		return false
	}
	return a.scheme == qtx.SchemeDark
}

func (a *App) resolveTheme() {
	t := theme.Find(a.themes, a.settings.Theme, a.systemDark())
	a.theme = t.WithOverrides(a.settings.Accent, theme.Density(a.settings.Density))
	a.metrics = a.theme.Metrics()
}

// applyTheme pushes the resolved theme into Qt: the palette for what Fusion
// draws itself, the generated style sheet for the stock widgets, and the
// parsed tokens for everything Commander paints by hand.
func (a *App) applyTheme() {
	pal := qt.NewQPalette()
	defer pal.Delete()
	for role, hex := range theme.Palette(a.theme) {
		r, ok := paletteRoles[role]
		if !ok {
			continue
		}
		c := parseHex(hex).q(1)
		pal.SetColor2(r, c)
		c.Delete()
	}
	qt.QApplication_SetPalette(pal)

	if err := theme.WriteIndicators(a.theme.Colors["accent_fg_color"]); err != nil {
		fmt.Fprintln(os.Stderr, "atlas-commander: style:", err)
	}
	a.glass = a.effectiveGlass()
	qss := theme.QSS(a.theme, a.metrics, a.settings.UIFont, a.mono, a.settings.FontSize, a.glass)
	if qss != a.qss {
		a.qss = qss
		a.qapp.SetStyleSheet(qss)
	}
	a.pal = newPalette(tokensFromTheme(a.theme))
	for _, f := range a.themed {
		f()
	}
	if a.win != nil {
		a.win.Update()
		if a.frame != nil {
			a.frame.Update() // the frame and sheet paint their own alpha
		}
	}
}

// previewTheme applies t without saving it, for the theme editor. The
// accent override is left out so the editor shows the theme's own accent.
// Calling applyTheme afterwards restores the saved choice.
func (a *App) previewTheme(t theme.Theme) {
	saved := a.theme
	a.theme = t.WithOverrides("", theme.Density(a.settings.Density))
	a.applyTheme()
	a.theme = saved
}

var paletteRoles = map[string]qt.QPalette__ColorRole{
	"Window":          qt.QPalette__Window,
	"WindowText":      qt.QPalette__WindowText,
	"Base":            qt.QPalette__Base,
	"AlternateBase":   qt.QPalette__AlternateBase,
	"Text":            qt.QPalette__Text,
	"Button":          qt.QPalette__Button,
	"ButtonText":      qt.QPalette__ButtonText,
	"Highlight":       qt.QPalette__Highlight,
	"HighlightedText": qt.QPalette__HighlightedText,
	"ToolTipBase":     qt.QPalette__ToolTipBase,
	"ToolTipText":     qt.QPalette__ToolTipText,
	"PlaceholderText": qt.QPalette__PlaceholderText,
	"Link":            qt.QPalette__Link,
	"Mid":             qt.QPalette__Mid,
	"Dark":            qt.QPalette__Dark,
	"Light":           qt.QPalette__Light,
}

// tokensFromTheme picks the colours the painters need out of a filled theme.
func tokensFromTheme(t theme.Theme) Tokens {
	t = t.Filled()
	c := t.Colors
	return Tokens{
		Dark:    t.Dark,
		Frame:   c["sidebar_bg_color"],
		Page:    c["window_bg_color"],
		View:    c["view_bg_color"],
		Card:    c["card_bg_color"],
		Fg:      c["window_fg_color"],
		Accent:  c["accent_color"],
		AccentB: c["accent_bg_color"],
		AccentF: c["accent_fg_color"],
		OK:      c["status_ok"],
		Warn:    c["status_warn"],
		Error:   c["status_error"],
		Idle:    c["status_idle"],
		Charts:  [5]string{c["chart_1"], c["chart_2"], c["chart_3"], c["chart_4"], c["chart_5"]},
	}
}

// build makes the window: a header strip and the sidebar on the frame
// colour, and the page sheet holding the stacked views.
func (a *App) build() {
	a.win = qt.NewQMainWindow2()
	a.win.SetWindowTitle("Atlas Commander")
	a.win.SetMinimumSize2(config.MinWindowWidth, config.MinWindowHeight)
	winIcon := markIcon()
	qt.QGuiApplication_SetWindowIcon(winIcon)
	winIcon.Delete()
	a.win.Resize(a.settings.WindowWidth, a.settings.WindowHeight)

	// The window is always made translucent-capable on Linux, so changing the
	// level later needs no re-creation; with transparency off the frame paints
	// itself opaque and the window looks as it always did. The attribute must
	// be set before the window is first shown.
	if transparencyPlatform() {
		a.win.SetAttribute2(qt.WA_TranslucentBackground, true)
	}

	frame := qt.NewQWidget2()
	a.frame = frame
	setName(frame, "frame")
	frame.OnPaintEvent(func(super func(*qt.QPaintEvent), ev *qt.QPaintEvent) {
		painter := qt.NewQPainter2(frame.QPaintDevice)
		c := a.pal.frame.q(a.glass.Frame)
		painter.FillRect6(frame.Rect(), c)
		c.Delete()
		painter.End()
		painter.Delete()
	})
	outer := qt.NewQVBoxLayout(frame)
	outer.SetContentsMargins(0, 0, 0, 0)
	outer.SetSpacing(0)
	outer.AddWidget(a.buildHeader())

	body := qt.NewQHBoxLayout2()
	body.SetContentsMargins(0, 0, 0, 0)
	body.SetSpacing(0)

	a.side = newSidebar(a, a.navItems(), []navItem{{id: "settings", title: "Settings"}})
	a.side.onSelect = a.show
	body.AddWidget(a.side.W)

	a.sheet = qt.NewQWidget2()
	setName(a.sheet, "sheet")
	a.sheet.OnPaintEvent(func(super func(*qt.QPaintEvent), ev *qt.QPaintEvent) { a.paintPage(a.sheet) })
	sl := qt.NewQVBoxLayout(a.sheet)
	sl.SetContentsMargins(1, 1, 0, 0) // inside the hairline
	a.stack = qt.NewQStackedWidget2()
	sl.AddWidget(a.stack.QWidget)
	body.AddWidget(a.sheet)
	outer.AddLayout(body.QLayout)

	a.win.SetCentralWidget(frame)

	a.addPage("board", newBoardPage(a))

	start := a.settings.ActiveView
	if v := os.Getenv("ATLAS_VIEW"); v != "" {
		start = v
	}
	if _, ok := pageMakers[start]; !ok {
		start = "board"
	}
	a.side.active = ""
	a.side.select_(start)

	a.win.OnCloseEvent(func(super func(*qt.QCloseEvent), ev *qt.QCloseEvent) {
		a.saveWindow()
		super(ev)
	})
}

func (a *App) buildHeader() *qt.QWidget {
	h := qt.NewQWidget2()
	setName(h, "header")
	h.SetFixedHeight(47)
	l := qt.NewQHBoxLayout(h)
	l.SetContentsMargins(10, 0, 10, 0)
	l.SetSpacing(8)

	l.AddSpacing(12)
	l.AddWidget(newMarkWidget(20))
	brand := qt.NewQLabel3("Atlas Commander")
	setProp(brand.QWidget, "brand", true)
	l.AddWidget(brand.QWidget)
	ver := qt.NewQLabel3("v" + strings.TrimSuffix(a.version, "\n"))
	ver.SetProperty("caption", qt.NewQVariant8(true))
	l.AddWidget(ver.QWidget)
	l.AddStretch()

	a.header.spend = newLiveLabel("")
	a.header.spend.L.SetProperty("mono", qt.NewQVariant8(true))
	a.header.spend.L.SetProperty("caption", qt.NewQVariant8(true))
	a.header.spend.L.SetToolTip("Spent by all fleets since Commander started")
	l.AddWidget(a.header.spend.L.QWidget)

	a.header.killAll = qt.NewQPushButton3("Kill all")
	a.header.killAll.SetProperty("danger", qt.NewQVariant8(true))
	a.header.killAll.SetToolTip("Stop every running agent at once")
	a.header.killAll.OnClicked(a.confirmKillAll)
	l.AddWidget(a.header.killAll.QWidget)
	return h
}

func (a *App) navItems() []navItem {
	count := func(f func(*fleet.Snapshot) int) func() string {
		return func() string {
			if a.snap == nil {
				return ""
			}
			if n := f(a.snap); n > 0 {
				return fmt.Sprint(n)
			}
			return ""
		}
	}
	return []navItem{
		{id: "g-fleet", title: "Fleet", group: true},
		{id: "board", title: "Board", badge: count(func(s *fleet.Snapshot) int {
			n := 0
			for _, ag := range s.Agents {
				if ag.Status.Live() {
					n++
				}
			}
			return n
		})},
		{id: "approvals", title: "Approvals", badge: count(func(s *fleet.Snapshot) int { return len(s.Approvals) })},
		{id: "tasks", title: "Tasks", badge: count(func(s *fleet.Snapshot) int {
			n := 0
			for _, t := range s.Tasks {
				if t.Ready {
					n++
				}
			}
			return n
		})},
		{id: "fleets", title: "Fleets", badge: count(func(s *fleet.Snapshot) int { return len(s.Fleets) })},
		{id: "g-insight", title: "Insight", group: true},
		{id: "analytics", title: "Analytics"},
		{id: "audit", title: "Audit log"},
		{id: "observed", title: "Observed"},
	}
}

func (a *App) addPage(id string, p page) {
	a.pages[id] = p
	a.stack.AddWidget(p.widget())
}

// pageMakers build the sidebar's views the first time they are shown.
var pageMakers = map[string]func(*App) page{
	"board":     func(a *App) page { return a.pages["board"] },
	"approvals": func(a *App) page { return newApprovalsPage(a) },
	"tasks":     func(a *App) page { return newTasksPage(a) },
	"analytics": func(a *App) page { return newAnalyticsPage(a) },
	"audit":     func(a *App) page { return newAuditPage(a) },
	"observed":  func(a *App) page { return newObservedPage(a) },
	"fleets":    func(a *App) page { return newFleetsPage(a) },
	"settings":  func(a *App) page { return newSettingsPage(a) },
}

// show switches the visible view, building it on first use.
func (a *App) show(id string) {
	p, ok := a.pages[id]
	if !ok {
		if mk, known := pageMakers[id]; known {
			p = mk(a)
		} else {
			p = newPlaceholderPage(id)
		}
		a.addPage(id, p)
	}
	a.current = id
	a.stack.SetCurrentWidget(p.widget())
	if a.snap != nil {
		p.refresh(a.snap)
	}
	if a.settings.ActiveView != id {
		a.settings.ActiveView = id
		a.saveSettings()
	}
}

// tick is the one place the UI pulls state.
func (a *App) tick() {
	if s := qtx.SystemScheme(); s != a.scheme && !a.previewing {
		a.scheme = s
		if a.settings.ThemeMode == config.ModeSystem {
			a.resolveTheme()
			a.applyTheme()
		}
	}
	snap := a.ctl.Snapshot()
	if snap == nil {
		return
	}
	changed := a.snap == nil || snap.Seq != a.snap.Seq
	a.snap = snap
	if p, ok := a.pages[a.current]; ok {
		p.refresh(snap)
	}
	if changed {
		a.side.W.Update()
		a.header.spend.Set(fmtUSD(snap.Spent) + " spent")
		live := 0
		for _, ag := range snap.Agents {
			if ag.Status.Live() {
				live++
			}
		}
		a.header.killAll.SetEnabled(live > 0)
	}
}

func (a *App) confirmKillAll() {
	live := 0
	if a.snap != nil {
		for _, ag := range a.snap.Agents {
			if ag.Status.Live() {
				live++
			}
		}
	}
	if live == 0 {
		return
	}
	box := qt.NewQMessageBox6(qt.QMessageBox__Warning, "Kill all agents?",
		fmt.Sprintf("This force-stops %s now. Work in progress is lost; worktrees are kept.", plural(live, "running agent")),
		qt.QMessageBox__Cancel, a.win.QWidget)
	kill := box.AddButton2("Kill all", qt.QMessageBox__DestructiveRole)
	box.SetDefaultButtonWithButton(qt.QMessageBox__Cancel)
	box.Exec()
	if box.ClickedButton() == kill.QAbstractButton {
		a.ctl.KillAll()
	}
	box.DeleteLater()
}

func (a *App) saveWindow() {
	if !a.win.IsMaximized() {
		a.settings.WindowWidth = a.win.Width()
		a.settings.WindowHeight = a.win.Height()
	}
	a.saveSettingsNow()
}

// saveSettings writes the settings a moment after the last change: a drag
// of the text size or a run of page switches is one write, not one each,
// and each write syncs the file to disk on the Qt thread.
func (a *App) saveSettings() {
	if a.saveTimer == nil {
		a.saveTimer = qt.NewQTimer2(a.win.QObject)
		a.saveTimer.SetSingleShot(true)
		a.saveTimer.OnTimeout(a.saveSettingsNow)
	}
	a.saveDirty = true
	a.saveTimer.Start(400)
}

func (a *App) saveSettingsNow() {
	if a.saveTimer != nil {
		a.saveTimer.Stop()
	}
	a.saveDirty = false
	if err := config.Save(a.settingsPath, a.settings); err != nil {
		fmt.Fprintln(os.Stderr, "atlas-commander: saving settings:", err)
	}
}

// monoFont returns the data font at scale times the UI font size. The
// caller owns it and must Delete it.
func (a *App) monoFont(scale float64) *qt.QFont {
	base := qt.QApplication_Font()
	f := qt.NewQFont2(a.mono)
	f.SetStyleHint(qt.QFont__Monospace)
	f.SetPointSizeF(base.PointSizeF() * scale)
	return f
}

func upper(s string) string { return strings.ToUpper(s) }

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

// report shows a failed command to the user. Nothing in the UI fails
// silently: a button that did nothing must say why.
func (a *App) report(err error) {
	if err == nil {
		return
	}
	box := qt.NewQMessageBox6(qt.QMessageBox__Warning, "Atlas Commander", err.Error(), qt.QMessageBox__Ok, a.win.QWidget)
	box.Exec()
	box.DeleteLater()
}

// openAgent shows an agent's detail view.
func (a *App) openAgent(id string) {
	d, ok := a.pages["agent"].(*detailPage)
	if !ok {
		d = newDetailPage(a)
		a.addPage("agent", d)
	}
	d.setAgent(id)
	a.side.active = ""
	a.side.W.Update()
	a.current = "agent"
	a.stack.SetCurrentWidget(d.widget())
	if a.snap != nil {
		d.refresh(a.snap)
	}
}
