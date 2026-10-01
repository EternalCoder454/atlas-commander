package ui

import (
	qt "github.com/mappu/miqt/qt6"

	"atlas-commander/internal/fleet"
)

// buildTray adds the tray icon, when the desktop has a tray, and routes the
// supervisor's notices to it. Notices are shown only while the window is
// not the active one: someone looking at Commander already sees them.
func (a *App) buildTray() {
	a.ctl.SetNotifier(func(n fleet.Notice) { post(func() { a.notify(n) }) })
	if !qt.QSystemTrayIcon_IsSystemTrayAvailable() {
		return
	}
	icon := markIcon()
	a.tray = qt.NewQSystemTrayIcon2(icon)
	icon.Delete()
	a.tray.SetToolTip("Atlas Commander")

	menu := qt.NewQMenu2()
	menu.AddActionWithText("Show Atlas Commander").OnTriggered(a.Raise)
	menu.AddActionWithText("Approvals").OnTriggered(func() {
		a.Raise()
		post(func() { a.side.select_("approvals") })
	})
	menu.AddSeparator()
	menu.AddActionWithText("Kill all agents…").OnTriggered(func() {
		a.Raise()
		post(a.confirmKillAll)
	})
	menu.AddSeparator()
	menu.AddActionWithText("Quit").OnTriggered(func() { a.win.Close() })
	a.tray.SetContextMenu(menu)
	a.tray.OnActivated(func(r qt.QSystemTrayIcon__ActivationReason) {
		if r == qt.QSystemTrayIcon__Trigger {
			a.Raise()
		}
	})
	a.tray.OnMessageClicked(func() {
		a.Raise()
		if id := a.lastNotice.AgentID; id != "" {
			post(func() { a.openAgent(id) })
		}
	})
	a.tray.Show()
}

func (a *App) notify(n fleet.Notice) {
	// Any window of Commander's in front, a dialog included, means the person
	// is looking at it already.
	if !a.settings.Notifications || a.tray == nil || a.win.IsActiveWindow() || qt.QApplication_ActiveWindow() != nil {
		return
	}
	a.lastNotice = n
	if n.Error {
		a.tray.ShowMessage5(n.Title, n.Body, qt.QSystemTrayIcon__Warning, 10000)
		return
	}
	if a.trayIcon == nil {
		a.trayIcon = markIcon()
	}
	a.tray.ShowMessage3(n.Title, n.Body, a.trayIcon, 8000)
}
