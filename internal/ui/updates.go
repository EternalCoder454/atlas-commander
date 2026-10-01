package ui

import (
	"fmt"
	"strings"

	qt "github.com/mappu/miqt/qt6"

	"atlas-commander/internal/update"
)

// updateCheck is what one background check found. The install is detected in
// the same goroutine, because telling a distro package from a checkout runs
// package-manager commands that can take a moment.
type updateCheck struct {
	info update.Info
	err  error
	in   update.Install
}

// updateUI is the update system's state on the Qt side: the last check, the
// "Update available" pill in the header, and whatever on Settings shows the
// result. A check runs in a goroutine that only writes into load; the tick
// takes the result and updates widgets, so no goroutine touches a widget.
type updateUI struct {
	app  *App
	load bgLoad[updateCheck]

	checking bool
	checked  bool
	// gen counts channel changes and tags each check with the one it started
	// under; inflight is the tag of the check now running. A check answers for
	// the channel it was asked about, so a result from an older gen is dropped
	// and the check is run again for the current channel.
	gen, inflight uint64
	res      updateCheck

	pill   *qt.QPushButton
	render []func() // redraw what shows the result, e.g. Settings' Updates card
}

func newUpdateUI(a *App) *updateUI { return &updateUI{app: a} }

// available is whether the last check found an update that is worth telling
// the user about unasked. A channel switch is theirs to ask for in Settings.
func (u *updateUI) available() bool {
	return u.checked && u.res.err == nil && u.res.info.Available && !u.res.info.Switch
}

// startCheck asks the chosen channel what it has. Demo mode never goes to the
// network: a screenshot or a demo must not depend on, or announce, anything
// online, so it reports up to date.
func (u *updateUI) startCheck() {
	if u.checking {
		return
	}
	if u.app.demo {
		u.deliver(updateCheck{info: update.Info{Summary: "Up to date"}})
		return
	}
	channel := u.app.settings.UpdateChannel
	local := strings.TrimSpace(u.app.version)
	if u.load.start(u.gen, func() (updateCheck, error) {
		in := update.Detect()
		info, err := update.Check(in, channel, local)
		return updateCheck{info: info, err: err, in: in}, nil
	}) {
		u.checking, u.inflight = true, u.gen
		u.redraw()
	}
}

// poll is called from the tick: it takes a finished check, if there is one.
func (u *updateUI) poll() {
	if res, _, ok := u.load.take(u.gen); ok {
		u.checking = false
		u.deliver(res)
		return
	}
	// The channel changed while a check ran: take dropped its answer, which
	// described the old channel. Ask again for the new one.
	if u.checking && u.inflight != u.gen && !u.load.running() {
		u.checking = false
		u.startCheck()
	}
}

func (u *updateUI) deliver(res updateCheck) {
	u.res, u.checked = res, true
	u.redraw()
}

// forget drops the last answer, after the channel changed and it no longer
// describes what the user chose.
func (u *updateUI) forget() {
	u.gen++
	u.checked = false
	u.res = updateCheck{}
	u.redraw()
}

func (u *updateUI) redraw() {
	if u.pill != nil {
		u.pill.SetVisible(u.available())
	}
	for _, f := range u.render {
		f()
	}
}

// addPill puts the small "Update available" button in the header. It is hidden
// until a check finds something, and opens the update dialog.
func (u *updateUI) addPill(l *qt.QHBoxLayout, at int) {
	u.pill = qt.NewQPushButton3("Update available")
	setProp(u.pill.QWidget, "accent", true)
	u.pill.SetToolTip("A newer version of Atlas Commander is out")
	u.pill.SetVisible(false)
	u.pill.OnClicked(u.openDialog)
	l.InsertWidget(at, u.pill.QWidget)
}

// status is the sentence under the version on Settings.
func (u *updateUI) status() string {
	channel := update.ChannelLabel(u.app.settings.UpdateChannel)
	switch {
	case u.checking:
		return "Checking for updates…"
	case !u.checked:
		return "On the " + channel + " channel"
	case u.res.err != nil:
		return capitalise(u.res.err.Error())
	case u.res.info.Available && u.res.info.Switch:
		return "Switch to " + channel + ": " + versionOr(u.res.info.Version, "the newest build") + " is ready"
	case u.res.info.Available:
		return "Version " + versionOr(u.res.info.Version, "newer than yours") + " is available"
	}
	return "Up to date"
}

func versionOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// updateCards is the Settings section: the version with its button, the
// channel, and whether to check at launch.
func (p *settingsPage) updateCards() {
	a, u, s := p.app, p.app.updates, p.settings()

	btn := qt.NewQPushButton3("Check for updates")
	c := p.card()
	head := c.head("update", "Atlas Commander "+strings.TrimSpace(a.version), u.status(), btn.QWidget)
	redraw := func() {
		head.setSub(u.status())
		btn.SetEnabled(!u.checking)
		if u.available() || (u.checked && u.res.err == nil && u.res.info.Available) {
			btn.SetText("Update…")
			setProp(btn.QWidget, "accent", true)
		} else {
			btn.SetText("Check for updates")
			setProp(btn.QWidget, "accent", false)
		}
	}
	btn.OnClicked(func() {
		if u.checked && u.res.err == nil && u.res.info.Available {
			u.openDialog()
		} else {
			u.startCheck()
		}
	})

	channels := update.ChannelLabel
	labels := []string{channels(update.ChannelRelease), channels(update.ChannelBeta)}
	sel := 0
	if s.UpdateChannel == update.ChannelBeta {
		sel = 1
	}
	seg := dropdown(labels, sel, func(i int) {
		s.UpdateChannel = []string{update.ChannelRelease, update.ChannelBeta}[i]
		p.save()
		u.forget()
		u.startCheck()
	})
	c.sub("Channel", "Release is the stable version. Beta gets new features and fixes first, and may be rougher.", seg.QWidget)

	atLaunch := newToggle(a, s.UpdateCheck, func(on bool) {
		s.UpdateCheck = on
		p.save()
	})
	c.sub("Check for updates at launch",
		"Asks GitHub once when Commander opens, and only speaks up if there is something newer. Nothing installs without your click.",
		atLaunch.w)

	u.render = append(u.render, redraw)
	redraw()
}

// openDialog shows what can be done about the update the last check found,
// which depends on how this copy was installed.
func (u *updateUI) openDialog() {
	a, in, info := u.app, u.res.in, u.res.info
	channel := a.settings.UpdateChannel

	dlg := qt.NewQDialog(a.win.QWidget)
	defer dlg.DeleteLater()
	dlg.SetWindowTitle("Atlas Commander update")
	dlg.Resize(520, 0)
	col := qt.NewQVBoxLayout(dlg.QWidget)
	col.SetContentsMargins(20, 18, 20, 18)
	col.SetSpacing(10)

	heading, body := "Update Atlas Commander", ""
	switch {
	case info.Switch:
		heading = "Switch to the " + update.ChannelLabel(channel) + " channel"
		body = "Commander will move to the " + update.ChannelLabel(channel) + " channel and rebuild."
	case info.Version != "":
		body = "Version " + info.Version + " is available. You have " + strings.TrimSpace(a.version) + "."
	default:
		body = "A newer version is available."
	}
	if !in.SelfUpdatable() {
		heading, _ = update.Wording(in)
	}
	h := qt.NewQLabel3(heading)
	setProp(h.QWidget, "title", true)
	col.AddWidget(h.QWidget)
	col.AddWidget(wrapLabel(body).QWidget)
	if len(info.Changes) > 0 {
		col.AddWidget(wrapLabel("What's new").QWidget)
		col.AddWidget(wrapCaption("•  " + strings.Join(info.Changes, "\n•  ")).QWidget)
	}

	buttons := qt.NewQHBoxLayout2()
	buttons.AddStretch()
	closeBtn := qt.NewQPushButton3("Close")
	closeBtn.OnClicked(dlg.Reject)

	switch {
	case in.SelfUpdatable():
		u.selfUpdateBody(dlg, col, buttons, closeBtn, in, channel)
	case in.Kind == update.FromPackage:
		_, explain := update.Wording(in)
		col.AddWidget(wrapCaption(explain).QWidget)
		cmd := in.UpdateCommand()
		line := wrapLabel(cmd)
		setProp(line.QWidget, "mono", true)
		line.SetTextInteractionFlags(qt.TextSelectableByMouse)
		col.AddWidget(line.QWidget)
		cp := qt.NewQPushButton3("Copy command")
		cp.OnClicked(func() {
			qt.QGuiApplication_Clipboard().SetText(cmd)
			cp.SetText("Copied")
		})
		setProp(cp.QWidget, "accent", true)
		buttons.AddWidget(cp.QWidget)
		col.AddWidget(wrapCaption("Commander uses the new version the next time you start it.").QWidget)
	default:
		_, explain := update.Wording(in)
		col.AddWidget(wrapCaption(explain).QWidget)
		url := update.DownloadPage(channel)
		link := wrapCaption(url)
		link.SetTextInteractionFlags(qt.TextSelectableByMouse)
		col.AddWidget(link.QWidget)
		open := qt.NewQPushButton3("Open releases page")
		setProp(open.QWidget, "accent", true)
		open.OnClicked(func() {
			if !qt.QDesktopServices_OpenUrl(qt.NewQUrl3(url)) {
				open.SetText("Couldn't open a browser")
				open.SetEnabled(false)
			}
		})
		buttons.AddWidget(open.QWidget)
	}
	buttons.AddWidget(closeBtn.QWidget)
	col.AddLayout(buttons.QLayout)
	dlg.Exec()
}

// selfUpdateBody is the dialog for a source install: an Update button that runs
// scripts/update.sh in the background with a spinner, then restarts Commander.
func (u *updateUI) selfUpdateBody(dlg *qt.QDialog, col *qt.QVBoxLayout, buttons *qt.QHBoxLayout, closeBtn *qt.QPushButton, in update.Install, channel string) {
	a := u.app
	status := wrapLabel("")
	status.SetVisible(false)
	bar := qt.NewQProgressBar2()
	bar.SetRange(0, 0) // no known length: it just shows that something is happening
	bar.SetTextVisible(false)
	bar.SetVisible(false)

	details := qt.NewQPlainTextEdit2()
	details.SetReadOnly(true)
	setProp(details.QWidget, "mono", true)
	details.SetMinimumHeight(160)
	details.SetVisible(false)
	toggle := qt.NewQPushButton3("Technical details")
	toggle.SetCheckable(true)
	toggle.SetVisible(false)
	toggle.OnToggled(details.SetVisible)
	copyLog := qt.NewQPushButton3("Copy log")
	copyLog.SetVisible(false)
	copyLog.OnClicked(func() {
		qt.QGuiApplication_Clipboard().SetText(details.ToPlainText())
		copyLog.SetText("Copied")
	})

	col.AddWidget(status.QWidget)
	col.AddWidget(bar.QWidget)
	col.AddWidget(details.QWidget)
	buttons.InsertWidget(0, toggle.QWidget)
	buttons.InsertWidget(1, copyLog.QWidget)

	start := qt.NewQPushButton3("Update now")
	setProp(start.QWidget, "accent", true)
	buttons.AddWidget(start.QWidget)

	var run bgLoad[error]
	running := false
	// A running build must not be cut off by Esc or the window's close button:
	// half an install is worse than a slow one.
	dlg.OnReject(func(super func()) {
		if !running {
			super()
		}
	})
	dlg.OnCloseEvent(func(super func(*qt.QCloseEvent), ev *qt.QCloseEvent) {
		if running {
			ev.Ignore()
			return
		}
		super(ev)
	})

	finish := func(err error) {
		running = false
		bar.SetVisible(false)
		closeBtn.SetEnabled(true)
		if err == nil {
			status.SetText("Updated. Restarting Commander…")
			closeBtn.SetEnabled(false)
			t := qt.NewQTimer2(dlg.QObject)
			t.SetSingleShot(true)
			t.OnTimeout(func() {
				if in.Binary == "" {
					status.SetText("Updated. Start Commander again to use the new version.")
					closeBtn.SetEnabled(true)
					return
				}
				if rerr := update.Relaunch(in.Binary); rerr != nil {
					status.SetText("Updated, but Commander couldn't restart itself. Start it again to use the new version.")
					closeBtn.SetEnabled(true)
					return
				}
				dlg.Accept()
				a.Quit()
			})
			t.Start(900)
			return
		}
		if err == update.ErrNotUpdated {
			status.SetText("Commander was rebuilt, but couldn't bring in the newest version. Check your connection and that the folder it was built from has no uncommitted changes, then try again.")
		} else {
			status.SetText("The update didn't finish. Commander may be in the state it was in before.")
		}
		details.SetPlainText(err.Error() + "\n\n" + update.LogTail(8000))
		toggle.SetVisible(true)
		copyLog.SetVisible(true)
		start.SetText("Try again")
		start.SetEnabled(true)
	}

	poll := qt.NewQTimer2(dlg.QObject)
	poll.OnTimeout(func() {
		if err, _, ok := run.take(2); ok {
			poll.Stop()
			finish(err)
		}
	})

	start.OnClicked(func() {
		if !u.confirmWithLiveAgents(dlg) {
			return
		}
		running = true
		start.SetEnabled(false)
		closeBtn.SetEnabled(false)
		toggle.SetVisible(false)
		copyLog.SetVisible(false)
		details.SetVisible(false)
		toggle.SetChecked(false)
		status.SetText("Updating… this rebuilds Commander and can take a few minutes. Keep this window open.")
		status.SetVisible(true)
		bar.SetVisible(true)
		run.start(2, func() (error, error) { return update.Run(in, channel), nil })
		poll.Start(int(tickInterval.Milliseconds()))
	})
}

// confirmWithLiveAgents asks first when restarting would stop agents that are
// working: an update ends in a restart, and a restart ends their sessions.
func (u *updateUI) confirmWithLiveAgents(parent *qt.QDialog) bool {
	live := 0
	if u.app.snap != nil {
		for _, ag := range u.app.snap.Agents {
			if ag.Status.Live() {
				live++
			}
		}
	}
	if live == 0 {
		return true
	}
	box := qt.NewQMessageBox6(qt.QMessageBox__Warning, "Agents are running",
		fmt.Sprintf("%d running agent(s) will be stopped when Commander restarts to finish the update. Update anyway?", live),
		qt.QMessageBox__Cancel, parent.QWidget)
	defer box.DeleteLater()
	yes := box.AddButton2("Update anyway", qt.QMessageBox__AcceptRole)
	box.SetDefaultButtonWithButton(qt.QMessageBox__Cancel)
	box.Exec()
	return box.ClickedButton() == yes.QAbstractButton
}

func wrapLabel(text string) *qt.QLabel {
	l := qt.NewQLabel3(text)
	l.SetWordWrap(true)
	return l
}

func wrapCaption(text string) *qt.QLabel {
	l := caption(text)
	l.SetWordWrap(true)
	return l
}
