package ui

import (
	"cmp"
	"fmt"
	"html"
	"slices"
	"strings"
	"time"

	qt "github.com/mappu/miqt/qt6"

	"atlas-commander/internal/fleet"
)

// approvalsPage is the gate queue: every tool call waiting on a human, oldest
// first, with the full input of the selected one and Allow / Deny.
type approvalsPage struct {
	app  *App
	w    *qt.QWidget
	seq  uint64
	now  time.Time
	rows []fleet.ApprovalView

	summary *liveLabel
	list    *listView
	table   *dataTable

	card   *qt.QFrame
	head   *qt.QLabel
	input  *qt.QPlainTextEdit
	reason *qt.QLineEdit
	allow  *qt.QPushButton
	deny   *qt.QPushButton
	shown  string // approval id in the card
	// seeded is set after the first snapshot; no approvals is a valid state.
	seeded bool
	// btnOn mirrors the Allow / Deny enabled state so the tick only touches
	// the widgets when it flips.
	btnOn bool
}

// maxPlain caps text handed to a QPlainTextEdit: a tool input can be a whole
// file, and laying it out would stall the Qt thread.
const maxPlain = 256 << 10

func capPlain(s string) string {
	if len(s) <= maxPlain {
		return s
	}
	return strings.ToValidUTF8(s[:maxPlain], "") + "\n… truncated"
}

func (p *approvalsPage) setButtons(on bool) {
	if on == p.btnOn {
		return
	}
	p.btnOn = on
	p.allow.SetEnabled(on)
	p.deny.SetEnabled(on)
}

var approvalCols = []tableCol{
	{title: "Waiting", width: 96, mono: true, right: true},
	{title: "Agent", width: 170},
	{title: "Tool", width: 110, mono: true},
	{title: "Request", mono: true},
}

func newApprovalsPage(a *App) *approvalsPage {
	p := &approvalsPage{app: a, w: qt.NewQWidget2()}
	l := newPageLayout(p.w)

	top := qt.NewQHBoxLayout2()
	top.SetSpacing(12)
	top.AddWidget(pageTitle("Approvals").QWidget)
	p.summary = newLiveLabel("")
	setProp(p.summary.L.QWidget, "caption", true)
	top.AddWidget(p.summary.L.QWidget)
	top.AddStretch()
	l.AddLayout(top.QLayout)

	p.table = newDataTable(a, approvalCols, p.cell)
	p.table.tip = func(r, c int) string {
		if c == 3 {
			return p.rows[r].Summary
		}
		return ""
	}
	p.table.onSelect = func(string) { p.showSelected() }
	p.table.onActivate = func(string) { p.reason.SetFocus() }
	p.list = newListView(p.table, "Nothing waiting",
		"When an agent wants to run a tool you've marked for approval, it shows up here. Allow or deny it, and the agent carries on.")
	l.AddWidget2(p.list.W.QWidget, 3)

	p.card = qt.NewQFrame2()
	setProp(p.card.QWidget, "card", true)
	cl := qt.NewQVBoxLayout(p.card.QWidget)
	cl.SetContentsMargins(14, 12, 14, 14)
	cl.SetSpacing(10)
	p.head = qt.NewQLabel3("")
	p.head.SetWordWrap(true)
	cl.AddWidget(p.head.QWidget)
	p.input = qt.NewQPlainTextEdit2()
	p.input.SetReadOnly(true)
	setProp(p.input.QWidget, "mono", true)
	p.input.SetLineWrapMode(qt.QPlainTextEdit__WidgetWidth)
	cl.AddWidget(p.input.QWidget)

	row := qt.NewQHBoxLayout2()
	row.SetSpacing(6)
	p.reason = qt.NewQLineEdit2()
	p.reason.SetPlaceholderText("Reason (optional): the agent sees it when you deny")
	// Enter must not deny: a stray keypress would refuse a tool call. Deny
	// only happens through its button.
	row.AddWidget(p.reason.QWidget)
	p.deny = qt.NewQPushButton3("Deny")
	setProp(p.deny.QWidget, "danger", true)
	p.deny.SetToolTip("Refuse this tool call. The agent is told why and carries on.")
	p.deny.OnClicked(func() { p.decide(false) })
	row.AddWidget(p.deny.QWidget)
	p.allow = qt.NewQPushButton3("Allow")
	setProp(p.allow.QWidget, "accent", true)
	p.allow.SetToolTip("Let this tool call run")
	p.allow.OnClicked(func() { p.decide(true) })
	row.AddWidget(p.allow.QWidget)
	cl.AddLayout(row.QLayout)
	l.AddWidget2(p.card.QWidget, 2)
	p.btnOn = true
	p.card.Hide()
	return p
}

func (p *approvalsPage) widget() *qt.QWidget { return p.w }

func (p *approvalsPage) cell(r, c int) cell {
	ap := &p.rows[r]
	switch c {
	case 0:
		d := p.now.Sub(ap.At)
		tone := fleet.ToneWarn
		if d < 30*time.Second {
			tone = fleet.ToneIdle
		}
		return cell{text: fmtWait(d), tone: tone}
	case 1:
		return cell{text: ap.AgentName}
	case 2:
		return cell{text: ap.Tool, alpha: 0.75}
	case 3:
		return cell{text: ap.Summary}
	}
	return cell{}
}

func fmtWait(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
}

func (p *approvalsPage) refresh(s *fleet.Snapshot) {
	p.now = time.Now()
	if s.Seq == p.seq && p.seeded {
		p.table.T.Viewport().Update() // the waiting clock moves
		return
	}
	p.seq, p.seeded = s.Seq, true
	p.setButtons(true) // a decision disabled them until this snapshot
	p.rows = slices.Clone(s.Approvals)
	slices.SortStableFunc(p.rows, func(x, y fleet.ApprovalView) int {
		if c := x.At.Compare(y.At); c != 0 {
			return c
		}
		return cmp.Compare(x.ID, y.ID)
	})
	keys := make([]string, len(p.rows))
	agents := map[string]bool{}
	for i, r := range p.rows {
		keys[i] = r.ID
		agents[r.AgentID] = true
	}
	switch len(p.rows) {
	case 0:
		p.summary.Set("")
	default:
		p.summary.Set(fmt.Sprintf("%s from %s", plural(len(p.rows), "request"), plural(len(agents), "agent")))
	}
	p.list.update(keys)
	if p.table.selected == "" && len(keys) > 0 {
		p.table.selectKey(keys[0])
	}
	p.showSelected()
}

func (p *approvalsPage) current() *fleet.ApprovalView {
	for i := range p.rows {
		if p.rows[i].ID == p.table.selected {
			return &p.rows[i]
		}
	}
	return nil
}

func (p *approvalsPage) showSelected() {
	ap := p.current()
	if ap == nil {
		p.card.Hide()
		p.shown = ""
		return
	}
	p.card.Show()
	if ap.ID == p.shown {
		return
	}
	p.shown = ap.ID
	p.head.SetText(fmt.Sprintf("<b>%s</b> wants to run <b>%s</b>", html.EscapeString(ap.AgentName), html.EscapeString(ap.Tool)))
	p.input.SetPlainText(capPlain(strings.TrimSpace(ap.Input)))
	p.reason.Clear()
}

func (p *approvalsPage) decide(allow bool) {
	ap := p.current()
	if ap == nil {
		return
	}
	reason := strings.TrimSpace(p.reason.Text())
	if err := p.app.ctl.Decide(ap.ID, allow, reason); err != nil {
		p.app.report(err)
		return
	}
	// The request is gone as far as the gate is concerned; keep a second
	// click from deciding it again until the next snapshot says so.
	p.setButtons(false)
	// Move on to the next request without waiting for the snapshot.
	i := slices.IndexFunc(p.rows, func(x fleet.ApprovalView) bool { return x.ID == ap.ID })
	next := ""
	if i+1 < len(p.rows) {
		next = p.rows[i+1].ID
	} else if i > 0 {
		next = p.rows[i-1].ID
	}
	p.table.selectKey(next)
}
