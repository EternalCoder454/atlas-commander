package ui

import (
	"fmt"

	qt "github.com/mappu/miqt/qt6"
	"rsc.io/qr"

	"atlas-commander/internal/config"
	"atlas-commander/internal/remote"
)

// Phone is what Settings needs from the phone access server. remote.Host
// satisfies it. Enable and Disable are quick (they open or close a socket);
// Status never blocks, so the 250 ms tick can call it.
type Phone interface {
	Enable(port int)
	Disable()
	Forget() error
	Status() remote.Status
}

// phone is set once from main before Run, which also starts the server when
// the setting is on. A package variable rather than an
// Options field keeps this feature out of app.go; nil hides the Settings
// section, which is what tests and builds without the server get.
var phone Phone

// SetPhone gives Settings the phone access server. Call it before Run.
func SetPhone(p Phone) { phone = p }

// phoneCards is the Phone section of Settings: the switch, the port, the
// pairing QR code and link, and the way to forget paired phones. The server
// itself runs on goroutines of its own and only reports into Host's mutex;
// everything here runs on the Qt thread and pulls that status from refresh.
type phoneCards struct {
	status  *settingRow
	pairing *qt.QFrame
	qr      *qt.QLabel
	link    *qt.QLabel
	hint    *qt.QLabel
	last    remote.Status
}

// phoneSection adds the Phone section, or nothing when there is no server.
func (p *settingsPage) phoneSection() {
	if phone == nil {
		return
	}
	s := p.settings()
	pc := &phoneCards{}
	p.phone = pc

	p.section("Phone")
	c := p.card()
	toggle := newToggle(p.app, s.PhoneAccess, func(on bool) {
		s.PhoneAccess = on
		p.save()
		if on {
			phone.Enable(s.PhonePort)
		} else {
			phone.Disable()
		}
		p.phoneRefresh()
	})
	c.head("notifications", "Allow phone access",
		"Lets the Atlas Commander app on your phone watch your agents and answer approvals. Off until you turn it on.",
		toggle.w)

	port := qt.NewQSpinBox2()
	port.SetRange(config.MinPhonePort, config.MaxPhonePort)
	port.SetValue(s.PhonePort)
	port.SetMinimumWidth(100)
	// Without this the server would restart on every digit typed.
	port.SetKeyboardTracking(false)
	port.OnValueChanged(func(v int) {
		s.PhonePort = v
		p.save()
		if s.PhoneAccess {
			phone.Enable(v)
		}
		p.phoneRefresh()
	})
	c.sub("Port", "The phone connects here. Pick another if something else uses it.", port.QWidget)
	pc.status = c.sub("Status", "")

	pc.pairing = qt.NewQFrame2()
	setProp(pc.pairing.QWidget, "card", true)
	pl := qt.NewQVBoxLayout(pc.pairing.QWidget)
	pl.SetContentsMargins(settingIndent, 14, 14, 14)
	pl.SetSpacing(10)
	pl.AddWidget(wrapLabel("Scan this with the Atlas Commander app, or copy the link and paste it there.").QWidget)
	pc.qr = qt.NewQLabel2()
	pl.AddWidget3(pc.qr.QWidget, 0, qt.AlignLeft)
	pc.link = wrapCaption("")
	setProp(pc.link.QWidget, "mono", true)
	pc.link.SetTextInteractionFlags(qt.TextSelectableByMouse)
	pl.AddWidget(pc.link.QWidget)
	buttons := qt.NewQHBoxLayout2()
	cp := qt.NewQPushButton3("Copy link")
	cp.OnClicked(func() {
		qt.QGuiApplication_Clipboard().SetText(pc.last.Link)
		cp.SetText("Copied")
	})
	forget := qt.NewQPushButton3("Forget paired phones")
	forget.OnClicked(func() {
		if !confirm(p.app, "Forget paired phones?",
			"Every phone you paired stops working until you scan the new code. Agents keep running.", "Forget phones") {
			return
		}
		p.app.report(phone.Forget())
		cp.SetText("Copy link")
		p.phoneRefresh()
	})
	buttons.AddWidget(cp.QWidget)
	buttons.AddWidget(forget.QWidget)
	buttons.AddStretch()
	pl.AddLayout(buttons.QLayout)
	p.col.AddWidget(pc.pairing.QWidget)

	pc.hint = wrapCaption("")
	p.col.AddWidget(pc.hint.QWidget)

	p.phoneRefresh()
}

// phoneRefresh reads the server's status and brings the section in line. The
// QR code is only redrawn when the link changes.
func (p *settingsPage) phoneRefresh() {
	pc := p.phone
	if pc == nil || phone == nil {
		return
	}
	st := phone.Status()
	if st == pc.last {
		return
	}
	linkChanged := st.Link != pc.last.Link
	pc.last = st

	switch {
	case st.Err != "":
		pc.status.setSub(capitalise(st.Err))
	case st.On:
		pc.status.setSub("Listening on " + st.Listening)
	default:
		pc.status.setSub("Off")
	}
	showing := st.On && st.Err == "" && st.Link != ""
	pc.pairing.SetVisible(showing)
	pc.hint.SetVisible(st.On)
	pc.hint.SetText(fmt.Sprintf("If your phone can't connect, your firewall may be blocking the port. On Fedora: "+
		"sudo firewall-cmd --add-port=%d/tcp --permanent && sudo firewall-cmd --reload", st.Port))
	if showing && linkChanged {
		pc.link.SetText(st.Link)
		drawQR(pc.qr, st.Link)
	}
}

// drawQR paints the link as a QR code on a white square with the quiet zone
// scanners need. It is black on white whatever the theme: a dark-mode code
// would not scan.
func drawQR(l *qt.QLabel, text string) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		l.SetText("The pairing code couldn't be drawn. Copy the link instead.")
		return
	}
	const module, quiet = 5, 4
	n := code.Size + 2*quiet
	img := qt.NewQImage3(n*module, n*module, qt.QImage__Format_RGB32)
	defer img.Delete()
	img.Fill(0xffffffff)
	for y := range code.Size {
		for x := range code.Size {
			if !code.Black(x, y) {
				continue
			}
			for dy := range module {
				for dx := range module {
					img.SetPixel((x+quiet)*module+dx, (y+quiet)*module+dy, 0xff000000)
				}
			}
		}
	}
	pm := qt.QPixmap_FromImage(img)
	defer pm.Delete()
	l.SetPixmap(pm)
	l.SetFixedSize2(n*module, n*module)
}
