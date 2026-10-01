package ui

import (
	"fmt"
	"strings"
	"time"

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
	// PairingLink is a credential: ask for it only while showing it.
	PairingLink() string
}

const (
	// pairingShownFor is how long the QR code and link stay up once shown.
	pairingShownFor = 2 * time.Minute
	// clipboardKeep is how long a copied link stays on the clipboard.
	clipboardKeep = time.Minute
)

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
	reveal  *qt.QWidget // the QR code, link and Copy button; hidden until asked for
	show    *qt.QPushButton
	copy    *qt.QPushButton
	qr      *qt.QLabel
	link    *qt.QLabel
	hint    *qt.QLabel
	last    remote.Status

	// shownUntil is when the code hides itself; zero while hidden. shownLink
	// is what is drawn now, so the QR is only redrawn when it changes.
	shownUntil time.Time
	shownLink  string
	// clipLink is the link last copied, and clipAt when to take it back off
	// the clipboard; zero when nothing is pending.
	clipLink string
	clipAt   time.Time
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
	pl.AddWidget(wrapLabel("To pair a phone, show the code and scan it with the Atlas Commander app, or copy the link and paste it there. Anyone who gets the code can control your agents, so it hides again after two minutes.").QWidget)

	pc.reveal = qt.NewQWidget2()
	rl := qt.NewQVBoxLayout(pc.reveal)
	rl.SetContentsMargins(0, 0, 0, 0)
	rl.SetSpacing(10)
	pc.qr = qt.NewQLabel2()
	rl.AddWidget3(pc.qr.QWidget, 0, qt.AlignLeft)
	// Not selectable: the shown text carries break points that a hand-copied
	// link would take along. Copy link gives the exact text.
	pc.link = wrapCaption("")
	setProp(pc.link.QWidget, "mono", true)
	rl.AddWidget(pc.link.QWidget)
	pc.copy = qt.NewQPushButton3("Copy link")
	pc.copy.OnClicked(func() {
		link := phone.PairingLink()
		if link == "" {
			return
		}
		qt.QGuiApplication_Clipboard().SetText(link)
		pc.clipLink, pc.clipAt = link, time.Now().Add(clipboardKeep)
		pc.copy.SetText("Copied")
	})
	rl.AddWidget3(pc.copy.QWidget, 0, qt.AlignLeft)
	pc.reveal.SetVisible(false)
	pl.AddWidget(pc.reveal)

	buttons := qt.NewQHBoxLayout2()
	pc.show = qt.NewQPushButton3("Show pairing code")
	pc.show.OnClicked(func() {
		if pc.shownUntil.IsZero() {
			pc.shownUntil = time.Now().Add(pairingShownFor)
		} else {
			pc.shownUntil = time.Time{}
		}
		p.phoneRefresh()
	})
	forget := qt.NewQPushButton3("Forget paired phones")
	forget.OnClicked(func() {
		if !confirm(p.app, "Forget paired phones?",
			"Every phone you paired stops working until you scan the new code. Agents keep running.", "Forget phones") {
			return
		}
		p.app.report(phone.Forget())
		pc.copy.SetText("Copy link")
		pc.shownLink = ""
		p.phoneRefresh()
	})
	buttons.AddWidget(pc.show.QWidget)
	buttons.AddWidget(forget.QWidget)
	buttons.AddStretch()
	pl.AddLayout(buttons.QLayout)
	p.col.AddWidget(pc.pairing.QWidget)

	pc.hint = wrapCaption("")
	p.col.AddWidget(pc.hint.QWidget)

	p.phoneRefresh()
}

// phoneRefresh reads the server's status and brings the section in line. The
// pairing link is only fetched while the code is shown, and the code hides
// itself after pairingShownFor.
func (p *settingsPage) phoneRefresh() {
	pc := p.phone
	if pc == nil || phone == nil {
		return
	}
	st := phone.Status()
	if !pc.shownUntil.IsZero() && (!time.Now().Before(pc.shownUntil) || !st.On || st.Err != "") {
		pc.shownUntil = time.Time{}
	}
	shown := !pc.shownUntil.IsZero()
	if st != pc.last {
		pc.last = st
		switch {
		case st.Err != "":
			pc.status.setSub(capitalise(st.Err))
		case st.On && st.Note != "":
			pc.status.setSub("Listening on " + st.Listening + ". " + st.Note)
		case st.On:
			pc.status.setSub("Listening on " + st.Listening)
		default:
			pc.status.setSub("Off")
		}
		pc.pairing.SetVisible(st.On && st.Err == "")
		pc.hint.SetVisible(st.On)
		pc.hint.SetText(fmt.Sprintf("If your phone can't connect, your firewall may be blocking the port. On Fedora: "+
			"sudo firewall-cmd --add-port=%d/tcp --permanent && sudo firewall-cmd --reload", st.Port))
	}
	pc.reveal.SetVisible(shown)
	if shown {
		pc.show.SetText("Hide pairing code")
		if link := phone.PairingLink(); link != pc.shownLink {
			pc.shownLink = link
			pc.link.SetText(breakable(link))
			drawQR(pc.qr, link)
		}
	} else {
		pc.show.SetText("Show pairing code")
		if pc.shownLink != "" {
			pc.shownLink = ""
			pc.link.SetText("")
			pc.qr.Clear()
		}
		pc.copy.SetText("Copy link")
	}
}

// phoneHide hides the pairing code at once, for when the user leaves the page.
func (p *settingsPage) phoneHide() {
	if p.phone != nil && !p.phone.shownUntil.IsZero() {
		p.phone.shownUntil = time.Time{}
		p.phoneRefresh()
	}
}

// phoneClipboardTick takes the pairing link back off the clipboard once its
// time is up, but only if the clipboard still holds it: something the user
// copied since is theirs. It runs from the app's tick, not the page's, so it
// still fires after the user has moved to another page.
func (p *settingsPage) phoneClipboardTick() {
	pc := p.phone
	if pc == nil || pc.clipAt.IsZero() || time.Now().Before(pc.clipAt) {
		return
	}
	cb := qt.QGuiApplication_Clipboard()
	if cb.Text() == pc.clipLink {
		cb.Clear()
	}
	pc.clipLink, pc.clipAt = "", time.Time{}
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
	l.SetPixmap(pm)
	l.SetFixedSize2(n*module, n*module)
}

// breakable puts a zero-width space every few characters so a word-wrapped
// label can break the pairing link, which has no spaces. Without them the
// label is as wide as the whole link and pushes the Settings page past the
// window's right edge.
func breakable(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n > 0 && n%8 == 0 {
			b.WriteRune('\u200b')
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
