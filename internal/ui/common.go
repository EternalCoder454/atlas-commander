package ui

import (
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	qt "github.com/mappu/miqt/qt6"
	"github.com/mappu/miqt/qt6/mainthread"

	"atlas-commander/internal/fleet"
)

// post runs f on the Qt thread without waiting. It is the only way code off
// the main goroutine may reach a widget.
func post(f func()) { mainthread.Start(f) }

// setProp sets a dynamic property the style sheet selects on (mono, caption,
// accent, danger, status) and re-polishes so the new rule applies at once.
// The QVariant is copied by Qt, so it is freed here.
func setProp(w *qt.QWidget, name string, v any) {
	var q *qt.QVariant
	switch v := v.(type) {
	case bool:
		q = qt.NewQVariant8(v)
	case string:
		q = qt.NewQVariant11(v)
	default:
		panic(fmt.Sprintf("setProp: unsupported %T", v))
	}
	w.SetProperty(name, q)
	q.Delete()
	st := w.Style()
	st.Unpolish(w)
	st.Polish(w)
}

// liveLabel is a QLabel that only touches Qt when its text changes. The
// timer sets every live value four times a second; most of those are
// no-ops, and a QLabel relayouts its parent on every SetText.
type liveLabel struct {
	L    *qt.QLabel
	last string
}

func newLiveLabel(text string) *liveLabel {
	return &liveLabel{L: qt.NewQLabel3(text), last: text}
}

func (l *liveLabel) Set(s string) {
	if s != l.last {
		l.last = s
		l.L.SetText(s)
	}
}

// caption makes a dim secondary label.
func caption(text string) *qt.QLabel {
	l := qt.NewQLabel3(text)
	setProp(l.QWidget, "caption", true)
	return l
}

// pageTitle is the 1.45em, weight 600 heading at the top of a view.
func pageTitle(text string) *qt.QLabel {
	l := qt.NewQLabel3(text)
	// Sized by the style sheet (QLabel[title]): a style sheet font rule
	// beats SetFont, so the size has to live there.
	setProp(l.QWidget, "title", true)
	return l
}

// newPageLayout gives a view Monitor's page margins (18 px) and block
// spacing (14 px).
func newPageLayout(w *qt.QWidget) *qt.QVBoxLayout {
	l := qt.NewQVBoxLayout(w)
	l.SetContentsMargins(18, 18, 18, 18)
	l.SetSpacing(14)
	return l
}

// placeholderPage stands in for a view that has not been built.
type placeholderPage struct{ w *qt.QWidget }

func newPlaceholderPage(id string) *placeholderPage {
	w := qt.NewQWidget2()
	l := newPageLayout(w)
	l.AddWidget(pageTitle(strings.ToUpper(id[:1]) + id[1:]).QWidget)
	l.AddWidget(caption("Not built yet.").QWidget)
	l.AddStretch()
	return &placeholderPage{w: w}
}

func (p *placeholderPage) widget() *qt.QWidget       { return p.w }
func (p *placeholderPage) refresh(s *fleet.Snapshot) {}

// Formatting. Every number on screen goes through these so the board, the
// detail view and analytics agree on units and precision.

func fmtFloat(v float64) string {
	switch {
	case v == 0:
		return "0"
	case math.Abs(v) >= 100:
		return fmt.Sprintf("%.0f", v)
	case math.Abs(v) >= 10:
		return fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf("%.2f", v)
}

// fmtTokens is 0, 950, 12.4k, 3.10M.
func fmtTokens(n int64) string {
	f := float64(n)
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 100_000:
		return fmt.Sprintf("%.1fk", f/1e3)
	case n < 1_000_000:
		return fmt.Sprintf("%.0fk", f/1e3)
	case n < 100_000_000:
		return fmt.Sprintf("%.2fM", f/1e6)
	}
	return fmt.Sprintf("%.0fM", f/1e6)
}

// fmtUSD keeps four decimals below a cent so a cheap agent does not read
// as free, two above.
func fmtUSD(v float64) string {
	switch {
	case v == 0:
		return "$0.00"
	case v < 0.01:
		return fmt.Sprintf("$%.4f", v)
	case v < 1000:
		return fmt.Sprintf("$%.2f", v)
	}
	return fmt.Sprintf("$%.0f", v)
}

// fmtRate is tokens per second.
func fmtRate(v float64) string {
	if v <= 0 {
		return "0/s"
	}
	if v >= 1000 {
		return fmt.Sprintf("%.1fk/s", v/1000)
	}
	return fmt.Sprintf("%.0f/s", v)
}

// fmtDur is a latency or turn time: 850ms, 4.2s, 3m10s.
func fmtDur(d time.Duration) string {
	switch {
	case d <= 0:
		return "–"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// fmtAgo is a coarse relative time for "last tool" columns.
func fmtAgo(t time.Time, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < 5*time.Second:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func lastOr(v []float64, def float64) float64 {
	if len(v) == 0 {
		return def
	}
	return v[len(v)-1]
}

// firstLine trims s to its first line, for one-line cells.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

// setName sets objectName through the Q_PROPERTY. MIQT's SetObjectName takes
// a QAnyStringView built from a temporary QString that is gone by the time
// Qt reads it.
func setName(w *qt.QWidget, name string) {
	q := qt.NewQVariant11(name)
	w.SetProperty("objectName", q)
	q.Delete()
}

// dropdown is a compact choice, as Monitor uses for anything with several
// answers. on is called with the index the user picks, not when sel is set.
func dropdown(labels []string, sel int, on func(i int)) *qt.QComboBox {
	c := qt.NewQComboBox2()
	c.AddItems(labels)
	c.SetCurrentIndex(sel)
	c.OnActivated(on)
	return c
}

// commandButton is a flat button for a command bar, as Monitor's are: an icon
// (when there is one) and a word, filled only on hover.
func (a *App) commandButton(text, icon, tip string) *qt.QPushButton {
	b := qt.NewQPushButton3(text)
	setProp(b.QWidget, "command", true)
	b.SetToolTip(tip)
	if icon != "" {
		a.buttonIcon(b, icon)
	}
	return b
}

// agentControls are the Hold, Resume, Stop and Kill commands for the agent
// id() names, shared by the board's command bar and the detail view so the two
// cannot drift apart. They do nothing while id() is empty.
func (a *App) agentControls(id func() string) (hold, resume, stop, kill *qt.QPushButton) {
	mk := func(text, tip string, f func(id string) error) *qt.QPushButton {
		b := a.commandButton(text, "", tip)
		b.OnClicked(func() {
			if v := id(); v != "" {
				a.report(f(v))
			}
		})
		return b
	}
	hold = mk("Hold", "Stop the agent at its next tool call", a.ctl.Hold)
	resume = mk("Resume", "Let a held agent continue", a.ctl.Resume)
	stop = mk("Stop", "End the session after asking the agent to stop", a.ctl.Stop)
	kill = mk("Kill", "Force-stop the agent's processes now", a.ctl.Kill)
	setProp(kill.QWidget, "danger", true)
	return
}

// statTile is a card with a dim caption over a large monospace figure.
type statTile struct {
	W     *qt.QFrame
	value *liveLabel
	note  *liveLabel
}

func newStatTile(title string) *statTile {
	t := &statTile{W: qt.NewQFrame2(), value: newLiveLabel("–"), note: newLiveLabel("")}
	setProp(t.W.QWidget, "card", true)
	l := qt.NewQVBoxLayout(t.W.QWidget)
	l.SetContentsMargins(14, 10, 14, 12)
	l.SetSpacing(2)
	l.AddWidget(caption(title).QWidget)
	setProp(t.value.L.QWidget, "mono", true)
	setProp(t.value.L.QWidget, "figure", true)
	l.AddWidget(t.value.L.QWidget)
	setProp(t.note.L.QWidget, "caption", true)
	l.AddWidget(t.note.L.QWidget)
	return t
}

func (t *statTile) set(value, note string) {
	t.value.Set(value)
	t.note.Set(note)
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}
